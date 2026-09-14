package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type RegisterCustomerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
	Phone    string `json:"phone"`
}

// RegisterCustomerHandler registra a un nuevo usuario normal (cliente/comprador)
func RegisterCustomerHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req RegisterCustomerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload JSON inválido"})
			return
		}

		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if req.Email == "" || req.Password == "" || req.FullName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "correo, contraseña y nombre completo son requeridos"})
			return
		}

		var existingID int64
		err := db.QueryRow("SELECT id FROM users WHERE email = $1", req.Email).Scan(&existingID)
		if err == nil && existingID > 0 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ya existe una cuenta registrada con este correo electrónico"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al procesar la contraseña"})
			return
		}

		tx, err := db.Begin()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al iniciar transacción"})
			return
		}
		defer tx.Rollback()

		var userID int64
		err = tx.QueryRow(`
			INSERT INTO users (email, password_hash, role)
			VALUES ($1, $2, 'cliente')
			RETURNING id
		`, req.Email, string(hash)).Scan(&userID)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("error al crear usuario: %v", err)})
			return
		}

		_, err = tx.Exec(`
			INSERT INTO customer_profiles (user_id, full_name, phone)
			VALUES ($1, $2, $3)
		`, userID, req.FullName, req.Phone)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al crear perfil de cliente"})
			return
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al confirmar registro"})
			return
		}

		// Create session
		sessionID := uuid.NewString()
		ttl := 30 * 24 * time.Hour
		_ = setSessionStore(rdb, sessionID, userID, ttl)

		isSecure := os.Getenv("ENV") == "production" || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		sameSiteMode := http.SameSiteLaxMode
		if isSecure {
			sameSiteMode = http.SameSiteNoneMode
		}

		cookie := &http.Cookie{
			Name:     "roveni_session",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecure,
			SameSite: sameSiteMode,
			MaxAge:   int(ttl.Seconds()),
		}
		http.SetCookie(w, cookie)

		writeJSON(w, http.StatusCreated, map[string]any{
			"success":    true,
			"session_id": sessionID,
			"message":    "Registro completado con éxito",
			"user": map[string]any{
				"id":        userID,
				"email":     req.Email,
				"full_name": req.FullName,
				"role":      "cliente",
			},
		})
	}
}

// LoginUserHandler autentica a cualquier usuario (cliente, organizador, administrador)
func LoginUserHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload JSON inválido"})
			return
		}

		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if req.Email == "" || req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "correo y contraseña son requeridos"})
			return
		}

		var userID int64
		var role string
		var pwHash string
		var createdAt time.Time

		err := db.QueryRow("SELECT id, role, password_hash, created_at FROM users WHERE email = $1", req.Email).Scan(&userID, &role, &pwHash, &createdAt)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "correo o contraseña incorrectos"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error en base de datos"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(req.Password)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "correo o contraseña incorrectos"})
			return
		}

		var fullName string
		var phone string
		if role == "cliente" {
			_ = db.QueryRow("SELECT full_name, COALESCE(phone, '') FROM customer_profiles WHERE user_id = $1", userID).Scan(&fullName, &phone)
		} else {
			_ = db.QueryRow("SELECT full_name, COALESCE(phone, '') FROM organizer_profiles WHERE user_id = $1", userID).Scan(&fullName, &phone)
		}
		if fullName == "" {
			fullName = req.Email
		}

		sessionID := uuid.NewString()
		ttl := 30 * 24 * time.Hour
		_ = setSessionStore(rdb, sessionID, userID, ttl)

		isSecure := os.Getenv("ENV") == "production" || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		sameSiteMode := http.SameSiteLaxMode
		if isSecure {
			sameSiteMode = http.SameSiteNoneMode
		}

		cookie := &http.Cookie{
			Name:     "roveni_session",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecure,
			SameSite: sameSiteMode,
			MaxAge:   int(ttl.Seconds()),
		}
		http.SetCookie(w, cookie)

		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"session_id": sessionID,
			"user": map[string]any{
				"id":         userID,
				"email":      req.Email,
				"full_name":  fullName,
				"phone":      phone,
				"role":       role,
				"created_at": createdAt,
			},
		})
	}
}

func GetMeHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
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

		writeJSON(w, http.StatusOK, map[string]any{
			"id":        user.ID,
			"email":     user.Email,
			"full_name": user.FullName,
			"phone":     user.Phone,
			"role":      user.Role,
		})
	}
}

// LogoutUserHandler destruye la sesión del usuario
func LogoutUserHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := extractSessionID(r)
		if sessionID != "" {
			deleteSessionStore(rdb, sessionID)
		}

		cookieClear := &http.Cookie{
			Name:     "roveni_session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		}
		http.SetCookie(w, cookieClear)

		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Sesión cerrada correctamente"})
	}
}

