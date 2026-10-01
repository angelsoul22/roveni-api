package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Event struct {
	ID                int64            `json:"id"`
	OrganizerID       int64            `json:"organizer_id"`
	Name              string           `json:"name"`
	Description       string           `json:"description"`
	Category          string           `json:"category"`
	ImageURL          string           `json:"image_url"`
	Country           string           `json:"country"`
	City              string           `json:"city"`
	VenueAddress      string           `json:"venue_address"`
	EventDate         string           `json:"event_date"`
	DoorsOpenTime     string           `json:"doors_open_time"`
	ShowStartTime     string           `json:"show_start_time"`
	SaleStartDate     string           `json:"sale_start_date"`
	SaleStartTime     string           `json:"sale_start_time"`
	MaxTicketsPerUser int              `json:"max_tickets_per_user"`
	Status            string           `json:"status"`
	Currency          string           `json:"currency"`
	SpotifyURL        string           `json:"spotify_url"`
	AppleMusicURL     string           `json:"apple_music_url"`
	YouTubeURL        string           `json:"youtube_url"`
	InstagramURL      string           `json:"instagram_url"`
	TikTokURL         string           `json:"tiktok_url"`
	ServiceFeePercentage float64       `json:"service_fee_percentage"`
	CourtesyQuota        int           `json:"courtesy_quota"`
	CreatedAt            time.Time     `json:"created_at"`
	TicketCategories     []TicketCategory `json:"ticket_categories,omitempty"`
}

type TicketCategory struct {
	ID          int64   `json:"id"`
	EventID     int64   `json:"event_id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Capacity    int     `json:"capacity"`
	SoldCount   int     `json:"sold_count"`
	Available   int     `json:"available"`
	Description string  `json:"description"`
}

type TicketCategoryInput struct {
	ID          int64   `json:"id,omitempty"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Capacity    int     `json:"capacity"`
	Description string  `json:"description"`
}

func fetchCategoriesForEvent(db *sql.DB, eventID int64) []TicketCategory {
	rows, err := db.Query(`
		SELECT 
			tc.id, 
			tc.event_id, 
			tc.name, 
			tc.price, 
			tc.capacity, 
			COALESCE(sub.sold_count, 0) as sold_count,
			GREATEST(0, tc.capacity - COALESCE(sub.sold_count, 0)) as available,
			COALESCE(tc.description, '')
		FROM ticket_categories tc
		LEFT JOIN (
			SELECT pi.ticket_category_id, SUM(pi.quantity) as sold_count
			FROM purchase_items pi
			JOIN purchases p ON pi.purchase_id = p.id
			WHERE p.status = 'completed'
			GROUP BY pi.ticket_category_id
		) sub ON tc.id = sub.ticket_category_id
		WHERE tc.event_id = $1
		ORDER BY tc.id ASC
	`, eventID)
	if err != nil {
		return []TicketCategory{}
	}
	defer rows.Close()

	cats := []TicketCategory{}
	for rows.Next() {
		var cat TicketCategory
		if err := rows.Scan(&cat.ID, &cat.EventID, &cat.Name, &cat.Price, &cat.Capacity, &cat.SoldCount, &cat.Available, &cat.Description); err == nil {
			cats = append(cats, cat)
		}
	}
	return cats
}

