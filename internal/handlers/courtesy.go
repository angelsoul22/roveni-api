package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"roveni/internal/mailer"
	"roveni/internal/tickets"

	"github.com/redis/go-redis/v9"
)

type CourtesyItemInput struct {
	TicketCategoryID int64   `json:"ticket_category_id"`
	Quantity         int     `json:"quantity"`
	SeatIDs          []int64 `json:"seat_ids"`
}

type IssueCourtesyRequest struct {
	EventID          int64               `json:"event_id"`
	RecipientName    string              `json:"recipient_name"`
	RecipientEmail   string              `json:"recipient_email"`
	RecipientPhone   string              `json:"recipient_phone"`
	CourtesyNote     string              `json:"courtesy_note"`
	TicketCategoryID int64               `json:"ticket_category_id"`
	Quantity         int                 `json:"quantity"`
	Items            []CourtesyItemInput `json:"items"`
}

type CourtesyPassSummary struct {
	ID            int64     `json:"id"`
	OrderNumber   string    `json:"order_number"`
	CustomerName  string    `json:"customer_name"`
	CustomerEmail string    `json:"customer_email"`
	CustomerPhone string    `json:"customer_phone"`
	CourtesyNote  string    `json:"courtesy_note"`
	TotalTickets  int       `json:"total_tickets"`
	Details       string    `json:"details"`
	CreatedAt     time.Time `json:"created_at"`
}

type CourtesyListResponse struct {
	EventID        int64                 `json:"event_id"`
	EventName      string                `json:"event_name"`
	CourtesyQuota  int                   `json:"courtesy_quota"`
	IssuedCount    int                   `json:"issued_count"`
	RemainingCount int                   `json:"remaining_count"`
	Courtesies     []CourtesyPassSummary `json:"courtesies"`
}

