package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

var fallbackSessions sync.Map
var globalDB *sql.DB

func SetGlobalDB(db *sql.DB) {
	globalDB = db
}

func setSessionStore(rdb *redis.Client, db *sql.DB, sessionID string, userID int64, ttl time.Duration) error {
	if sessionID == "" || userID <= 0 {
		return fmt.Errorf("invalid session parameters")
	}

	userIDStr := fmt.Sprintf("%d", userID)
	fallbackSessions.Store(sessionID, userIDStr)

	if rdb != nil {
		_ = rdb.Set(context.Background(), "session:"+sessionID, userIDStr, ttl).Err()
	}

	targetDB := db
	if targetDB == nil {
		targetDB = globalDB
	}

	if targetDB != nil {
		expiresAt := time.Now().Add(ttl)
		_, err := targetDB.Exec(`
			INSERT INTO user_sessions (id, user_id, expires_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (id) DO UPDATE
			SET user_id = EXCLUDED.user_id, expires_at = EXCLUDED.expires_at
		`, sessionID, userID, expiresAt)
		if err != nil {
			fmt.Printf("[SESSION PERSISTENCE WARNING] Could not insert user_session into DB: %v\n", err)
		}
	}

	return nil
}

func extractSessionID(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if token != "" {
			return token
		}
	}
	if headerToken := r.Header.Get("X-Roveni-Session"); strings.TrimSpace(headerToken) != "" {
		return strings.TrimSpace(headerToken)
	}
	if headerToken := r.Header.Get("X-AltumPass-Session"); strings.TrimSpace(headerToken) != "" {
		return strings.TrimSpace(headerToken)
	}
	if cookie, err := r.Cookie("roveni_session"); err == nil && strings.TrimSpace(cookie.Value) != "" {
		return strings.TrimSpace(cookie.Value)
	}
	if cookie, err := r.Cookie("altumpass_session"); err == nil && strings.TrimSpace(cookie.Value) != "" {
		return strings.TrimSpace(cookie.Value)
	}
	if queryToken := r.URL.Query().Get("session_id"); strings.TrimSpace(queryToken) != "" {
		return strings.TrimSpace(queryToken)
	}
	return ""
}

func getSessionStore(rdb *redis.Client, db *sql.DB, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("empty session id")
	}

	// 1. Try Redis first
	if rdb != nil {
		val, err := rdb.Get(context.Background(), "session:"+sessionID).Result()
		if err == nil && val != "" {
			return val, nil
		}
	}

	// 2. Try RAM fallback
	val, ok := fallbackSessions.Load(sessionID)
	if ok && val != nil && val.(string) != "" {
		return val.(string), nil
	}

	// 3. Try PostgreSQL DB persistence
	targetDB := db
	if targetDB == nil {
		targetDB = globalDB
	}

	if targetDB != nil {
		var userID int64
		err := targetDB.QueryRow(`
			SELECT user_id 
			FROM user_sessions 
			WHERE id = $1 AND expires_at > NOW()
		`, sessionID).Scan(&userID)
		if err == nil && userID > 0 {
			userIDStr := fmt.Sprintf("%d", userID)
			fallbackSessions.Store(sessionID, userIDStr)
			if rdb != nil {
				_ = rdb.Set(context.Background(), "session:"+sessionID, userIDStr, 72*time.Hour).Err()
			}
			return userIDStr, nil
		}
	}

	return "", fmt.Errorf("session not found or expired")
}

func deleteSessionStore(rdb *redis.Client, db *sql.DB, sessionID string) {
	if sessionID == "" {
		return
	}
	if rdb != nil {
		_ = rdb.Del(context.Background(), "session:"+sessionID).Err()
	}
	fallbackSessions.Delete(sessionID)

	targetDB := db
	if targetDB == nil {
		targetDB = globalDB
	}

	if targetDB != nil {
		_, _ = targetDB.Exec("DELETE FROM user_sessions WHERE id = $1", sessionID)
	}
}