// CreateEventHandler crea un evento y sus categorías de boletos en una transacción
func CreateEventHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
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
		userID := user.ID
		role := user.Role

		// Parse multipart form (max 10MB)
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse form"})
			return
		}

		name := r.FormValue("name")
		description := r.FormValue("description")
		category := r.FormValue("category")
		country := r.FormValue("country")
		city := r.FormValue("city")
		venueAddress := r.FormValue("venue_address")
		eventDate := r.FormValue("event_date")
		doorsOpenTime := r.FormValue("doors_open_time")
		showStartTime := r.FormValue("show_start_time")
		saleStartDate := r.FormValue("sale_start_date")
		saleStartTime := r.FormValue("sale_start_time")
		maxTicketsStr := r.FormValue("max_tickets_per_user")
		
		spotifyURL := r.FormValue("spotify_url")
		appleMusicURL := r.FormValue("apple_music_url")
		youtubeURL := r.FormValue("youtube_url")
		instagramURL := r.FormValue("instagram_url")
		tiktokURL := r.FormValue("tiktok_url")
		statusCreate := NormalizeEventStatus(r.FormValue("status"))
		currencyCreate := r.FormValue("currency")
		if currencyCreate == "" {
			currencyCreate = "USD"
		}

		courtesyQuotaStr := r.FormValue("courtesy_quota")
		courtesyQuota := 0
		if courtesyQuotaStr != "" {
			if parsed, err := strconv.Atoi(courtesyQuotaStr); err == nil && parsed >= 0 {
				courtesyQuota = parsed
			}
		}

		serviceFeePercentage := 20.00
		if strings.EqualFold(strings.TrimSpace(role), "administrador") {
			feeStr := r.FormValue("service_fee_percentage")
			if feeStr != "" {
				if parsedFee, err := strconv.ParseFloat(feeStr, 64); err == nil {
					if parsedFee < 0 { parsedFee = 0 }
					if parsedFee > 100 { parsedFee = 100 }
					serviceFeePercentage = parsedFee
				}
			}
		}

		maxTickets := 4
		if maxTicketsStr != "" {
			if parsed, err := strconv.Atoi(maxTicketsStr); err == nil {
				maxTickets = parsed
			}
		}

		if name == "" || description == "" || category == "" || country == "" || city == "" || venueAddress == "" || eventDate == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing required fields"})
			return
		}

		// Save Image if provided
		var imageURL string
		file, header, err := r.FormFile("image_file")
		if err == nil {
			defer file.Close()
			uploadsDir := "./uploads"
			_ = os.MkdirAll(uploadsDir, 0755)

			uniqueID := uuid.NewString()
			filename := fmt.Sprintf("%s_%s", uniqueID, header.Filename)
			filePath := filepath.Join(uploadsDir, filename)

			dst, err := os.Create(filePath)
			if err == nil {
				defer dst.Close()
				if _, err := io.Copy(dst, file); err == nil {
					imageURL = "/uploads/" + filename
				}
			}
		}

		// Parse ticket categories JSON
		categoriesJSON := r.FormValue("ticket_categories")
		var categoriesInput []TicketCategoryInput
		if categoriesJSON != "" {
			if err := json.Unmarshal([]byte(categoriesJSON), &categoriesInput); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ticket categories JSON"})
				return
			}
		}

		if len(categoriesInput) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "must add at least one ticket category"})
			return
		}

		// Begin Tx
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db tx error"})
			return
		}
		defer tx.Rollback()

		var eventID int64
		err = tx.QueryRow(`
			INSERT INTO events (organizer_id, name, description, category, image_url, country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, status, currency, spotify_url, apple_music_url, youtube_url, instagram_url, tiktok_url, service_fee_percentage, courtesy_quota)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
			RETURNING id
		`, userID, name, description, category, imageURL, country, city, venueAddress, eventDate, doorsOpenTime, showStartTime, saleStartDate, saleStartTime, maxTickets, statusCreate, currencyCreate, spotifyURL, appleMusicURL, youtubeURL, instagramURL, tiktokURL, serviceFeePercentage, courtesyQuota).Scan(&eventID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to insert event: %v", err)})
			return
		}

		// Insert ticket categories
		for _, cat := range categoriesInput {
			if cat.Name == "" || cat.Capacity <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ticket category name or capacity"})
				return
			}
			_, err = tx.Exec(`
				INSERT INTO ticket_categories (event_id, name, price, capacity, description)
				VALUES ($1, $2, $3, $4, $5)
			`, eventID, cat.Name, cat.Price, cat.Capacity, cat.Description)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to insert ticket category: %v", err)})
				return
			}
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db commit error"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{"id": eventID})
	}
}

