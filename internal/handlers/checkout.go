package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"roveni/internal/mailer"
	"roveni/internal/tickets"

	"github.com/redis/go-redis/v9"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/paymentintent"
)

// isZeroDecimalCurrency checks if currency is zero-decimal in Stripe API
func generateNextOrderNumber(db *sql.DB) string {
	var maxNum int
	err := db.QueryRow(`
		SELECT COALESCE(
			MAX(
				CASE 
					WHEN order_number ~ '^RV-[0-9]+$' 
					THEN CAST(SUBSTRING(order_number FROM 4) AS INTEGER) 
					ELSE id 
				END
			), 
			0
		) FROM purchases
	`).Scan(&maxNum)
	if err != nil || maxNum < 0 {
		maxNum = 0
	}
	nextVal := maxNum + 1
	return fmt.Sprintf("RV-%06d", nextVal)
}

func isZeroDecimalCurrency(currency string) bool {
	c := strings.ToUpper(strings.TrimSpace(currency))
	switch c {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return true
	default:
		return false
	}
}

// toStripeAmount converts a monetary amount to the smallest currency unit required by Stripe
func toStripeAmount(amount float64, currency string) int64 {
	c := strings.ToUpper(strings.TrimSpace(currency))
	switch c {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		// Zero-decimal currencies in Stripe
		return int64(math.Round(amount))
	case "BHD", "JOD", "KWD", "OMR", "TND":
		// Three-decimal currencies in Stripe
		return int64(math.Round(amount * 1000))
	default:
		// Standard two-decimal currencies (USD, MXN, COP, PEN, EUR, ARS, BRL, CAD, GBP, etc.)
		return int64(math.Round(amount * 100))
	}
}

type TicketOrderInput struct {
	ID       int64 `json:"id"`
	Quantity int   `json:"quantity"`
}

type CreateIntentRequest struct {
	EventID       int64              `json:"event_id"`
	CustomerName  string             `json:"customer_name"`
	CustomerEmail string             `json:"customer_email"`
	CustomerPhone string             `json:"customer_phone"`
	Currency      string             `json:"currency"`
	ExchangeRate  float64            `json:"exchange_rate"`
	Tickets       []TicketOrderInput `json:"tickets"`
}

type ConfirmCheckoutRequest struct {
	OrderNumber           string             `json:"order_number"`
	EventID               int64              `json:"event_id"`
	CustomerName          string             `json:"customer_name"`
	CustomerEmail         string             `json:"customer_email"`
	CustomerPhone         string             `json:"customer_phone"`
	StripePaymentIntentID string             `json:"stripe_payment_intent_id"`
	Currency              string             `json:"currency"`
	ExchangeRate          float64            `json:"exchange_rate"`
	Tickets               []TicketOrderInput `json:"tickets"`
}