type AuthUser struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Phone    string `json:"phone"`
	Role     string `json:"role"`
}

// GetAuthenticatedUser extrae el token de sesión (Cookie, Bearer, X-AltumPass-Session, query param),
// consulta Redis/DB y obtiene el usuario autenticado de la base de datos.
func GetAuthenticatedUser(r *http.Request, db *sql.DB, rdb *redis.Client) (*AuthUser, error) {
	sessionID := extractSessionID(r)
	if sessionID == "" {
		return nil, fmt.Errorf("no session provided")
	}

	userIDStr, err := getSessionStore(rdb, db, sessionID)
	if err != nil || userIDStr == "" {
		return nil, fmt.Errorf("invalid or expired session")
	}

	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil || userID <= 0 {
		return nil, fmt.Errorf("invalid user id in session")
	}

	var user AuthUser
	user.ID = userID

	err = db.QueryRow(`
		SELECT 
			u.email, 
			u.role, 
			COALESCE(op.full_name, cp.full_name, u.email), 
			COALESCE(op.phone, cp.phone, '')
		FROM users u
		LEFT JOIN organizer_profiles op ON op.user_id = u.id
		LEFT JOIN customer_profiles cp ON cp.user_id = u.id
		WHERE u.id = $1
	`, userID).Scan(&user.Email, &user.Role, &user.FullName, &user.Phone)

	if err != nil {
		return nil, fmt.Errorf("user not found in database")
	}

	return &user, nil
}