// ListEventsHandler lista todos los eventos (el admin ve todos, el organizador ve solo los suyos)
func ListEventsHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
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
		userID := user.ID
		role := user.Role

		var rows *sql.Rows
		if role == "administrador" {
			// Admin sees all events (including paused/cancelled)
			rows, err = db.Query(`
				SELECT id, organizer_id, name, description, category, COALESCE(image_url, ''), country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0), created_at 
				FROM events 
				ORDER BY created_at DESC
			`)
		} else {
			// Organizer sees only their own events (including paused/cancelled)
			rows, err = db.Query(`
				SELECT id, organizer_id, name, description, category, COALESCE(image_url, ''), country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0), created_at 
				FROM events 
				WHERE organizer_id = $1 
				ORDER BY created_at DESC
			`, userID)
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db query error"})
			return
		}
		defer rows.Close()

		var events []Event
		for rows.Next() {
			var ev Event
			err = rows.Scan(
				&ev.ID, &ev.OrganizerID, &ev.Name, &ev.Description, &ev.Category, &ev.ImageURL, &ev.Country, &ev.City, &ev.VenueAddress,
				&ev.EventDate, &ev.DoorsOpenTime, &ev.ShowStartTime, &ev.SaleStartDate, &ev.SaleStartTime, &ev.MaxTicketsPerUser,
				&ev.Status, &ev.Currency,
				&ev.SpotifyURL, &ev.AppleMusicURL, &ev.YouTubeURL, &ev.InstagramURL, &ev.TikTokURL, &ev.ServiceFeePercentage, &ev.CourtesyQuota, &ev.CreatedAt,
			)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "scan event error"})
				return
			}

			ev.TicketCategories = fetchCategoriesForEvent(db, ev.ID)

			events = append(events, ev)
		}

		writeJSON(w, http.StatusOK, events)
	}
}

// GetEventDetailHandler obtiene los detalles de un evento en particular
func GetEventDetailHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
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
		userID := user.ID
		role := user.Role

		// Get query parameter 'id'
		eventIDStr := r.URL.Query().Get("id")
		if eventIDStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing event id"})
			return
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid event id"})
			return
		}

		// Fetch event
		var ev Event
		err = db.QueryRow(`
			SELECT id, organizer_id, name, description, category, COALESCE(image_url, ''), country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0), created_at 
			FROM events 
			WHERE id = $1
		`, eventID).Scan(
			&ev.ID, &ev.OrganizerID, &ev.Name, &ev.Description, &ev.Category, &ev.ImageURL, &ev.Country, &ev.City, &ev.VenueAddress,
			&ev.EventDate, &ev.DoorsOpenTime, &ev.ShowStartTime, &ev.SaleStartDate, &ev.SaleStartTime, &ev.MaxTicketsPerUser,
			&ev.Status, &ev.Currency,
			&ev.SpotifyURL, &ev.AppleMusicURL, &ev.YouTubeURL, &ev.InstagramURL, &ev.TikTokURL, &ev.ServiceFeePercentage, &ev.CourtesyQuota, &ev.CreatedAt,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		// Restriction: each organizer sees their own, admin sees all
		if role != "administrador" && ev.OrganizerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: you cannot access this event"})
			return
		}

		ev.TicketCategories = fetchCategoriesForEvent(db, ev.ID)

		writeJSON(w, http.StatusOK, ev)
	}
}