// CreatePaymentIntentHandler validates ticket availability, calculates totals in the requested currency, and creates a Stripe PaymentIntent.
func CreatePaymentIntentHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req CreateIntentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[CHECKOUT ERROR] Failed to decode request body: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload de solicitud inválido"})
			return
		}

		currency := strings.ToUpper(strings.TrimSpace(req.Currency))
		if currency == "" {
			currency = "USD"
		}

		if req.EventID <= 0 || len(req.Tickets) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debe seleccionar al menos un boleto y un evento válido"})
			return
		}

		// Verify event status and dates (reject if paused, cancelled, finalized, sold out, past event, or future sale date)
		var eventStatus, eventCurrency, eventDate, saleStartDate, saleStartTime string
		errEv := db.QueryRow("SELECT COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(event_date, ''), COALESCE(sale_start_date, ''), COALESCE(sale_start_time, '') FROM events WHERE id = $1", req.EventID).Scan(&eventStatus, &eventCurrency, &eventDate, &saleStartDate, &saleStartTime)
		if errEv != nil {
			if errEv == sql.ErrNoRows {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "el evento especificado no existe"})
				return
			}
			log.Printf("[CHECKOUT ERROR] Error querying event status: %v", errEv)
		} else {
			st := NormalizeEventStatus(eventStatus)
			if IsEventInactiveStatus(st) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("El evento se encuentra %s. No es posible adquirir boletos.", strings.ToLower(st))})
				return
			}
			if st == EventStatusAgotado {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "El evento se encuentra agotado. No es posible adquirir boletos."})
				return
			}
			todayStr := time.Now().Format("2006-01-02")
			if eventDate != "" && eventDate < todayStr {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "El evento ya ha finalizado. No es posible adquirir boletos."})
				return
			}
			if saleStartDate != "" && saleStartDate > todayStr {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("La venta de boletos para este evento inicia el %s a las %s.", saleStartDate, saleStartTime)})
				return
			}
		}

		liveRates := GetLiveRates(rdb)
		eventRate := liveRates[eventCurrency]
		if eventRate <= 0 {
			eventRate = 1.0
		}
		targetRate := liveRates[currency]
		if targetRate <= 0 {
			targetRate = req.ExchangeRate
			if targetRate <= 0 {
				targetRate = 1.0
			}
		}

		// Calculate total cost and verify capacity for each category
		var subtotalNative, subtotalUSD, subtotalTarget float64
		for _, item := range req.Tickets {
			if item.Quantity <= 0 {
				continue
			}

			var name string
			var priceNative float64
			var available int
			err := db.QueryRow(`
				SELECT tc.name, tc.price, GREATEST(0, tc.capacity - COALESCE((
					SELECT SUM(pi.quantity) 
					FROM purchase_items pi 
					JOIN purchases p ON pi.purchase_id = p.id 
					WHERE pi.ticket_category_id = tc.id AND p.status = 'completed'
				), 0)) as available
				FROM ticket_categories tc
				WHERE tc.id = $1 AND tc.event_id = $2
			`, item.ID, req.EventID).Scan(&name, &priceNative, &available)

			if err != nil {
				if err == sql.ErrNoRows {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("la categoría de boleto %d no existe", item.ID)})
					return
				}
				log.Printf("[CHECKOUT ERROR] DB query error: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al consultar categorías de boletos"})
				return
			}

			if available < item.Quantity {
				log.Printf("[CHECKOUT ERROR] Insufficient capacity for category '%s'. Available: %d, Requested: %d", name, available, item.Quantity)
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Entradas agotadas\n La disponibilidad para esta categoría ha concluido. Explora otras localidades o próximos eventos. (Disponibles: %d)", available)})
				return
			}

			itemNative := priceNative * float64(item.Quantity)
			subtotalNative += itemNative

			var itemUSD float64
			if strings.EqualFold(eventCurrency, "USD") {
				itemUSD = itemNative
			} else {
				itemUSD = itemNative / eventRate
			}
			subtotalUSD += itemUSD

			var itemTarget float64
			if strings.EqualFold(currency, eventCurrency) {
				itemTarget = itemNative
			} else if strings.EqualFold(currency, "USD") {
				itemTarget = itemUSD
			} else {
				itemTarget = itemUSD * targetRate
			}
			subtotalTarget += itemTarget
		}

		if subtotalTarget <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "el monto total debe ser mayor a 0"})
			return
		}

		var eventFeePercentage float64
		_ = db.QueryRow(`SELECT COALESCE(service_fee_percentage, 20.00) FROM events WHERE id = $1`, req.EventID).Scan(&eventFeePercentage)
		feeRate := eventFeePercentage / 100.0

		serviceFee := subtotalTarget * feeRate
		totalAmount := subtotalTarget + serviceFee

		// Generate consecutive Order Number RV-XXXXXX
		orderNumber := generateNextOrderNumber(db)

		// Stripe Secret Key setup from environment
		stripeKey := os.Getenv("STRIPE_SECRET_KEY")
		stripe.Key = stripeKey

		var clientSecret string
		var intentID string

		amountInUnits := toStripeAmount(totalAmount, currency)

		params := &stripe.PaymentIntentParams{
			Amount:   stripe.Int64(amountInUnits),
			Currency: stripe.String(strings.ToLower(currency)),
			AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
				Enabled: stripe.Bool(true),
			},
			Description: stripe.String(fmt.Sprintf("Roveni Order %s", orderNumber)),
		}
		if req.CustomerEmail != "" {
			params.ReceiptEmail = stripe.String(req.CustomerEmail)
		}
		params.AddMetadata("order_number", orderNumber)
		params.AddMetadata("event_id", fmt.Sprintf("%d", req.EventID))

		pi, err := paymentintent.New(params)
		if err != nil {
			log.Printf("[STRIPE ERROR] Failed to create PaymentIntent via Stripe API: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("Error al conectar con la pasarela de pago: %v", err),
			})
			return
		}
		log.Printf("[STRIPE SUCCESS] PaymentIntent created successfully! ID: %s", pi.ID)
		intentID = pi.ID
		clientSecret = pi.ClientSecret

		writeJSON(w, http.StatusOK, map[string]any{
			"orderNumber":           orderNumber,
			"clientSecret":          clientSecret,
			"stripePaymentIntentId": intentID,
			"subtotal":              subtotalTarget,
			"serviceFee":            serviceFee,
			"totalAmount":           totalAmount,
			"currency":              currency,
		})
	}
}