type OrganizerProfile struct {
	ID             int64     `json:"id"`
	FullName       string    `json:"full_name"`
	ContactName    string    `json:"contact_name,omitempty"`
	Phone          string    `json:"phone,omitempty"`
	Email          string    `json:"email"`
	Category       string    `json:"category,omitempty"`
	City           string    `json:"city,omitempty"`
	Role           string    `json:"role"`
	TypeOrganizer  string    `json:"type_organizer,omitempty"`
	ExperienceArea string    `json:"experience_area,omitempty"`
	DocumentID     string    `json:"document_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type RegistrationInput struct {
	FullName       string `json:"full_name"`
	ContactName    string `json:"contact_name"`
	CompanyName    string `json:"company_name"`
	Phone          string `json:"phone"`
	Email          string `json:"email"`
	Category       string `json:"category"`
	City           string `json:"city"`
	Ciudad         string `json:"ciudad"`
	Password       string `json:"password"`
	ExperienceArea string `json:"experience_area"`
	DocumentID     string `json:"document_id"`
	Type           string `json:"type"`
}

func parseRegistrationInput(r *http.Request) (RegistrationInput, error) {
	var input RegistrationInput
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "multipart/form-data") || strings.Contains(contentType, "application/x-www-form-urlencoded") {
		_ = r.ParseMultipartForm(10 << 20)
		input.FullName = r.FormValue("full_name")
		input.ContactName = r.FormValue("contact_name")
		input.CompanyName = r.FormValue("company_name")
		input.Phone = r.FormValue("phone")
		input.Email = r.FormValue("email")
		input.Category = r.FormValue("category")
		input.City = r.FormValue("city")
		input.Ciudad = r.FormValue("ciudad")
		input.Password = r.FormValue("password")
		input.ExperienceArea = r.FormValue("experience_area")
		input.DocumentID = r.FormValue("document_id")
		input.Type = r.FormValue("type")

		file, header, fileErr := r.FormFile("document_file")
		if fileErr == nil && file != nil {
			defer file.Close()
			docDir := filepath.Join(".", "uploads", "documents")
			_ = os.MkdirAll(docDir, 0755)
			fileName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(header.Filename))
			filePath := filepath.Join(docDir, fileName)
			out, createErr := os.Create(filePath)
			if createErr == nil {
				defer out.Close()
				_, _ = io.Copy(out, file)
				input.DocumentID = "/uploads/documents/" + fileName
			}
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return input, fmt.Errorf("cuerpo de solicitud inválido (se esperaba JSON o FormData): %w", err)
		}
	}

	if input.FullName == "" {
		if input.ContactName != "" {
			input.FullName = input.ContactName
		} else if input.CompanyName != "" {
			input.FullName = input.CompanyName
		}
	}
	if input.City == "" && input.Ciudad != "" {
		input.City = input.Ciudad
	}

	return input, nil
}

// Helper to write JSON
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// RegisterOrganizerHandler registra organizador
func RegisterOrganizerHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		req, err := parseRegistrationInput(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		if req.Email == "" || req.Password == "" || req.FullName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debe proporcionar correo, contraseña y nombre/empresa"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password error"})
			return
		}

		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		defer tx.Rollback()

		var userID int64
		err = tx.QueryRow(
			"INSERT INTO users (email, password_hash, role, created_at) VALUES ($1, $2, $3, $4) RETURNING id",
			req.Email, string(hash), "organizador", time.Now(),
		).Scan(&userID)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "el correo electrónico ya se encuentra registrado"})
			return
		}

		_, err = tx.Exec(
			"INSERT INTO organizer_profiles (user_id, full_name, phone, email, category, city, type_organizer, document_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			userID, req.FullName, req.Phone, req.Email, req.Category, req.City, "organizador", req.DocumentID,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create profile"})
			return
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transaction commit failed"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":        userID,
			"email":     req.Email,
			"role":      "organizador",
			"full_name": req.FullName,
		})
	}
}

// LoginOrganizerHandler autentica y crea sesión
func LoginOrganizerHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credenciales inválidas o JSON malformado"})
			return
		}

		var org OrganizerProfile
		var pwHash string

		err := db.QueryRow(`
			SELECT u.id, 
				COALESCE(op.full_name, ''), 
				u.email, 
				COALESCE(op.phone, ''), 
				COALESCE(op.category, ''), 
				COALESCE(op.city, ''), 
				u.role, 
				COALESCE(op.type_organizer, 'organizador'), 
				COALESCE(op.experience_area, ''), 
				COALESCE(op.document_id, ''), 
				u.created_at, 
				u.password_hash
			FROM users u
			LEFT JOIN organizer_profiles op ON op.user_id = u.id
			WHERE u.email = $1
		`, req.Email).Scan(
			&org.ID, &org.FullName, &org.Email, &org.Phone, &org.Category, &org.City,
			&org.Role, &org.TypeOrganizer, &org.ExperienceArea, &org.DocumentID, &org.CreatedAt, &pwHash,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "correo o contraseña incorrectos"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(req.Password)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "correo o contraseña incorrectos"})
			return
		}

		// Create session with nil-safe session store
		sessionID := uuid.NewString()
		ttl := 30 * 24 * time.Hour
		_ = setSessionStore(rdb, db, sessionID, org.ID, ttl)

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
			Expires:  time.Now().Add(ttl),
		}
		http.SetCookie(w, cookie)

		org.ContactName = org.FullName
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"session_id": sessionID,
			"organizer":  org,
			"user": map[string]any{
				"id":        org.ID,
				"email":     org.Email,
				"full_name": org.FullName,
				"role":      org.Role,
			},
		})
	}
}

// SessionOrganizerHandler valida sesión y retorna usuario
func SessionOrganizerHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := extractSessionID(r)
		if sessionID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no session"})
			return
		}

		val, err := getSessionStore(rdb, db, sessionID)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid session"})
			return
		}

		var org OrganizerProfile
		var pwHash string

		err = db.QueryRow(`
			SELECT u.id, 
				COALESCE(op.full_name, cp.full_name, u.email), 
				u.email, 
				COALESCE(op.phone, cp.phone, ''), 
				COALESCE(op.category, ''), 
				COALESCE(op.city, ''), 
				u.role, 
				COALESCE(op.type_organizer, 'organizador'), 
				COALESCE(op.experience_area, ''), 
				COALESCE(op.document_id, ''), 
				u.created_at, 
				u.password_hash
			FROM users u
			LEFT JOIN organizer_profiles op ON op.user_id = u.id
			LEFT JOIN customer_profiles cp ON cp.user_id = u.id
			WHERE u.id = $1
		`, val).Scan(
			&org.ID, &org.FullName, &org.Email, &org.Phone, &org.Category, &org.City,
			&org.Role, &org.TypeOrganizer, &org.ExperienceArea, &org.DocumentID, &org.CreatedAt, &pwHash,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		org.ContactName = org.FullName
		writeJSON(w, http.StatusOK, org)
	}
}

// LogoutOrganizerHandler elimina sesión
func LogoutOrganizerHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := extractSessionID(r)
		if sessionID != "" {
			deleteSessionStore(rdb, db, sessionID)
		}
		expired := &http.Cookie{
			Name:     "roveni_session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Expires:  time.Unix(0, 0),
		}
		http.SetCookie(w, expired)
		writeJSON(w, http.StatusOK, map[string]string{"message": "Logged out"})
	}
}

// RegisterPromoterHandler registra promotor/usuario (permite autoregistro o registro desde el dashboard)
func RegisterPromoterHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		req, err := parseRegistrationInput(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		typeOrg := req.Type
		if typeOrg == "" {
			typeOrg = "promotor"
		}

		if req.Email == "" || req.Password == "" || req.FullName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debe proporcionar correo, contraseña y nombre completo"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password error"})
			return
		}

		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		defer tx.Rollback()

		var newUserID int64
		err = tx.QueryRow(
			"INSERT INTO users (email, password_hash, role, created_at) VALUES ($1, $2, $3, $4) RETURNING id",
			req.Email, string(hash), "organizador", time.Now(),
		).Scan(&newUserID)

		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "el correo electrónico ya se encuentra registrado"})
			return
		}

		_, err = tx.Exec(
			`INSERT INTO organizer_profiles 
			(user_id, full_name, phone, email, category, city, type_organizer, experience_area, document_id) 
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			newUserID, req.FullName, req.Phone, req.Email, req.Category, req.City, typeOrg, req.ExperienceArea, req.DocumentID,
		)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create profile"})
			return
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transaction commit failed"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":             newUserID,
			"email":          req.Email,
			"full_name":      req.FullName,
			"type_organizer": typeOrg,
		})
	}
}