// PublicListEventsHandler returns all events with support for search query (q), category filter, and pagination (page, limit)
func PublicListEventsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		queryStr := r.URL.Query().Get("q")
		if queryStr == "" {
			queryStr = r.URL.Query().Get("search")
		}
		categoryFilter := r.URL.Query().Get("category")

		pageStr := r.URL.Query().Get("page")
		limitStr := r.URL.Query().Get("limit")

		usePagination := pageStr != "" || limitStr != ""

		page := 1
		limit := 12
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}

		offset := (page - 1) * limit

		// Dynamic WHERE clause — exclude Pausado/Cancelado/Finalizado/Paused/Cancelled from public view
		whereClause := "WHERE (LOWER(TRIM(COALESCE(NULLIF(status, ''), 'Publicado'))) NOT IN ('pausado', 'cancelado', 'finalizado', 'paused', 'cancelled'))"
		args := []any{}
		argIdx := 1

		if queryStr != "" {
			whereClause += fmt.Sprintf(" AND (LOWER(name) LIKE $%d OR LOWER(description) LIKE $%d OR LOWER(city) LIKE $%d OR LOWER(venue_address) LIKE $%d OR LOWER(category) LIKE $%d)", argIdx, argIdx, argIdx, argIdx, argIdx)
			args = append(args, "%"+strings.ToLower(queryStr)+"%")
			argIdx++
		}

		if categoryFilter != "" && categoryFilter != "all" && categoryFilter != "Todos" {
			whereClause += fmt.Sprintf(" AND LOWER(category) = LOWER($%d)", argIdx)
			args = append(args, categoryFilter)
			argIdx++
		}

		// Count total matching events
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM events %s", whereClause)
		var totalCount int
		_ = db.QueryRow(countQuery, args...).Scan(&totalCount)

		// Main query
		selectQuery := fmt.Sprintf(`
			SELECT id, organizer_id, name, description, category, COALESCE(image_url, ''), country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0), created_at 
			FROM events 
			%s 
			ORDER BY event_date ASC
		`, whereClause)

		if usePagination {
			selectQuery += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
			args = append(args, limit, offset)
		}

		rows, err := db.Query(selectQuery, args...)
		if err != nil {
			log.Printf("[EVENTS ERROR] PublicListEvents query error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db query error"})
			return
		}
		defer rows.Close()

		var events []Event
		for rows.Next() {
			var ev Event
			err = rows.Scan(
				&ev.ID, &ev.OrganizerID, &ev.Name, &ev.Description, &ev.Category, &ev.ImageURL, &ev.Country, &ev.City, &ev.VenueAddress,
				&ev.EventDate, &ev.DoorsOpenTime, &ev.ShowStartTime, &ev.SaleStartDate, &ev.SaleStartTime, &ev.MaxTicketsPerUser,
				&ev.Status, &ev.Currency,
				&ev.SpotifyURL, &ev.AppleMusicURL, &ev.YouTubeURL, &ev.InstagramURL, &ev.TikTokURL, &ev.ServiceFeePercentage, &ev.CourtesyQuota, &ev.CreatedAt,
			)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "scan event error"})
				return
			}

			ev.TicketCategories = fetchCategoriesForEvent(db, ev.ID)
			events = append(events, ev)
		}

		if usePagination {
			totalPages := 1
			if limit > 0 {
				totalPages = int(math.Ceil(float64(totalCount) / float64(limit)))
			}
			if totalPages < 1 {
				totalPages = 1
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"events":      events,
				"total":       totalCount,
				"page":        page,
				"limit":       limit,
				"total_pages": totalPages,
			})
		} else {
			writeJSON(w, http.StatusOK, events)
		}
	}
}

// PublicEventDetailHandler returns event and ticket categories for a single event without requiring a session
func PublicEventDetailHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		eventIDStr := r.URL.Query().Get("id")
		if eventIDStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing event id"})
			return
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid event id"})
			return
		}

		var ev Event
		err = db.QueryRow(`
			SELECT id, organizer_id, name, description, category, COALESCE(image_url, ''), country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0), created_at 
			FROM events 
			WHERE id = $1
		`, eventID).Scan(
			&ev.ID, &ev.OrganizerID, &ev.Name, &ev.Description, &ev.Category, &ev.ImageURL, &ev.Country, &ev.City, &ev.VenueAddress,
			&ev.EventDate, &ev.DoorsOpenTime, &ev.ShowStartTime, &ev.SaleStartDate, &ev.SaleStartTime, &ev.MaxTicketsPerUser,
			&ev.Status, &ev.Currency,
			&ev.SpotifyURL, &ev.AppleMusicURL, &ev.YouTubeURL, &ev.InstagramURL, &ev.TikTokURL, &ev.ServiceFeePercentage, &ev.CourtesyQuota, &ev.CreatedAt,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		ev.TicketCategories = fetchCategoriesForEvent(db, ev.ID)

		writeJSON(w, http.StatusOK, ev)
	}
}