// ConfirmCheckoutHandler processes atomic ticket capacity deduction, stores purchase details, generates PDF tickets with signed QRs, and dispatches confirmation email.
func ConfirmCheckoutHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req ConfirmCheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[CONFIRM ERROR] Failed to decode json request body: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload de confirmación inválido"})
			return
		}

		currency := strings.ToUpper(strings.TrimSpace(req.Currency))
		if currency == "" {
			currency = "USD"
		}

		if req.OrderNumber == "" || req.EventID <= 0 || req.CustomerEmail == "" || len(req.Tickets) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "datos de compra incompletos"})
			return
		}

		// Verify event status and dates (reject if paused, cancelled, finalized, sold out, or past event)
		var confirmEventStatus, confirmEventDate, eventCurrency string
		errEvConfirm := db.QueryRow("SELECT COALESCE(status, 'Publicado'), COALESCE(event_date, ''), COALESCE(currency, 'USD') FROM events WHERE id = $1", req.EventID).Scan(&confirmEventStatus, &confirmEventDate, &eventCurrency)
		if errEvConfirm == nil {
			st := NormalizeEventStatus(confirmEventStatus)
			if IsEventInactiveStatus(st) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("El evento se encuentra %s. No es posible completar la compra.", strings.ToLower(st))})
				return
			}
			if st == EventStatusAgotado {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "El evento se encuentra agotado. No es posible completar la compra."})
				return
			}
			todayStr := time.Now().Format("2006-01-02")
			if confirmEventDate != "" && confirmEventDate < todayStr {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "El evento ya ha finalizado. No es posible completar la compra."})
				return
			}
		}

		liveRates := GetLiveRates(rdb)
		eventRate := liveRates[eventCurrency]
		if eventRate <= 0 {
			eventRate = 1.0
		}
		targetRate := liveRates[currency]
		if targetRate <= 0 {
			targetRate = req.ExchangeRate
			if targetRate <= 0 {
				targetRate = 1.0
			}
		}

		// -------------------------------------------------------------------------
		// SECURITY CHECK 1: Require Valid Non-Empty Stripe PaymentIntent ID
		// -------------------------------------------------------------------------
		if req.StripePaymentIntentID == "" || !strings.HasPrefix(req.StripePaymentIntentID, "pi_") {
			log.Printf("[SECURITY VIOLATION] Order %s attempt without valid Stripe PaymentIntent ID!", req.OrderNumber)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "Operación denegada. Se requiere un identificador de pago verificado por Stripe.",
			})
			return
		}

		// -------------------------------------------------------------------------
		// SECURITY CHECK 2: Anti-Replay / Double Claim Protection
		// -------------------------------------------------------------------------
		var existingCount int
		errDup := db.QueryRow("SELECT COUNT(*) FROM purchases WHERE stripe_payment_intent_id = $1", req.StripePaymentIntentID).Scan(&existingCount)
		if errDup == nil && existingCount > 0 {
			log.Printf("[SECURITY VIOLATION] Replay attack detected for PaymentIntent %s!", req.StripePaymentIntentID)
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Esta transacción de Stripe ya ha sido procesada previamente.",
			})
			return
		}

		// -------------------------------------------------------------------------
		// SECURITY CHECK 3: Real-Time Direct Verification with Stripe API
		// -------------------------------------------------------------------------
		stripeKey := os.Getenv("STRIPE_SECRET_KEY")
		stripe.Key = stripeKey

		pi, errPI := paymentintent.Get(req.StripePaymentIntentID, nil)
		if errPI != nil || pi == nil {
			log.Printf("[SECURITY ERROR] Failed to retrieve PaymentIntent '%s' from Stripe: %v", req.StripePaymentIntentID, errPI)
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "No se pudo verificar la transacción con la entidad bancaria a través de Stripe.",
			})
			return
		}

		if pi.Status != stripe.PaymentIntentStatusSucceeded {
			log.Printf("[SECURITY REJECTION] PaymentIntent %s has unverified status: '%s'", pi.ID, pi.Status)
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("El pago no ha sido completado por la entidad bancaria. Estado actual: %s", pi.Status),
			})
			return
		}

		log.Printf("[SECURITY SUCCESS] Verified Stripe PaymentIntent %s status 'succeeded' with amount %d %s!", pi.ID, pi.AmountReceived, pi.Currency)

		// Query Event details for email and confirmation summary
		var eventName, eventDate, doorsOpenTime, showTime, venueAddress, city, country string
		err := db.QueryRow(`
			SELECT name, event_date, doors_open_time, show_start_time, venue_address, city, country 
			FROM events 
			WHERE id = $1
		`, req.EventID).Scan(&eventName, &eventDate, &doorsOpenTime, &showTime, &venueAddress, &city, &country)

		if err != nil {
			log.Printf("[CONFIRM ERROR] Querying event %d failed: %v", req.EventID, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "el evento especificado no existe"})
			return
		}

		// Begin SQL Transaction to atomically deduct ticket capacity and record order
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			log.Printf("[CONFIRM ERROR] Failed to begin DB transaction: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al iniciar transacción de compra"})
			return
		}
		defer tx.Rollback()

		var totalSubtotalTarget, totalSubtotalUSD float64
		var emailTickets []mailer.TicketItem

		type TicketItemDetail struct {
			CatID       int64
			Name        string
			Quantity    int
			PriceTarget float64
			PriceUSD    float64
			PriceNative float64
		}
		var ticketDetails []TicketItemDetail

		for _, item := range req.Tickets {
			if item.Quantity <= 0 {
				continue
			}

			// Atomic availability check (without mutating total category capacity)
			var available int
			err := tx.QueryRow(`
				SELECT GREATEST(0, tc.capacity - COALESCE((
					SELECT SUM(pi.quantity) 
					FROM purchase_items pi 
					JOIN purchases p ON pi.purchase_id = p.id 
					WHERE pi.ticket_category_id = tc.id AND p.status = 'completed'
				), 0))
				FROM ticket_categories tc
				WHERE tc.id = $1 AND tc.event_id = $2 FOR UPDATE
			`, item.ID, req.EventID).Scan(&available)

			if err != nil {
				log.Printf("[CONFIRM ERROR] Failed DB capacity check for category %d: %v", item.ID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al verificar boletos en la base de datos"})
				return
			}

			if available < item.Quantity {
				log.Printf("[CONFIRM ERROR] Category %d capacity exceeded! Available: %d, Requested: %d", item.ID, available, item.Quantity)
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No hay suficientes boletos disponibles. La capacidad ha sido sobrepasada."})
				return
			}

			var catName string
			var priceNative float64
			err = tx.QueryRow(`SELECT name, price FROM ticket_categories WHERE id = $1`, item.ID).Scan(&catName, &priceNative)
			if err != nil {
				log.Printf("[CONFIRM ERROR] Querying category details for ID %d failed: %v", item.ID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al obtener precio de categoría"})
				return
			}

			var priceUSD float64
			if strings.EqualFold(eventCurrency, "USD") {
				priceUSD = priceNative
			} else {
				priceUSD = priceNative / eventRate
			}

			var priceTarget float64
			if strings.EqualFold(currency, eventCurrency) {
				priceTarget = priceNative
			} else if strings.EqualFold(currency, "USD") {
				priceTarget = priceUSD
			} else {
				priceTarget = priceUSD * targetRate
			}

			totalSubtotalTarget += priceTarget * float64(item.Quantity)
			totalSubtotalUSD += priceUSD * float64(item.Quantity)

			emailTickets = append(emailTickets, mailer.TicketItem{
				Name:     catName,
				Quantity: item.Quantity,
				Price:    priceTarget,
			})

			ticketDetails = append(ticketDetails, TicketItemDetail{
				CatID:       item.ID,
				Name:        catName,
				Quantity:    item.Quantity,
				PriceTarget: priceTarget,
				PriceUSD:    priceUSD,
				PriceNative: priceNative,
			})
		}

		var eventFeePercentage float64
		_ = db.QueryRow(`SELECT COALESCE(service_fee_percentage, 20.00) FROM events WHERE id = $1`, req.EventID).Scan(&eventFeePercentage)
		feeRate := eventFeePercentage / 100.0

		serviceFeeTarget := totalSubtotalTarget * feeRate
		totalAmountTarget := totalSubtotalTarget + serviceFeeTarget

		serviceFeeUSD := totalSubtotalUSD * feeRate
		totalAmountUSD := totalSubtotalUSD + serviceFeeUSD

		effExchangeRate := targetRate / eventRate

		// Insert into purchases table
		var purchaseID int64
		err = tx.QueryRow(`
			INSERT INTO purchases (
				order_number, event_id, customer_name, customer_email, customer_phone,
				subtotal, service_fee, total_amount,
				subtotal_usd, service_fee_usd, total_amount_usd,
				currency, event_currency, exchange_rate, stripe_payment_intent_id, status
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'completed')
			RETURNING id
		`, req.OrderNumber, req.EventID, req.CustomerName, req.CustomerEmail, req.CustomerPhone,
			totalSubtotalTarget, serviceFeeTarget, totalAmountTarget,
			totalSubtotalUSD, serviceFeeUSD, totalAmountUSD,
			currency, eventCurrency, effExchangeRate, req.StripePaymentIntentID).Scan(&purchaseID)

		if err != nil {
			log.Printf("[CONFIRM ERROR] Inserting into purchases table failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("error al registrar compra en base de datos: %v", err)})
			return
		}

		// Insert into purchase_items table
		for _, t := range ticketDetails {
			_, err = tx.Exec(`
				INSERT INTO purchase_items (purchase_id, ticket_category_id, ticket_name, price, price_usd, price_native, quantity)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
			`, purchaseID, t.CatID, t.Name, t.PriceTarget, t.PriceUSD, t.PriceNative, t.Quantity)
			if err != nil {
				log.Printf("[CONFIRM ERROR] Inserting into purchase_items table failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al guardar detalles de boletos"})
				return
			}
		}

		// Commit SQL Transaction
		if err := tx.Commit(); err != nil {
			log.Printf("[CONFIRM ERROR] Failed to commit DB transaction: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al confirmar transacción"})
			return
		}

		log.Printf("[CONFIRM SUCCESS] Order %s (ID %d) saved to DB! Ticket capacity deducted successfully.", req.OrderNumber, purchaseID)

		// Generate 1 PDF ticket per unit purchased
		var pdfTicketList []tickets.TicketPDFData
		ticketSerialIndex := 1
		for _, t := range emailTickets {
			for q := 0; q < t.Quantity; q++ {
				pdfTicketList = append(pdfTicketList, tickets.TicketPDFData{
					OrderNumber:  req.OrderNumber,
					TicketSerial: fmt.Sprintf("%s-%02d", req.OrderNumber, ticketSerialIndex),
					CategoryName: t.Name,
					Price:        t.Price,
					Currency:     currency,
				})
				ticketSerialIndex++
			}
		}

		pdfPaths, pdfErr := tickets.GenerateAllTicketsForOrder(
			req.OrderNumber, req.CustomerName, req.CustomerEmail,
			eventName, venueAddress, city, country,
			eventDate, doorsOpenTime, showTime, pdfTicketList,
		)
		if pdfErr != nil {
			log.Printf("[PDF TICKET ERROR] Failed generating tickets for order %s: %v", req.OrderNumber, pdfErr)
		} else {
			log.Printf("[PDF TICKET SUCCESS] Successfully generated %d individual PDF tickets for order %s!", len(pdfPaths), req.OrderNumber)
		}

		// Asynchronously dispatch purchase confirmation email with PDF attachments
		go func() {
			emailData := mailer.EmailData{
				To:             req.CustomerEmail,
				CustomerName:   req.CustomerName,
				OrderNumber:    req.OrderNumber,
				EventID:        req.EventID,
				EventName:      eventName,
				EventDate:      eventDate,
				ShowTime:       showTime,
				VenueAddress:   venueAddress,
				City:           city,
				Tickets:        emailTickets,
				Subtotal:       totalSubtotalTarget,
				ServiceFee:     serviceFeeTarget,
				TotalAmount:    totalAmountTarget,
				Currency:       currency,
				PdfAttachments: pdfPaths,
			}
			if err := mailer.SendPurchaseConfirmationEmail(emailData); err != nil {
				log.Printf("[MAILER ERROR] Failed to send email for Order %s: %v", req.OrderNumber, err)
			}
		}()

		log.Printf("==================================================")

		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"orderNumber": req.OrderNumber,
			"ticketCount": len(pdfPaths),
			"message":     "Compra procesada, boletos PDF generados y correo de confirmación enviado con éxito",
		})
	}
}