type UserTicketPass struct {
	PurchaseID    int64   `json:"purchase_id"`
	OrderNumber   string  `json:"order_number"`
	EventID       int64   `json:"event_id"`
	EventName     string  `json:"event_name"`
	EventImage    string  `json:"event_image"`
	EventCategory string  `json:"event_category"`
	EventDate     string  `json:"event_date"`
	ShowStartTime string  `json:"show_start_time"`
	DoorsOpenTime string  `json:"doors_open_time"`
	VenueAddress  string  `json:"venue_address"`
	City          string  `json:"city"`
	Country       string  `json:"country"`
	CustomerName  string  `json:"customer_name"`
	CustomerEmail string  `json:"customer_email"`
	TicketName    string  `json:"ticket_name"`
	TicketPrice   float64 `json:"ticket_price"`
	Currency      string  `json:"currency"`
	TicketSerial  string  `json:"ticket_serial"`
	Scanned       bool    `json:"scanned"`
	ScannedAt     string  `json:"scanned_at,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// GetMyTicketsHandler devuelve la lista de boletos/pases digitales del usuario para su Wallet
func GetMyTicketsHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var userEmail string

		// Option A: Logged-in session via Cookie, Bearer or Custom Header
		sessionID := extractSessionID(r)
		if sessionID != "" {
			userIDStr, sErr := getSessionStore(rdb, sessionID)
			if sErr == nil {
				userID, _ := strconv.ParseInt(userIDStr, 10, 64)
				_ = db.QueryRow("SELECT email FROM users WHERE id = $1", userID).Scan(&userEmail)
			}
		}

		// Option B: Query parameter fallback `?email=...`
		if userEmail == "" {
			userEmail = strings.TrimSpace(strings.ToLower(r.URL.Query().Get("email")))
		}

		if userEmail == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sesión no iniciada o email no proporcionado"})
			return
		}

		rows, err := db.Query(`
			SELECT p.id, p.order_number, p.event_id, p.customer_name, p.customer_email, p.currency, p.created_at,
			       e.name, COALESCE(e.image_url, ''), COALESCE(e.category, ''), COALESCE(e.event_date, ''),
			       COALESCE(e.doors_open_time, ''), COALESCE(e.show_start_time, ''),
			       COALESCE(e.venue_address, ''), COALESCE(e.city, ''), COALESCE(e.country, '')
			FROM purchases p
			JOIN events e ON p.event_id = e.id
			WHERE LOWER(p.customer_email) = LOWER($1) AND p.status = 'completed'
			ORDER BY p.created_at DESC
		`, userEmail)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db query error"})
			return
		}
		defer rows.Close()

		passes := []UserTicketPass{}

		for rows.Next() {
			var purID, evID int64
			var orderNum, custName, custEmail, curr, evName, evImg, evCat, evDate, doorsTime, showTime, venue, city, country string
			var createdAt time.Time

			if err := rows.Scan(&purID, &orderNum, &evID, &custName, &custEmail, &curr, &createdAt, &evName, &evImg, &evCat, &evDate, &doorsTime, &showTime, &venue, &city, &country); err == nil {
				// Query purchase items
				itemRows, itemErr := db.Query(`
					SELECT ticket_name, price, quantity 
					FROM purchase_items 
					WHERE purchase_id = $1
				`, purID)
				if itemErr == nil {
					serialIndex := 1
					for itemRows.Next() {
						var tName string
						var price float64
						var qty int
						if err := itemRows.Scan(&tName, &price, &qty); err == nil {
							for q := 0; q < qty; q++ {
								serial := fmt.Sprintf("%s-%02d", orderNum, serialIndex)

								var scanID int64
								var scannedAt time.Time
								scanErr := db.QueryRow("SELECT id, scanned_at FROM ticket_scans WHERE order_number = $1 OR ticket_serial = $2 LIMIT 1", orderNum, serial).Scan(&scanID, &scannedAt)

								scanned := scanErr == nil
								scannedAtStr := ""
								if scanned {
									scannedAtStr = scannedAt.Format("15:04:05 02/01/2006")
								}

								passes = append(passes, UserTicketPass{
									PurchaseID:    purID,
									OrderNumber:   orderNum,
									EventID:       evID,
									EventName:     evName,
									EventImage:    evImg,
									EventCategory: evCat,
									EventDate:     evDate,
									ShowStartTime: showTime,
									DoorsOpenTime: doorsTime,
									VenueAddress:  venue,
									City:          city,
									Country:       country,
									CustomerName:  custName,
									CustomerEmail: custEmail,
									TicketName:    tName,
									TicketPrice:   price,
									Currency:      curr,
									TicketSerial:  serial,
									Scanned:       scanned,
									ScannedAt:     scannedAtStr,
									CreatedAt:     createdAt.Format("2006-01-02 15:04:05"),
								})
								serialIndex++
							}
						}
					}
					itemRows.Close()
				}
			}
		}

		writeJSON(w, http.StatusOK, passes)
	}
}