// IssueCourtesyTicketHandler handles generating free courtesy ticket passes and emailing them
func IssueCourtesyTicketHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		var req IssueCourtesyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Fallback to form parsing if JSON decode fails
			_ = r.ParseForm()
			eventID, _ := strconv.ParseInt(r.FormValue("event_id"), 10, 64)
			ticketCatID, _ := strconv.ParseInt(r.FormValue("ticket_category_id"), 10, 64)
			qty, _ := strconv.Atoi(r.FormValue("quantity"))

			req = IssueCourtesyRequest{
				EventID:          eventID,
				RecipientName:    r.FormValue("recipient_name"),
				RecipientEmail:   r.FormValue("recipient_email"),
				RecipientPhone:   r.FormValue("recipient_phone"),
				CourtesyNote:     r.FormValue("courtesy_note"),
				TicketCategoryID: ticketCatID,
				Quantity:         qty,
			}
		}

		req.RecipientName = strings.TrimSpace(req.RecipientName)
		req.RecipientEmail = strings.TrimSpace(req.RecipientEmail)
		req.CourtesyNote = strings.TrimSpace(req.CourtesyNote)

		if req.EventID <= 0 || req.RecipientName == "" || req.RecipientEmail == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Nombre, correo y ID de evento son requeridos"})
			return
		}

		// Normalize items
		if len(req.Items) == 0 {
			if req.Quantity <= 0 {
				req.Quantity = 1
			}
			req.Items = []CourtesyItemInput{
				{
					TicketCategoryID: req.TicketCategoryID,
					Quantity:         req.Quantity,
				},
			}
		}

		// Fetch Event metadata and check permissions
		var organizerID int64
		var eventName, imageURL, venueAddress, city, country, eventDate, doorsOpenTime, showTime, currency string
		var courtesyQuota int

		err = db.QueryRow(`
			SELECT organizer_id, name, COALESCE(image_url, ''), venue_address, city, country, event_date, doors_open_time, show_start_time, COALESCE(currency, 'USD'), COALESCE(courtesy_quota, 0)
			FROM events WHERE id = $1
		`, req.EventID).Scan(
			&organizerID, &eventName, &imageURL, &venueAddress, &city, &country, &eventDate, &doorsOpenTime, &showTime, &currency, &courtesyQuota,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al consultar evento"})
			return
		}

		if user.Role != "administrador" && organizerID != user.ID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "no tienes permiso para generar cortesías en este evento"})
			return
		}

		// Calculate total requested quantity across items
		totalRequestedQty := 0
		for _, item := range req.Items {
			if len(item.SeatIDs) > 0 {
				totalRequestedQty += len(item.SeatIDs)
			} else if item.Quantity > 0 {
				totalRequestedQty += item.Quantity
			}
		}

		if totalRequestedQty <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debes especificar al menos 1 boleto o asiento de cortesía"})
			return
		}

		// Check current issued courtesy passes count
		var currentIssued int
		_ = db.QueryRow(`
			SELECT COALESCE(SUM(pi.quantity), 0)
			FROM purchase_items pi
			JOIN purchases p ON pi.purchase_id = p.id
			WHERE p.event_id = $1 AND p.is_courtesy = true AND p.status = 'completed'
		`, req.EventID).Scan(&currentIssued)

		if courtesyQuota > 0 && (currentIssued+totalRequestedQty) > courtesyQuota {
			availableQuota := courtesyQuota - currentIssued
			if availableQuota < 0 {
				availableQuota = 0
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("Cuota de cortesías superada. Cuota total: %d, Ya emitidas: %d, Disponibles: %d, Solicitadas: %d", courtesyQuota, currentIssued, availableQuota, totalRequestedQty),
			})
			return
		}

		// Begin SQL Transaction
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error de transacción en base de datos"})
			return
		}
		defer tx.Rollback()

		orderNumber := generateNextOrderNumber(db)

		var purchaseID int64
		err = tx.QueryRow(`
			INSERT INTO purchases (
				order_number, event_id, customer_name, customer_email, customer_phone,
				subtotal, service_fee, total_amount, subtotal_usd, service_fee_usd, total_amount_usd,
				currency, event_currency, exchange_rate, stripe_payment_intent_id, status,
				is_courtesy, courtesy_note
			) VALUES (
				$1, $2, $3, $4, $5,
				0.00, 0.00, 0.00, 0.00, 0.00, 0.00,
				$6, $6, 1.0, 'COURTESY_PASS', 'completed',
				true, $7
			) RETURNING id
		`, orderNumber, req.EventID, req.RecipientName, req.RecipientEmail, req.RecipientPhone, currency, req.CourtesyNote).Scan(&purchaseID)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("error al guardar cortesía: %v", err)})
			return
		}

		// Process each item (Seated vs General Admission)
		for _, item := range req.Items {
			if len(item.SeatIDs) > 0 {
				// Seated tickets
				for _, sID := range item.SeatIDs {
					var rowLabel, block, catName string
					var seatNum int
					var catID sql.NullInt64

					var seatStatus string
					_ = db.QueryRow("SELECT COALESCE(status, 'available') FROM event_seats WHERE event_id = $1 AND seat_id = $2", req.EventID, sID).Scan(&seatStatus)
					if seatStatus == "sold" {
						writeJSON(w, http.StatusBadRequest, map[string]string{
							"error": fmt.Sprintf("El asiento ID %d ya se encuentra vendido o asignado", sID),
						})
						return
					}

					errSeat := db.QueryRow(`
						SELECT s.row_label, s.seat_number, s.block, es.ticket_category_id, COALESCE(tc.name, '')
						FROM seats s
						LEFT JOIN event_seats es ON es.seat_id = s.id AND es.event_id = $1
						LEFT JOIN ticket_categories tc ON tc.id = es.ticket_category_id
						WHERE s.id = $2
					`, req.EventID, sID).Scan(&rowLabel, &seatNum, &block, &catID, &catName)

					if errSeat != nil {
						log.Printf("[COURTESY SEAT ERROR] Failed to query seat ID %d for event %d: %v", sID, req.EventID, errSeat)
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("asiento ID %d no encontrado", sID)})
						return
					}

					if !catID.Valid && item.TicketCategoryID > 0 {
						catID.Valid = true
						catID.Int64 = item.TicketCategoryID
						_ = db.QueryRow("SELECT name FROM ticket_categories WHERE id = $1", item.TicketCategoryID).Scan(&catName)
					}
					if catName == "" {
						catName = "Asiento Reservado"
					}

					seatLabel := fmt.Sprintf("Fila %s - Asiento %d (%s)", rowLabel, seatNum, block)

					var cid *int64
					if catID.Valid {
						v := catID.Int64
						cid = &v
					}

					_, errItem := tx.Exec(`
						INSERT INTO purchase_items (purchase_id, ticket_category_id, ticket_name, price, price_usd, price_native, quantity, seat_id, seat_label)
						VALUES ($1, $2, $3, 0.00, 0.00, 0.00, 1, $4, $5)
					`, purchaseID, cid, catName, sID, seatLabel)

					if errItem != nil {
						writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al guardar detalles de asiento de cortesía"})
						return
					}

					// Update event_seats to sold
					_, _ = tx.Exec(`
						INSERT INTO event_seats (event_id, seat_id, ticket_category_id, status, purchase_id)
						VALUES ($1, $2, $3, 'sold', $4)
						ON CONFLICT (event_id, seat_id) DO UPDATE
						SET status = 'sold', purchase_id = EXCLUDED.purchase_id, locked_until = NULL
					`, req.EventID, sID, cid, purchaseID)
				}
			} else {
				// General Admission ticket
				var catName string
				if item.TicketCategoryID > 0 {
					_ = db.QueryRow("SELECT name FROM ticket_categories WHERE id = $1 AND event_id = $2", item.TicketCategoryID, req.EventID).Scan(&catName)
				}
				if catName == "" {
					catName = "Entrada General Cortesía"
				}

				qty := item.Quantity
				if qty <= 0 {
					qty = 1
				}

				var cid *int64
				if item.TicketCategoryID > 0 {
					v := item.TicketCategoryID
					cid = &v
				}

				_, errItem := tx.Exec(`
					INSERT INTO purchase_items (purchase_id, ticket_category_id, ticket_name, price, price_usd, price_native, quantity, seat_id, seat_label)
					VALUES ($1, $2, $3, 0.00, 0.00, 0.00, $4, NULL, '')
				`, purchaseID, cid, catName, qty)

				if errItem != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al guardar detalle de cortesía"})
					return
				}
			}
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al confirmar cortesía"})
			return
		}

		// Generate PDF tickets and email recipient
		var pdfTicketList []tickets.TicketPDFData
		var emailTickets []mailer.TicketItem

		itemRows, errItems := db.Query(`
			SELECT ticket_name, price, quantity, COALESCE(seat_label, '')
			FROM purchase_items
			WHERE purchase_id = $1
		`, purchaseID)

		if errItems == nil {
			ticketSerialIndex := 1
			for itemRows.Next() {
				var tName, sLabel string
				var price float64
				var qty int
				if errScan := itemRows.Scan(&tName, &price, &qty, &sLabel); errScan == nil {
					emailTickets = append(emailTickets, mailer.TicketItem{
						Name:     tName,
						Quantity: qty,
						Price:    0.00,
					})
					for q := 0; q < qty; q++ {
						pdfTicketList = append(pdfTicketList, tickets.TicketPDFData{
							OrderNumber:   orderNumber,
							TicketSerial:  fmt.Sprintf("%s-%02d", orderNumber, ticketSerialIndex),
							CategoryName:  tName + " (CORTESÍA)",
							CustomerName:  req.RecipientName,
							CustomerEmail: req.RecipientEmail,
							Price:         0.00,
							Currency:      currency,
							SeatLabel:     sLabel,
							IsCourtesy:    true,
						})
						ticketSerialIndex++
					}
				}
			}
			itemRows.Close()
		}

		pdfPaths, pdfErr := tickets.GenerateAllTicketsForOrder(
			orderNumber, req.RecipientName, req.RecipientEmail,
			eventName, imageURL, venueAddress, city, country,
			eventDate, doorsOpenTime, showTime, pdfTicketList,
		)
		if pdfErr != nil {
			log.Printf("[COURTESY PDF ERROR] Failed generating tickets for order %s: %v", orderNumber, pdfErr)
		} else {
			log.Printf("[COURTESY PDF SUCCESS] Generated %d PDF tickets for order %s", len(pdfPaths), orderNumber)
		}

		// Async email dispatch
		go func() {
			emailData := mailer.EmailData{
				To:             req.RecipientEmail,
				CustomerName:   req.RecipientName,
				OrderNumber:    orderNumber,
				EventID:        req.EventID,
				EventName:      eventName,
				EventDate:      eventDate,
				ShowTime:       showTime,
				VenueAddress:   venueAddress,
				City:           city,
				Tickets:        emailTickets,
				Subtotal:       0.00,
				ServiceFee:     0.00,
				TotalAmount:    0.00,
				Currency:       currency,
				PdfAttachments: pdfPaths,
			}
			if errMail := mailer.SendPurchaseConfirmationEmail(emailData); errMail != nil {
				log.Printf("[COURTESY MAIL ERROR] Failed to send email to %s: %v", req.RecipientEmail, errMail)
			} else {
				log.Printf("[COURTESY MAIL SUCCESS] Courtesy email sent to %s", req.RecipientEmail)
			}
		}()

		writeJSON(w, http.StatusOK, map[string]any{
			"success":      true,
			"message":      "Cortesía emitida y enviada por correo exitosamente",
			"order_number": orderNumber,
			"purchase_id":  purchaseID,
		})
	}
}