// DownloadTicketsPDFHandler streams the generated PDF ticket(s) for a given order_number.
func DownloadTicketsPDFHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderNumber := strings.TrimSpace(r.URL.Query().Get("order_number"))
		if orderNumber == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "El parámetro order_number es requerido"})
			return
		}

		// Query purchase and event details
		var eventID int64
		var customerName, customerEmail, currency string
		err := db.QueryRow(`
			SELECT event_id, customer_name, customer_email, currency 
			FROM purchases 
			WHERE order_number = $1
		`, orderNumber).Scan(&eventID, &customerName, &customerEmail, &currency)

		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Orden no encontrada en la base de datos"})
			return
		}

		var eventName, eventDate, doorsOpenTime, showStartTime, venueAddress, city, country string
		err = db.QueryRow(`
			SELECT name, event_date, doors_open_time, show_start_time, venue_address, city, country 
			FROM events 
			WHERE id = $1
		`, eventID).Scan(&eventName, &eventDate, &doorsOpenTime, &showStartTime, &venueAddress, &city, &country)

		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Evento no encontrado"})
			return
		}

		rows, err := db.Query(`
			SELECT ticket_name, price, quantity 
			FROM purchase_items 
			WHERE purchase_id = (SELECT id FROM purchases WHERE order_number = $1)
		`, orderNumber)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al consultar boletos de la orden"})
			return
		}
		defer rows.Close()

		var pdfTicketList []tickets.TicketPDFData
		ticketSerialIndex := 1
		for rows.Next() {
			var catName string
			var price float64
			var qty int
			if err := rows.Scan(&catName, &price, &qty); err == nil {
				for q := 0; q < qty; q++ {
					pdfTicketList = append(pdfTicketList, tickets.TicketPDFData{
						OrderNumber:  orderNumber,
						TicketSerial: fmt.Sprintf("%s-%02d", orderNumber, ticketSerialIndex),
						CategoryName: catName,
						Price:        price,
						Currency:     currency,
					})
					ticketSerialIndex++
				}
			}
		}

		if len(pdfTicketList) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "No se encontraron boletos para esta orden"})
			return
		}

		pdfPaths, err := tickets.GenerateAllTicketsForOrder(
			orderNumber, customerName, customerEmail,
			eventName, venueAddress, city, country,
			eventDate, doorsOpenTime, showStartTime, pdfTicketList,
		)

		if err != nil || len(pdfPaths) == 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Error al generar archivo PDF de boletos"})
			return
		}

		// Stream the first generated ticket PDF file
		pdfBytes, err := os.ReadFile(pdfPaths[0])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Error al leer archivo PDF"})
			return
		}

		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"Boleto_Roveni_%s.pdf\"", orderNumber))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pdfBytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfBytes)
	}
}

