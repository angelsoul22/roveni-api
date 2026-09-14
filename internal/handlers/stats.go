package handlers

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type TicketCategoryStat struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	PriceUSD         float64 `json:"price_usd"`
	Capacity         int     `json:"capacity"`
	TicketsSold      int     `json:"tickets_sold"`
	TicketsRemaining int     `json:"tickets_remaining"`
	NetRevenueUSD    float64 `json:"net_revenue_usd"`
	ServiceFeeUSD    float64 `json:"service_fee_usd"`
	GrossRevenueUSD  float64 `json:"gross_revenue_usd"`
	PercentageSold   float64 `json:"percentage_sold"`
}

type AttendeeRecord struct {
	OrderID        int64     `json:"order_id"`
	OrderNumber    string    `json:"order_number"`
	CustomerName   string    `json:"customer_name"`
	CustomerEmail  string    `json:"customer_email"`
	CustomerPhone  string    `json:"customer_phone"`
	TicketDetails  string    `json:"ticket_details"`
	BaseAmountUSD  float64   `json:"base_amount_usd"`
	ServiceFeeUSD  float64   `json:"service_fee_usd"`
	TotalAmountUSD float64   `json:"total_amount_usd"`
	Currency       string    `json:"currency"` // Always "USD"
	Status         string    `json:"status"`
	Scanned        bool      `json:"scanned"`
	ScannedAt      string    `json:"scanned_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type EventStatsResponse struct {
	EventID               int64                `json:"event_id"`
	EventName             string               `json:"event_name"`
	EventCurrency         string               `json:"event_currency"`
	EventDate             string               `json:"event_date"`
	VenueAddress          string               `json:"venue_address"`
	City                  string               `json:"city"`
	UserRole              string               `json:"user_role"`
	NetRevenueUSD         float64              `json:"net_revenue_usd"`
	TotalServiceFeeUSD    float64              `json:"total_service_fee_usd"`
	GrossRevenueUSD       float64              `json:"gross_revenue_usd"`
	TotalTicketsCapacity  int                  `json:"total_tickets_capacity"`
	TotalTicketsSold      int                  `json:"total_tickets_sold"`
	TotalTicketsRemaining int                  `json:"total_tickets_remaining"`
	OccupancyPercentage   float64              `json:"occupancy_percentage"`
	TotalOrders           int                  `json:"total_orders"`
	CategoryStats         []TicketCategoryStat `json:"category_stats"`
	Attendees             []AttendeeRecord     `json:"attendees"`
	TimeRange             string               `json:"time_range"`
}

type DashboardEventSummary struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	Category            string  `json:"category"`
	City                string  `json:"city"`
	EventDate           string  `json:"event_date"`
	ImageURL            string  `json:"image_url"`
	Status              string  `json:"status"`
	TicketsSold         int     `json:"tickets_sold"`
	TicketsCapacity     int     `json:"tickets_capacity"`
	OccupancyPercentage float64 `json:"occupancy_percentage"`
	NetRevenueUSD       float64 `json:"net_revenue_usd"`
	GrossRevenueUSD     float64 `json:"gross_revenue_usd"`
}

type DashboardRecentTransaction struct {
	OrderNumber    string    `json:"order_number"`
	CustomerName   string    `json:"customer_name"`
	CustomerEmail  string    `json:"customer_email"`
	EventName      string    `json:"event_name"`
	TicketDetails  string    `json:"ticket_details"`
	BaseAmountUSD  float64   `json:"base_amount_usd"`
	ServiceFeeUSD  float64   `json:"service_fee_usd"`
	TotalAmountUSD float64   `json:"total_amount_usd"`
	CreatedAt      time.Time `json:"created_at"`
}

type DashboardOrganizerUserSummary struct {
	ID            int64   `json:"id"`
	FullName      string  `json:"full_name"`
	Email         string  `json:"email"`
	Role          string  `json:"role"`
	TypeOrganizer string  `json:"type_organizer"`
	EventsCount   int     `json:"events_count"`
	TotalSalesUSD float64 `json:"total_sales_usd"`
}

type DashboardOverviewResponse struct {
	UserRole              string                          `json:"user_role"`
	UserName              string                          `json:"user_name"`
	UserEmail             string                          `json:"user_email"`
	NetRevenueUSD         float64                         `json:"net_revenue_usd"`
	TotalServiceFeeUSD    float64                         `json:"total_service_fee_usd"`
	GrossRevenueUSD       float64                         `json:"gross_revenue_usd"`
	TotalTicketsSold      int                             `json:"total_tickets_sold"`
	TotalTicketsCapacity  int                             `json:"total_tickets_capacity"`
	OccupancyPercentage   float64                         `json:"occupancy_percentage"`
	TotalEvents           int                             `json:"total_events"`
	ActiveEvents          int                             `json:"active_events"`
	UpcomingEvents        int                             `json:"upcoming_events"`
	CompletedEvents       int                             `json:"completed_events"`
	TotalUsers            int                             `json:"total_users"`
	TotalOrganizers       int                             `json:"total_organizers"`
	TotalPromoters        int                             `json:"total_promoters"`
	Events                []DashboardEventSummary         `json:"events"`
	RecentTransactions    []DashboardRecentTransaction    `json:"recent_transactions"`
	OrganizersSummary     []DashboardOrganizerUserSummary `json:"organizers_summary,omitempty"`
}

// GetDashboardOverviewHandler returns overall executive analytics formatted by user role (admin vs organizer)
func GetDashboardOverviewHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil || user == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		currentUserID := user.ID
		currentUserEmail := user.Email
		currentUserName := user.FullName
		userRole := user.Role

		if currentUserName == "" {
			currentUserName = currentUserEmail
		}

		var eventWhereClause string
		if userRole != "administrador" {
			eventWhereClause = fmt.Sprintf(" WHERE e.organizer_id = %d", currentUserID)
		}

		// Query Events for dashboard
		eventRows, err := db.Query(`
			SELECT e.id, e.name, e.category, e.city, e.event_date, COALESCE(e.image_url, ''), COALESCE(e.status, 'Publicado'),
				COALESCE((
					SELECT SUM(tc.capacity) FROM ticket_categories tc WHERE tc.event_id = e.id
				), 0) as remaining_capacity,
				COALESCE((
					SELECT SUM(pi.quantity) 
					FROM purchase_items pi
					JOIN purchases p ON p.id = pi.purchase_id
					WHERE p.event_id = e.id AND p.status = 'completed'
				), 0) as sold,
				COALESCE((
					SELECT SUM(COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal))
					FROM purchases p
					WHERE p.event_id = e.id AND p.status = 'completed'
				), 0) as net_usd,
				COALESCE((
					SELECT SUM(COALESCE(NULLIF(p.service_fee_usd, 0), p.service_fee))
					FROM purchases p
					WHERE p.event_id = e.id AND p.status = 'completed'
				), 0) as fee_usd
			FROM events e` + eventWhereClause + `
			ORDER BY e.created_at DESC
		`)

		events := make([]DashboardEventSummary, 0)
		var totalCapacitySum, totalSoldSum int
		var netRevUSDSum, totalServiceFeeUSD, totalGrossUSD float64
		var activeEvents, upcomingEvents, completedEvents int

		todayStr := time.Now().Format("2006-01-02")

		if err == nil {
			defer eventRows.Close()
			for eventRows.Next() {
				var ev DashboardEventSummary
				var totalCatCapacity int
				var feeUSD float64
				if scanErr := eventRows.Scan(
					&ev.ID, &ev.Name, &ev.Category, &ev.City, &ev.EventDate, &ev.ImageURL, &ev.Status,
					&totalCatCapacity, &ev.TicketsSold, &ev.NetRevenueUSD, &feeUSD,
				); scanErr == nil {
					ev.TicketsCapacity = totalCatCapacity
					ev.GrossRevenueUSD = ev.NetRevenueUSD + feeUSD

					if ev.TicketsCapacity > 0 {
						ev.OccupancyPercentage = (float64(ev.TicketsSold) / float64(ev.TicketsCapacity)) * 100.0
					}

					if ev.EventDate < todayStr {
						completedEvents++
					} else {
						activeEvents++
					}

					totalCapacitySum += ev.TicketsCapacity
					totalSoldSum += ev.TicketsSold

					events = append(events, ev)
				}
			}
		}

		// Calculate actual recorded platform KPIs from purchases
		var purchasesWhere string
		if userRole != "administrador" {
			purchasesWhere = fmt.Sprintf(" WHERE p.event_id IN (SELECT id FROM events WHERE organizer_id = %d) AND p.status = 'completed'", currentUserID)
		} else {
			purchasesWhere = " WHERE p.status = 'completed'"
		}
		_ = db.QueryRow(`
			SELECT COALESCE(SUM(COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal)), 0),
			       COALESCE(SUM(COALESCE(NULLIF(p.service_fee_usd, 0), p.service_fee)), 0),
			       COALESCE(SUM(COALESCE(NULLIF(p.total_amount_usd, 0), p.total_amount)), 0)
			FROM purchases p` + purchasesWhere).Scan(&netRevUSDSum, &totalServiceFeeUSD, &totalGrossUSD)

		var occupancyPercentage float64
		if totalCapacitySum > 0 {
			occupancyPercentage = (float64(totalSoldSum) / float64(totalCapacitySum)) * 100.0
		}

		// Query Recent Transactions Feed
		var purchaseWhereClause string
		if userRole != "administrador" && currentUserID > 0 {
			purchaseWhereClause = fmt.Sprintf(" WHERE p.event_id IN (SELECT id FROM events WHERE organizer_id = %d) AND p.status = 'completed'", currentUserID)
		} else {
			purchaseWhereClause = " WHERE p.status = 'completed'"
		}

		txRows, errTx := db.Query(`
			SELECT p.order_number, p.customer_name, p.customer_email, e.name as event_name, p.created_at,
				COALESCE((
					SELECT STRING_AGG(pi.quantity || 'x ' || pi.ticket_name, ', ')
					FROM purchase_items pi WHERE pi.purchase_id = p.id
				), 'Boleto General') as ticket_details,
				COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal) as base_usd,
				COALESCE(NULLIF(p.service_fee_usd, 0), p.service_fee) as fee_usd,
				COALESCE(NULLIF(p.total_amount_usd, 0), p.total_amount) as total_usd
			FROM purchases p
			JOIN events e ON e.id = p.event_id` + purchaseWhereClause + `
			ORDER BY p.created_at DESC
			LIMIT 10
		`)

		recentTxs := make([]DashboardRecentTransaction, 0)
		if errTx == nil {
			defer txRows.Close()
			for txRows.Next() {
				var tx DashboardRecentTransaction
				if errScan := txRows.Scan(
					&tx.OrderNumber, &tx.CustomerName, &tx.CustomerEmail, &tx.EventName, &tx.CreatedAt,
					&tx.TicketDetails, &tx.BaseAmountUSD, &tx.ServiceFeeUSD, &tx.TotalAmountUSD,
				); errScan == nil {
					recentTxs = append(recentTxs, tx)
				}
			}
		}

		// Query User/Organizer Counts if Admin
		var totalUsers, totalOrganizers, totalPromoters int
		organizersSummary := make([]DashboardOrganizerUserSummary, 0)

		if userRole == "administrador" {
			_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&totalUsers)
			_ = db.QueryRow("SELECT COUNT(*) FROM organizer_profiles WHERE type_organizer = 'organizador' OR type_organizer IS NULL").Scan(&totalOrganizers)
			_ = db.QueryRow("SELECT COUNT(*) FROM organizer_profiles WHERE type_organizer = 'promotor'").Scan(&totalPromoters)

			orgRows, errOrg := db.Query(`
				SELECT u.id, COALESCE(op.full_name, u.email), u.email, u.role, COALESCE(op.type_organizer, 'organizador'),
					COALESCE((SELECT COUNT(*) FROM events WHERE organizer_id = u.id), 0) as events_cnt,
					COALESCE((
						SELECT SUM(COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal))
						FROM purchases p
						JOIN events e ON e.id = p.event_id
						WHERE e.organizer_id = u.id AND p.status = 'completed'
					), 0) as total_sales
				FROM users u
				LEFT JOIN organizer_profiles op ON op.user_id = u.id
				WHERE u.role IN ('organizador', 'promotor') OR (op.user_id IS NOT NULL AND u.role != 'cliente')
				ORDER BY total_sales DESC
				LIMIT 20
			`)

			if errOrg == nil {
				defer orgRows.Close()
				for orgRows.Next() {
					var org DashboardOrganizerUserSummary
					if errScan := orgRows.Scan(&org.ID, &org.FullName, &org.Email, &org.Role, &org.TypeOrganizer, &org.EventsCount, &org.TotalSalesUSD); errScan == nil {
						organizersSummary = append(organizersSummary, org)
					}
				}
			}
		}

		resp := DashboardOverviewResponse{
			UserRole:              userRole,
			UserName:              currentUserName,
			UserEmail:             currentUserEmail,
			NetRevenueUSD:         netRevUSDSum,
			TotalServiceFeeUSD:    totalServiceFeeUSD,
			GrossRevenueUSD:       totalGrossUSD,
			TotalTicketsSold:      totalSoldSum,
			TotalTicketsCapacity:  totalCapacitySum,
			OccupancyPercentage:   occupancyPercentage,
			TotalEvents:           len(events),
			ActiveEvents:          activeEvents,
			UpcomingEvents:        upcomingEvents,
			CompletedEvents:       completedEvents,
			TotalUsers:            totalUsers,
			TotalOrganizers:       totalOrganizers,
			TotalPromoters:        totalPromoters,
			Events:                events,
			RecentTransactions:    recentTxs,
			OrganizersSummary:     organizersSummary,
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// GetOrganizerEventStatsHandler returns full event financial statistics and attendee breakdown in USD.
func GetOrganizerEventStatsHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		eventIDStr := r.URL.Query().Get("event_id")
		if eventIDStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "se requiere event_id"})
			return
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_id inválido"})
			return
		}

		timeRange := r.URL.Query().Get("time_range")
		if timeRange == "" {
			timeRange = "all"
		}

		// Determine user role and identity from session
		user, errUser := GetAuthenticatedUser(r, db, rdb)
		if errUser != nil || user == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		userRole := user.Role
		userID := user.ID

		// Time range SQL filter clause
		var timeClause string
		switch timeRange {
		case "today":
			timeClause = " AND p.created_at >= NOW() - INTERVAL '24 hours'"
		case "7d":
			timeClause = " AND p.created_at >= NOW() - INTERVAL '7 days'"
		case "30d":
			timeClause = " AND p.created_at >= NOW() - INTERVAL '30 days'"
		default:
			timeClause = ""
		}

		// Query Event Master Details
		var eventName, eventDate, venueAddress, city, eventCurrency string
		var organizerID int64
		var eventFeePercentage float64
		err = db.QueryRow(`
			SELECT name, event_date, venue_address, city, organizer_id, COALESCE(currency, 'USD'), COALESCE(service_fee_percentage, 20.00)
			FROM events
			WHERE id = $1
		`, eventID).Scan(&eventName, &eventDate, &venueAddress, &city, &organizerID, &eventCurrency, &eventFeePercentage)

		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
			return
		}

		if userRole != "administrador" && organizerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "no tienes permisos para consultar métricas de este evento"})
			return
		}

		// Query Ticket Categories Breakdown
		categoryRows, err := db.Query(`
			SELECT tc.id, tc.name, tc.price, tc.capacity,
				COALESCE((
					SELECT SUM(pi.quantity) 
					FROM purchase_items pi
					JOIN purchases p ON p.id = pi.purchase_id
					WHERE pi.ticket_category_id = tc.id AND p.status = 'completed'`+timeClause+`
				), 0) as sold,
				COALESCE((
					SELECT SUM(pi.quantity * COALESCE(NULLIF(pi.price_usd, 0), pi.price))
					FROM purchase_items pi
					JOIN purchases p ON p.id = pi.purchase_id
					WHERE pi.ticket_category_id = tc.id AND p.status = 'completed'`+timeClause+`
				), 0) as net_usd
			FROM ticket_categories tc
			WHERE tc.event_id = $1
			ORDER BY tc.price DESC
		`, eventID)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error consultando categorías"})
			return
		}
		defer categoryRows.Close()

		liveRates := GetLiveRates(rdb)
		eventRate := liveRates[eventCurrency]
		if eventRate <= 0 {
			eventRate = 1.0
		}

		eventFeeRate := eventFeePercentage / 100.0
		categoryStats := make([]TicketCategoryStat, 0)
		var totalCapacity, totalSold int
		var netRevenueUSD, totalServiceFeeUSD, grossRevenueUSD float64

		for categoryRows.Next() {
			var cat TicketCategoryStat
			var totalCatCapacity int
			var catPriceNative, catNetUSD float64
			if err := categoryRows.Scan(&cat.ID, &cat.Name, &catPriceNative, &totalCatCapacity, &cat.TicketsSold, &catNetUSD); err == nil {
				cat.Capacity = totalCatCapacity
				cat.TicketsRemaining = totalCatCapacity - cat.TicketsSold
				if cat.TicketsRemaining < 0 {
					cat.TicketsRemaining = 0
				}

				if strings.EqualFold(eventCurrency, "USD") {
					cat.PriceUSD = catPriceNative
				} else {
					cat.PriceUSD = catPriceNative / eventRate
				}

				cat.NetRevenueUSD = catNetUSD
				if cat.NetRevenueUSD == 0 && cat.TicketsSold > 0 {
					cat.NetRevenueUSD = float64(cat.TicketsSold) * cat.PriceUSD
				}
				cat.ServiceFeeUSD = cat.NetRevenueUSD * eventFeeRate
				cat.GrossRevenueUSD = cat.NetRevenueUSD + cat.ServiceFeeUSD

				if cat.Capacity > 0 {
					cat.PercentageSold = (float64(cat.TicketsSold) / float64(cat.Capacity)) * 100.0
				}

				totalCapacity += cat.Capacity
				totalSold += cat.TicketsSold
				netRevenueUSD += cat.NetRevenueUSD
				totalServiceFeeUSD += cat.ServiceFeeUSD
				grossRevenueUSD += cat.GrossRevenueUSD

				categoryStats = append(categoryStats, cat)
			}
		}

		var occupancyPercentage float64
		if totalCapacity > 0 {
			occupancyPercentage = (float64(totalSold) / float64(totalCapacity)) * 100.0
		}

		// Calculate actual recorded event totals directly from purchases table
		_ = db.QueryRow(`
			SELECT COALESCE(SUM(COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal)), 0),
			       COALESCE(SUM(COALESCE(NULLIF(p.service_fee_usd, 0), p.service_fee)), 0),
			       COALESCE(SUM(COALESCE(NULLIF(p.total_amount_usd, 0), p.total_amount)), 0)
			FROM purchases p
			WHERE p.event_id = $1 AND p.status = 'completed'`+timeClause, eventID).Scan(&netRevenueUSD, &totalServiceFeeUSD, &grossRevenueUSD)

		// Query Attendees & Purchases List reading actual recorded purchase financial amounts directly
		attendeeRows, err := db.Query(`
			SELECT p.id, p.order_number, p.customer_name, p.customer_email, COALESCE(p.customer_phone, ''), 
				COALESCE(NULLIF(p.subtotal_usd, 0), p.subtotal) as base_usd,
				COALESCE(NULLIF(p.service_fee_usd, 0), p.service_fee) as fee_usd,
				COALESCE(NULLIF(p.total_amount_usd, 0), p.total_amount) as gross_usd,
				'USD' as currency, 
				p.status, p.created_at,
				COALESCE((
					SELECT STRING_AGG(pi.quantity || 'x ' || pi.ticket_name, ', ')
					FROM purchase_items pi WHERE pi.purchase_id = p.id
				), 'Boleto General') as ticket_details
			FROM purchases p
			WHERE p.event_id = $1 AND p.status = 'completed'`+timeClause+`
			ORDER BY p.created_at DESC
		`, eventID)

		attendees := make([]AttendeeRecord, 0)
		totalOrders := 0

		if err == nil {
			defer attendeeRows.Close()
			for attendeeRows.Next() {
				var att AttendeeRecord
				var rawCurrency string

				if err := attendeeRows.Scan(
					&att.OrderID, &att.OrderNumber, &att.CustomerName, &att.CustomerEmail, &att.CustomerPhone,
					&att.BaseAmountUSD, &att.ServiceFeeUSD, &att.TotalAmountUSD, &rawCurrency, &att.Status, &att.CreatedAt, &att.TicketDetails,
				); err == nil {
					att.Currency = "USD"

					// Check if scanned in ticket_scans table
					var scannedAt time.Time
					var scanID int64
					scanErr := db.QueryRow(`
						SELECT id, scanned_at FROM ticket_scans 
						WHERE order_number = $1 OR ticket_serial LIKE $2
						LIMIT 1
					`, att.OrderNumber, att.OrderNumber+"-%").Scan(&scanID, &scannedAt)

					if scanErr == nil {
						att.Scanned = true
						att.ScannedAt = scannedAt.Format("15:04:05 02/01/2006")
					}

					attendees = append(attendees, att)
					totalOrders++
				}
			}
		}

		resp := EventStatsResponse{
			EventID:               eventID,
			EventName:             eventName,
			EventCurrency:         eventCurrency,
			EventDate:             eventDate,
			VenueAddress:          venueAddress,
			City:                  city,
			UserRole:              userRole,
			NetRevenueUSD:         netRevenueUSD,
			TotalServiceFeeUSD:    totalServiceFeeUSD,
			GrossRevenueUSD:       grossRevenueUSD,
			TotalTicketsCapacity:  totalCapacity,
			TotalTicketsSold:      totalSold,
			TotalTicketsRemaining: totalCapacity - totalSold,
			OccupancyPercentage:   occupancyPercentage,
			TotalOrders:           totalOrders,
			CategoryStats:         categoryStats,
			Attendees:             attendees,
			TimeRange:             timeRange,
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// ExportAttendeesCSVHandler exporta la lista de compradores/asistentes de un evento en formato CSV con soporte UTF-8 BOM para Excel
func ExportAttendeesCSVHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		// Session check
		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		userID := user.ID

		eventIDStr := r.URL.Query().Get("event_id")
		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil || eventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid event_id"})
			return
		}

		// Verify event & role
		var userRole string
		_ = db.QueryRow("SELECT role FROM users WHERE id = $1", userID).Scan(&userRole)

		var eventName string
		var organizerID int64
		err = db.QueryRow("SELECT name, organizer_id FROM events WHERE id = $1", eventID).Scan(&eventName, &organizerID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
			return
		}

		if userRole != "administrador" && organizerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: cannot export this event"})
			return
		}

		// Query attendees
		var selectColumn, headerAmount string
		if strings.EqualFold(strings.TrimSpace(userRole), "administrador") {
			selectColumn = "COALESCE(NULLIF(total_amount_usd, 0), total_amount)"
			headerAmount = "Total Bruto Pagado ($ USD)"
		} else {
			selectColumn = "COALESCE(NULLIF(subtotal_usd, 0), subtotal)"
			headerAmount = "Monto Neto Recaudado ($ USD)"
		}

		rows, err := db.Query(fmt.Sprintf(`
			SELECT id, order_number, customer_name, customer_email, COALESCE(customer_phone, ''), 
			       %s as amount_usd, status, created_at 
			FROM purchases 
			WHERE event_id = $1 AND status = 'completed'
			ORDER BY created_at DESC
		`, selectColumn), eventID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db query error"})
			return
		}
		defer rows.Close()

		var buf bytes.Buffer
		// UTF-8 BOM byte sequence so Microsoft Excel opens special characters correctly
		buf.Write([]byte{0xEF, 0xBB, 0xBF})

		writer := csv.NewWriter(&buf)

		// Header
		_ = writer.Write([]string{
			"Número de Orden",
			"Nombre del Comprador",
			"Correo Electrónico",
			"Teléfono",
			"Detalle de Boletos",
			headerAmount,
			"Fecha de Compra",
			"Estado Escáner / Ingreso",
		})

		for rows.Next() {
			var purID int64
			var orderNum, custName, custEmail, custPhone, status string
			var amountUSD float64
			var createdAt time.Time

			if err := rows.Scan(&purID, &orderNum, &custName, &custEmail, &custPhone, &amountUSD, &status, &createdAt); err == nil {
				// Query ticket items
				var itemsStr string
				itemRows, itemErr := db.Query(`
					SELECT ticket_name, quantity 
					FROM purchase_items 
					WHERE purchase_id = $1
				`, purID)
				if itemErr == nil {
					var itemParts []string
					for itemRows.Next() {
						var tName string
						var qty int
						if err := itemRows.Scan(&tName, &qty); err == nil {
							itemParts = append(itemParts, fmt.Sprintf("%s (x%d)", tName, qty))
						}
					}
					itemRows.Close()
					itemsStr = strings.Join(itemParts, " | ")
				}

				// Check scan status
				var scanCount int
				_ = db.QueryRow("SELECT COUNT(*) FROM ticket_scans WHERE order_number = $1 OR ticket_serial LIKE $2", orderNum, orderNum+"-%").Scan(&scanCount)

				scannedLabel := "Pendiente por Ingresar"
				if scanCount > 0 {
					scannedLabel = fmt.Sprintf("Ingresado (%d escaneos)", scanCount)
				}

				_ = writer.Write([]string{
					orderNum,
					custName,
					custEmail,
					custPhone,
					itemsStr,
					fmt.Sprintf("$%.2f", amountUSD),
					createdAt.Format("2006-01-02 15:04:05"),
					scannedLabel,
				})
			}
		}

		writer.Flush()

		safeFileName := strings.ReplaceAll(eventName, " ", "_")
		safeFileName = strings.ReplaceAll(safeFileName, "/", "_")

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"Asistentes_%s_%d.csv\"", safeFileName, eventID))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}
}