// UpdateEventHandler actualiza los detalles de un evento y sus categorías de boletos
func UpdateEventHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		userID := user.ID
		role := user.Role

		_ = r.ParseMultipartForm(10 << 20)

		eventIDStr := r.FormValue("id")
		if eventIDStr == "" {
			eventIDStr = r.FormValue("event_id")
		}
		if eventIDStr == "" {
			eventIDStr = r.URL.Query().Get("id")
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil || eventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or missing event id"})
			return
		}

		// Check event ownership
		var organizerID int64
		err = db.QueryRow("SELECT organizer_id FROM events WHERE id = $1", eventID).Scan(&organizerID)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		if role != "administrador" && organizerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "no tienes permiso para editar este evento"})
			return
		}

		name := r.FormValue("name")
		description := r.FormValue("description")
		category := r.FormValue("category")
		country := r.FormValue("country")
		city := r.FormValue("city")
		venueAddress := r.FormValue("venue_address")
		eventDate := r.FormValue("event_date")
		doorsOpenTime := r.FormValue("doors_open_time")
		showStartTime := r.FormValue("show_start_time")
		saleStartDate := r.FormValue("sale_start_date")
		saleStartTime := r.FormValue("sale_start_time")
		maxTicketsStr := r.FormValue("max_tickets_per_user")
		status := r.FormValue("status")
		currencyUpdate := r.FormValue("currency")
		spotifyURL := r.FormValue("spotify_url")
		appleMusicURL := r.FormValue("apple_music_url")
		youtubeURL := r.FormValue("youtube_url")
		instagramURL := r.FormValue("instagram_url")
		tiktokURL := r.FormValue("tiktok_url")

		courtesyQuotaStr := r.FormValue("courtesy_quota")

		// Fetch existing event values for fallback on empty/omitted fields
		var existingName, existingDesc, existingCat, existingCountry, existingCity, existingVenue, existingDate, existingDoors, existingShow, existingSaleDate, existingSaleTime, existingStatus, existingCurr, existingSpot, existingApple, existingYT, existingInsta, existingTikTok string
		var existingMax, existingCourtesyQuota int
		var existingFeePercentage float64
		errExist := db.QueryRow(`
			SELECT name, description, category, country, city, venue_address, event_date, doors_open_time, show_start_time, sale_start_date, sale_start_time, max_tickets_per_user, COALESCE(status, 'Publicado'), COALESCE(currency, 'USD'), COALESCE(spotify_url, ''), COALESCE(apple_music_url, ''), COALESCE(youtube_url, ''), COALESCE(instagram_url, ''), COALESCE(tiktok_url, ''), COALESCE(service_fee_percentage, 20.00), COALESCE(courtesy_quota, 0)
			FROM events WHERE id = $1
		`, eventID).Scan(
			&existingName, &existingDesc, &existingCat, &existingCountry, &existingCity, &existingVenue, &existingDate, &existingDoors, &existingShow, &existingSaleDate, &existingSaleTime, &existingMax, &existingStatus, &existingCurr, &existingSpot, &existingApple, &existingYT, &existingInsta, &existingTikTok, &existingFeePercentage, &existingCourtesyQuota,
		)
		if errExist != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
			return
		}

		if name == "" { name = existingName }
		if description == "" { description = existingDesc }
		if category == "" { category = existingCat }
		if country == "" { country = existingCountry }
		if city == "" { city = existingCity }
		if venueAddress == "" { venueAddress = existingVenue }
		if eventDate == "" { eventDate = existingDate }
		if doorsOpenTime == "" { doorsOpenTime = existingDoors }
		if showStartTime == "" { showStartTime = existingShow }
		if saleStartDate == "" { saleStartDate = existingSaleDate }
		if saleStartTime == "" { saleStartTime = existingSaleTime }
		if status == "" {
			status = NormalizeEventStatus(existingStatus)
		} else {
			status = NormalizeEventStatus(status)
		}
		if currencyUpdate == "" { currencyUpdate = existingCurr }
		if spotifyURL == "" { spotifyURL = existingSpot }
		if appleMusicURL == "" { appleMusicURL = existingApple }
		if youtubeURL == "" { youtubeURL = existingYT }
		if instagramURL == "" { instagramURL = existingInsta }
		if tiktokURL == "" { tiktokURL = existingTikTok }

		courtesyQuota := existingCourtesyQuota
		if courtesyQuotaStr != "" {
			if parsed, err := strconv.Atoi(courtesyQuotaStr); err == nil && parsed >= 0 {
				courtesyQuota = parsed
			}
		}

		feePercentage := existingFeePercentage
		if strings.EqualFold(strings.TrimSpace(role), "administrador") {
			feeStr := r.FormValue("service_fee_percentage")
			if feeStr != "" {
				if parsedFee, err := strconv.ParseFloat(feeStr, 64); err == nil {
					if parsedFee < 0 { parsedFee = 0 }
					if parsedFee > 100 { parsedFee = 100 }
					feePercentage = parsedFee
				}
			}
		}

		maxTickets := existingMax
		if maxTicketsStr != "" {
			if parsed, err := strconv.Atoi(maxTicketsStr); err == nil && parsed > 0 {
				maxTickets = parsed
			}
		}

		// Save new Image if provided
		var imageURL string
		file, header, err := r.FormFile("image_file")
		if err == nil {
			defer file.Close()
			uploadsDir := "./uploads"
			_ = os.MkdirAll(uploadsDir, 0755)

			uniqueID := uuid.NewString()
			filename := fmt.Sprintf("%s_%s", uniqueID, header.Filename)
			filePath := filepath.Join(uploadsDir, filename)

			dst, err := os.Create(filePath)
			if err == nil {
				defer dst.Close()
				if _, err := io.Copy(dst, file); err == nil {
					imageURL = "/uploads/" + filename
				}
			}
		}

		// Begin Tx
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db tx error"})
			return
		}
		defer tx.Rollback()

		if imageURL != "" {
			_, err = tx.Exec(`
				UPDATE events 
				SET name = $1, description = $2, category = $3, country = $4, city = $5, venue_address = $6, 
				    event_date = $7, doors_open_time = $8, show_start_time = $9, sale_start_date = $10, sale_start_time = $11, 
				    max_tickets_per_user = $12, status = $13, currency = $14, spotify_url = $15, apple_music_url = $16, youtube_url = $17, 
				    instagram_url = $18, tiktok_url = $19, service_fee_percentage = $20, courtesy_quota = $21, image_url = $22
				WHERE id = $23
			`, name, description, category, country, city, venueAddress, eventDate, doorsOpenTime, showStartTime, saleStartDate, saleStartTime, maxTickets, status, currencyUpdate, spotifyURL, appleMusicURL, youtubeURL, instagramURL, tiktokURL, feePercentage, courtesyQuota, imageURL, eventID)
		} else {
			_, err = tx.Exec(`
				UPDATE events 
				SET name = $1, description = $2, category = $3, country = $4, city = $5, venue_address = $6, 
				    event_date = $7, doors_open_time = $8, show_start_time = $9, sale_start_date = $10, sale_start_time = $11, 
				    max_tickets_per_user = $12, status = $13, currency = $14, spotify_url = $15, apple_music_url = $16, youtube_url = $17, 
				    instagram_url = $18, tiktok_url = $19, service_fee_percentage = $20, courtesy_quota = $21
				WHERE id = $22
			`, name, description, category, country, city, venueAddress, eventDate, doorsOpenTime, showStartTime, saleStartDate, saleStartTime, maxTickets, status, currencyUpdate, spotifyURL, appleMusicURL, youtubeURL, instagramURL, tiktokURL, feePercentage, courtesyQuota, eventID)
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to update event: %v", err)})
			return
		}

		// Update ticket categories safely without deleting categories or historical purchase items
		categoriesJSON := r.FormValue("ticket_categories")
		if categoriesJSON != "" {
			var categoriesInput []TicketCategoryInput
			if err := json.Unmarshal([]byte(categoriesJSON), &categoriesInput); err == nil && len(categoriesInput) > 0 {
				var keptIDs []int64
				for _, cat := range categoriesInput {
					if cat.Name == "" || cat.Capacity <= 0 {
						continue
					}
					if cat.ID > 0 {
						var exists bool
						_ = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM ticket_categories WHERE id = $1 AND event_id = $2)", cat.ID, eventID).Scan(&exists)
						if exists {
							_, _ = tx.Exec(`
								UPDATE ticket_categories 
								SET name = $1, price = $2, capacity = $3, description = $4 
								WHERE id = $5 AND event_id = $6
							`, cat.Name, cat.Price, cat.Capacity, cat.Description, cat.ID, eventID)
							keptIDs = append(keptIDs, cat.ID)
							continue
						}
					}

					// Check existing by name
					var existingID int64
					err := tx.QueryRow("SELECT id FROM ticket_categories WHERE event_id = $1 AND LOWER(name) = LOWER($2)", eventID, cat.Name).Scan(&existingID)
					if err == nil && existingID > 0 {
						_, _ = tx.Exec(`
							UPDATE ticket_categories 
							SET price = $1, capacity = $2, description = $3 
							WHERE id = $4
						`, cat.Price, cat.Capacity, cat.Description, existingID)
						keptIDs = append(keptIDs, existingID)
					} else {
						var newID int64
						err := tx.QueryRow(`
							INSERT INTO ticket_categories (event_id, name, price, capacity, description)
							VALUES ($1, $2, $3, $4, $5)
							RETURNING id
						`, eventID, cat.Name, cat.Price, cat.Capacity, cat.Description).Scan(&newID)
						if err == nil {
							keptIDs = append(keptIDs, newID)
						}
					}
				}

				// Only delete categories that were omitted AND have 0 sales
				if len(keptIDs) > 0 {
					deleteQuery := `
						DELETE FROM ticket_categories 
						WHERE event_id = $1 
						  AND id NOT IN (SELECT DISTINCT ticket_category_id FROM purchase_items WHERE ticket_category_id IS NOT NULL)
					`
					for _, id := range keptIDs {
						deleteQuery += fmt.Sprintf(" AND id != %d", id)
					}
					_, _ = tx.Exec(deleteQuery, eventID)
				}
			}
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit transaction"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":   true,
			"message":   "Evento actualizado exitosamente",
			"event_id":  eventID,
			"image_url": imageURL,
		})
	}
}