// DownloadTicketsZipHandler genera y descarga un empaquetado .ZIP con todos los PDFs de boletos individuales de una orden
func DownloadTicketsZipHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		orderNumber := r.URL.Query().Get("order_number")
		if orderNumber == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing order_number"})
			return
		}

		var purchaseID int64
		var eventID int64
		var customerName string
		var customerEmail string
		var currency string

		err := db.QueryRow(`
			SELECT id, event_id, customer_name, customer_email, currency 
			FROM purchases 
			WHERE order_number = $1 AND status = 'completed'
		`, orderNumber).Scan(&purchaseID, &eventID, &customerName, &customerEmail, &currency)

		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "orden no encontrada"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error en base de datos"})
			return
		}

		var eventName, venueAddress, city, country, eventDate, doorsOpenTime, showStartTime string
		err = db.QueryRow(`
			SELECT name, venue_address, city, country, event_date, doors_open_time, show_start_time 
			FROM events 
			WHERE id = $1
		`, eventID).Scan(&eventName, &venueAddress, &city, &country, &eventDate, &doorsOpenTime, &showStartTime)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al obtener evento"})
			return
		}

		rows, err := db.Query(`
			SELECT ticket_name, price, quantity 
			FROM purchase_items 
			WHERE purchase_id = $1
		`, purchaseID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al obtener ítems de orden"})
			return
		}
		defer rows.Close()

		var pdfTicketList []tickets.TicketPDFData
		ticketSerialIndex := 1
		for rows.Next() {
			var catName string
			var price float64
			var qty int
			if err := rows.Scan(&catName, &price, &qty); err == nil {
				for q := 0; q < qty; q++ {
					pdfTicketList = append(pdfTicketList, tickets.TicketPDFData{
						OrderNumber:  orderNumber,
						TicketSerial: fmt.Sprintf("%s-%02d", orderNumber, ticketSerialIndex),
						CategoryName: catName,
						Price:        price,
						Currency:     currency,
					})
					ticketSerialIndex++
				}
			}
		}

		if len(pdfTicketList) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no se encontraron boletos para esta orden"})
			return
		}

		pdfPaths, err := tickets.GenerateAllTicketsForOrder(
			orderNumber, customerName, customerEmail,
			eventName, venueAddress, city, country,
			eventDate, doorsOpenTime, showStartTime, pdfTicketList,
		)

		if err != nil || len(pdfPaths) == 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al generar archivos PDF"})
			return
		}

		// Pack into ZIP buffer
		buf := new(bytes.Buffer)
		zipWriter := zip.NewWriter(buf)

		for i, pdfPath := range pdfPaths {
			fileBytes, readErr := os.ReadFile(pdfPath)
			if readErr != nil {
				continue
			}
			filename := fmt.Sprintf("Boleto_%d_%s.pdf", i+1, orderNumber)
			f, createErr := zipWriter.Create(filename)
			if createErr != nil {
				continue
			}
			_, _ = f.Write(fileBytes)
		}
		_ = zipWriter.Close()

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"Boletos_Roveni_%s.zip\"", orderNumber))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}
}