// ListUsersHandler lista todos los usuarios (organizadores y promotores) para el dashboard
func ListUsersHandler(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		sessionID := extractSessionID(r)
		if sessionID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		userIDStr, err := getSessionStore(rdb, db, sessionID)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
			return
		}

		var role string
		if err := db.QueryRow("SELECT role FROM users WHERE id = $1", userIDStr).Scan(&role); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid user"})
			return
		}

		rows, err := db.Query(`
			SELECT u.id, 
				COALESCE(op.full_name, ''), 
				u.email, 
				COALESCE(op.phone, ''), 
				COALESCE(op.category, ''), 
				COALESCE(op.city, ''), 
				u.role, 
				COALESCE(op.type_organizer, 'organizador'), 
				COALESCE(op.experience_area, ''), 
				COALESCE(op.document_id, ''), 
				u.created_at
			FROM users u
			LEFT JOIN organizer_profiles op ON op.user_id = u.id
			WHERE u.role IN ('organizador', 'promotor') OR (op.user_id IS NOT NULL AND u.role != 'cliente')
			ORDER BY u.created_at DESC
		`)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		defer rows.Close()

		users := make([]OrganizerProfile, 0)
		for rows.Next() {
			var org OrganizerProfile
			if err := rows.Scan(
				&org.ID, &org.FullName, &org.Email, &org.Phone, &org.Category, &org.City,
				&org.Role, &org.TypeOrganizer, &org.ExperienceArea, &org.DocumentID, &org.CreatedAt,
			); err == nil {
				org.ContactName = org.FullName
				users = append(users, org)
			}
		}

		writeJSON(w, http.StatusOK, users)
	}
}