// CancelEventHandler cancela un evento. RESTRICCIÓN: Solo el administrador puede cancelar eventos.
func CancelEventHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		user, err := GetAuthenticatedUser(r, db, rdb)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		_ = user.ID
		role := user.Role

		// Get event ID
		eventIDStr := r.URL.Query().Get("id")
		if eventIDStr == "" {
			eventIDStr = r.FormValue("id")
		}
		if eventIDStr == "" {
			eventIDStr = r.FormValue("event_id")
		}
		if eventIDStr == "" {
			var bodyData struct {
				ID int64 `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&bodyData)
			if bodyData.ID > 0 {
				eventIDStr = strconv.FormatInt(bodyData.ID, 10)
			}
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil || eventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or missing event id"})
			return
		}

		// Security rule: administrator or event organizer can cancel
		var organizerID int64
		errOrg := db.QueryRow("SELECT organizer_id FROM events WHERE id = $1", eventID).Scan(&organizerID)
		if errOrg != nil {
			if errOrg == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		if role != "administrador" && organizerID != user.ID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "no tienes permiso para cancelar este evento"})
			return
		}

		// Update event status to 'Cancelado'
		res, err := db.Exec("UPDATE events SET status = $1 WHERE id = $2", EventStatusCancelado, eventID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to cancel event"})
			return
		}

		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento no encontrado"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Evento cancelado exitosamente",
			"event_id": eventID,
		})
	}
}