type CreateSessionRequest struct {
	EventID       int64              `json:"event_id"`
	CustomerName  string             `json:"customer_name"`
	CustomerEmail string             `json:"customer_email"`
	CustomerPhone string             `json:"customer_phone"`
	Currency      string             `json:"currency"`
	ExchangeRate  float64            `json:"exchange_rate"`
	Tickets       []TicketOrderInput `json:"tickets"`
	SuccessURL    string             `json:"success_url"`
	CancelURL     string             `json:"cancel_url"`
}

type ConfirmSessionRequest struct {
	SessionID     string             `json:"session_id"`
	OrderNumber   string             `json:"order_number"`
	EventID       int64              `json:"event_id"`
	CustomerName  string             `json:"customer_name"`
	CustomerEmail string             `json:"customer_email"`
	CustomerPhone string             `json:"customer_phone"`
	Currency      string             `json:"currency"`
	ExchangeRate  float64            `json:"exchange_rate"`
	Tickets       []TicketOrderInput `json:"tickets"`
}

func CreateCheckoutSessionHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload de solicitud inválido"})
			return
		}
		currency := strings.ToUpper(strings.TrimSpace(req.Currency))
		if currency == "" {
			currency = "USD"
		}
		rate := req.ExchangeRate
		if rate <= 0 {
			rate = 1.0
		}

		var lineItems []*stripe.CheckoutSessionLineItemParams
		var subtotalConverted float64

		for _, item := range req.Tickets {
			if item.Quantity <= 0 {
				continue
			}
			var catName string
			var priceUSD float64
			err := db.QueryRow(`SELECT name, price FROM ticket_categories WHERE id = $1`, item.ID).Scan(&catName, &priceUSD)
			if err == nil {
				priceConverted := priceUSD * rate
				subtotalConverted += priceConverted * float64(item.Quantity)
				lineItems = append(lineItems, &stripe.CheckoutSessionLineItemParams{
					PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
						Currency:   stripe.String(strings.ToLower(currency)),
						UnitAmount: stripe.Int64(toStripeAmount(priceConverted, currency)),
						ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
							Name: stripe.String(catName),
						},
					},
					Quantity: stripe.Int64(int64(item.Quantity)),
				})
			}
		}

		// Add Service Fee line item based on event's configured percentage
		if subtotalConverted > 0 {
			var eventFeePercentage float64
			_ = db.QueryRow(`SELECT COALESCE(service_fee_percentage, 20.00) FROM events WHERE id = $1`, req.EventID).Scan(&eventFeePercentage)
			feeRate := eventFeePercentage / 100.0

			serviceFeeConverted := subtotalConverted * feeRate
			feeLabel := fmt.Sprintf("Cargo por Servicio Roveni (%.0f%%)", eventFeePercentage)
			if eventFeePercentage != float64(int(eventFeePercentage)) {
				feeLabel = fmt.Sprintf("Cargo por Servicio Roveni (%.2f%%)", eventFeePercentage)
			}
			lineItems = append(lineItems, &stripe.CheckoutSessionLineItemParams{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripe.String(strings.ToLower(currency)),
					UnitAmount: stripe.Int64(toStripeAmount(serviceFeeConverted, currency)),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name:        stripe.String(feeLabel),
						Description: stripe.String("Cargos de procesamiento de plataforma y emisión de boletos"),
					},
				},
				Quantity: stripe.Int64(1),
			})
		}
		stripeKey := os.Getenv("STRIPE_SECRET_KEY")

		stripe.Key = stripeKey
		orderNumber := generateNextOrderNumber(db)
		successURL := strings.ReplaceAll(req.SuccessURL, "{ORDER_NUMBER}", orderNumber)
		params := &stripe.CheckoutSessionParams{
			LineItems:  lineItems,
			Mode:       stripe.String(string(stripe.CheckoutSessionModePayment)),
			SuccessURL: stripe.String(successURL),
			CancelURL:  stripe.String(req.CancelURL),
		}
		if req.CustomerEmail != "" {
			params.CustomerEmail = stripe.String(req.CustomerEmail)
		}
		params.AddMetadata("order_number", orderNumber)
		params.AddMetadata("event_id", fmt.Sprintf("%d", req.EventID))
		s, err := session.New(params)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": s.URL, "orderNumber": orderNumber})
	}
}

func ConfirmSessionHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return ConfirmCheckoutHandler(db, rdb)
}