// GetCourtesyListHandler lists all issued courtesy passes for an event with quota stats
func GetCourtesyListHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		eventIDStr := r.URL.Query().Get("event_id")
		if eventIDStr == "" {
			eventIDStr = r.URL.Query().Get("id")
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil || eventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID de evento requerido"})
			return
		}

		var organizerID int64
		var eventName string
		var courtesyQuota int

		err = db.QueryRow("SELECT organizer_id, name, COALESCE(courtesy_quota, 0) FROM events WHERE id = $1", eventID).Scan(&organizerID, &eventName, &courtesyQuota)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		if user.Role != "administrador" && organizerID != user.ID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "sin autorización para ver cortesías de este evento"})
			return
		}

		rows, err := db.Query(`
			SELECT 
				p.id, p.order_number, p.customer_name, p.customer_email, COALESCE(p.customer_phone, ''), 
				COALESCE(p.courtesy_note, ''), p.created_at,
				COALESCE(SUM(pi.quantity), 0) as total_tickets,
				COALESCE(STRING_AGG(CONCAT(pi.ticket_name, CASE WHEN pi.seat_label != '' THEN CONCAT(' (', pi.seat_label, ')') ELSE '' END), ', '), '') as details
			FROM purchases p
			JOIN purchase_items pi ON pi.purchase_id = p.id
			WHERE p.event_id = $1 AND p.is_courtesy = true AND p.status = 'completed'
			GROUP BY p.id
			ORDER BY p.created_at DESC
		`, eventID)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al consultar cortesías"})
			return
		}
		defer rows.Close()

		var courtesies []CourtesyPassSummary
		totalIssued := 0

		for rows.Next() {
			var c CourtesyPassSummary
			if errScan := rows.Scan(&c.ID, &c.OrderNumber, &c.CustomerName, &c.CustomerEmail, &c.CustomerPhone, &c.CourtesyNote, &c.CreatedAt, &c.TotalTickets, &c.Details); errScan == nil {
				courtesies = append(courtesies, c)
				totalIssued += c.TotalTickets
			}
		}

		remaining := courtesyQuota - totalIssued
		if remaining < 0 {
			remaining = 0
		}

		writeJSON(w, http.StatusOK, CourtesyListResponse{
			EventID:        eventID,
			EventName:      eventName,
			CourtesyQuota:  courtesyQuota,
			IssuedCount:    totalIssued,
			RemainingCount: remaining,
			Courtesies:     courtesies,
		})
	}
}
