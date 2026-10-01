package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func enableCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	} else {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE, PATCH, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "*")
}

type SeatResponse struct {
	ID           int64      `json:"id"`
	RowLabel     string     `json:"row_label"`
	SeatNumber   int        `json:"seat_number"`
	Block        string     `json:"block"`
	IsWheelchair bool       `json:"is_wheelchair"`
	Status       string     `json:"status"` // "available", "held", "sold"
	IsMine       bool       `json:"is_mine"`
	LockedUntil  *time.Time `json:"locked_until,omitempty"`
}

type GetSeatsResponse struct {
	EventID             int64          `json:"event_id"`
	VenueName           string         `json:"venue_name"`
	HoldDurationSeconds int            `json:"hold_duration_seconds"`
	Seats               []SeatResponse `json:"seats"`
}

type HoldSeatsRequest struct {
	EventID   int64   `json:"event_id"`
	SeatIDs   []int64 `json:"seat_ids"`
	SessionID string  `json:"session_id"`
}

type HoldSeatsResponse struct {
	Success          bool      `json:"success"`
	Message          string    `json:"message"`
	HeldUntil        time.Time `json:"held_until"`
	ExpiresInSeconds int       `json:"expires_in_seconds"`
	HeldSeatIDs      []int64   `json:"held_seat_ids"`
}

type ReleaseSeatsRequest struct {
	EventID   int64   `json:"event_id"`
	SeatIDs   []int64 `json:"seat_ids"`
	SessionID string  `json:"session_id"`
}

// GetEventSeatsHandler handles GET /public/events/seats?event_id=123&session_id=abc
func GetEventSeatsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enableCORS(w, r)
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		eventIDStr := r.URL.Query().Get("event_id")
		if eventIDStr == "" {
			http.Error(w, `{"error": "Parámetro event_id requerido"}`, http.StatusBadRequest)
			return
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil {
			http.Error(w, `{"error": "event_id inválido"}`, http.StatusBadRequest)
			return
		}

		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))

		// Auto-release expired holds for this event
		_, _ = db.Exec(`
			UPDATE event_seats 
			SET status = 'available', session_id = '', locked_until = NULL 
			WHERE event_id = $1 AND status = 'held' AND locked_until < NOW()
		`, eventID)

		query := `
			SELECT 
				s.id, s.row_label, s.seat_number, s.block, s.is_wheelchair,
				COALESCE(es.status, 'available') as status,
				COALESCE(es.session_id, '') as session_id,
				es.locked_until
			FROM seats s
			LEFT JOIN event_seats es ON es.seat_id = s.id AND es.event_id = $1
			WHERE s.venue_name = 'Auditorio Charles Chaplin'
			ORDER BY s.row_label ASC, s.seat_number ASC
		`

		rows, err := db.Query(query, eventID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "Error al consultar asientos: %v"}`, err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var seatsList []SeatResponse
		now := time.Now()

		for rows.Next() {
			var s SeatResponse
			var dbSessionID string
			var lockedUntil sql.NullTime

			if err := rows.Scan(&s.ID, &s.RowLabel, &s.SeatNumber, &s.Block, &s.IsWheelchair, &s.Status, &dbSessionID, &lockedUntil); err != nil {
				continue
			}

			if lockedUntil.Valid {
				t := lockedUntil.Time
				s.LockedUntil = &t
			}

			// Clean up expired status in response
			if s.Status == "held" {
				if s.LockedUntil != nil && s.LockedUntil.Before(now) {
					s.Status = "available"
					s.IsMine = false
				} else if sessionID != "" && dbSessionID == sessionID {
					s.IsMine = true
				}
			}

			seatsList = append(seatsList, s)
		}

		resp := GetSeatsResponse{
			EventID:             eventID,
			VenueName:           "Auditorio Charles Chaplin",
			HoldDurationSeconds: 300, // 5 minutes
			Seats:               seatsList,
		}

		json.NewEncoder(w).Encode(resp)
	}
}

// HoldSeatsHandler handles POST /public/checkout/hold-seats (5-minute lock)
func HoldSeatsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enableCORS(w, r)
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			http.Error(w, `{"error": "Método no permitido"}`, http.StatusMethodNotAllowed)
			return
		}

		var req HoldSeatsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "Cuerpo de solicitud inválido"}`, http.StatusBadRequest)
			return
		}

		if req.EventID <= 0 || req.SessionID == "" {
			http.Error(w, `{"error": "event_id y session_id son requeridos"}`, http.StatusBadRequest)
			return
		}

		tx, err := db.Begin()
		if err != nil {
			http.Error(w, `{"error": "Error al iniciar transacción"}`, http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		// 1. Auto-release expired holds
		_, _ = tx.Exec(`
			UPDATE event_seats 
			SET status = 'available', session_id = '', locked_until = NULL 
			WHERE event_id = $1 AND status = 'held' AND locked_until < NOW()
		`, req.EventID)

		// 2. Check conflict for requested seats
		if len(req.SeatIDs) > 0 {
			placeholders := make([]string, len(req.SeatIDs))
			args := make([]interface{}, len(req.SeatIDs)+2)
			args[0] = req.EventID
			args[1] = req.SessionID
			for i, id := range req.SeatIDs {
				placeholders[i] = fmt.Sprintf("$%d", i+3)
				args[i+2] = id
			}

			conflictQuery := fmt.Sprintf(`
				SELECT COUNT(*) 
				FROM event_seats 
				WHERE event_id = $1 
				  AND seat_id IN (%s) 
				  AND (
					status = 'sold' 
					OR (status = 'held' AND session_id != $2 AND locked_until > NOW())
				  )
			`, strings.Join(placeholders, ","))

			var conflictCount int
			if err := tx.QueryRow(conflictQuery, args...).Scan(&conflictCount); err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "Error al verificar disponibilidad: %v"}`, err), http.StatusInternalServerError)
				return
			}

			if conflictCount > 0 {
				http.Error(w, `{"error": "Uno o más asientos seleccionados ya no están disponibles. Han sido tomados por otro usuario."}`, http.StatusConflict)
				return
			}
		}

		// 3. Release any seats previously held by this session that are NOT in current selection
		if len(req.SeatIDs) == 0 {
			_, _ = tx.Exec(`
				UPDATE event_seats 
				SET status = 'available', session_id = '', locked_until = NULL 
				WHERE event_id = $1 AND session_id = $2 AND status = 'held'
			`, req.EventID, req.SessionID)
		} else {
			placeholders := make([]string, len(req.SeatIDs))
			args := make([]interface{}, len(req.SeatIDs)+2)
			args[0] = req.EventID
			args[1] = req.SessionID
			for i, id := range req.SeatIDs {
				placeholders[i] = fmt.Sprintf("$%d", i+3)
				args[i+2] = id
			}

			releaseUnselectedQuery := fmt.Sprintf(`
				UPDATE event_seats 
				SET status = 'available', session_id = '', locked_until = NULL 
				WHERE event_id = $1 
				  AND session_id = $2 
				  AND status = 'held' 
				  AND seat_id NOT IN (%s)
			`, strings.Join(placeholders, ","))

			_, _ = tx.Exec(releaseUnselectedQuery, args...)
		}

		// 4. Lock requested seats for 5 minutes (300 seconds)
		lockUntil := time.Now().Add(5 * time.Minute)

		for _, seatID := range req.SeatIDs {
			_, err := tx.Exec(`
				INSERT INTO event_seats (event_id, seat_id, status, locked_until, session_id)
				VALUES ($1, $2, 'held', $3, $4)
				ON CONFLICT (event_id, seat_id) DO UPDATE 
				SET status = 'held', locked_until = EXCLUDED.locked_until, session_id = EXCLUDED.session_id
			`, req.EventID, seatID, lockUntil, req.SessionID)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "Error al reservar asiento ID %d: %v"}`, seatID, err), http.StatusInternalServerError)
				return
			}
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, `{"error": "Error al confirmar reserva"}`, http.StatusInternalServerError)
			return
		}

		resp := HoldSeatsResponse{
			Success:          true,
			Message:          "Asientos retenidos exitosamente por 5 minutos",
			HeldUntil:        lockUntil,
			ExpiresInSeconds: 300,
			HeldSeatIDs:      req.SeatIDs,
		}

		json.NewEncoder(w).Encode(resp)
	}
}

// ReleaseSeatsHandler handles POST /public/checkout/release-seats
func ReleaseSeatsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enableCORS(w, r)
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			http.Error(w, `{"error": "Método no permitido"}`, http.StatusMethodNotAllowed)
			return
		}

		var req ReleaseSeatsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "Cuerpo de solicitud inválido"}`, http.StatusBadRequest)
			return
		}

		if req.EventID <= 0 || req.SessionID == "" {
			http.Error(w, `{"error": "event_id y session_id son requeridos"}`, http.StatusBadRequest)
			return
		}

		if len(req.SeatIDs) == 0 {
			_, _ = db.Exec(`
				UPDATE event_seats 
				SET status = 'available', session_id = '', locked_until = NULL 
				WHERE event_id = $1 AND session_id = $2 AND status = 'held'
			`, req.EventID, req.SessionID)
		} else {
			placeholders := make([]string, len(req.SeatIDs))
			args := make([]interface{}, len(req.SeatIDs)+2)
			args[0] = req.EventID
			args[1] = req.SessionID
			for i, id := range req.SeatIDs {
				placeholders[i] = fmt.Sprintf("$%d", i+3)
				args[i+2] = id
			}

			query := fmt.Sprintf(`
				UPDATE event_seats 
				SET status = 'available', session_id = '', locked_until = NULL 
				WHERE event_id = $1 
				  AND session_id = $2 
				  AND status = 'held' 
				  AND seat_id IN (%s)
			`, strings.Join(placeholders, ","))

			_, _ = db.Exec(query, args...)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"message": "Asientos liberados exitosamente",
		})
	}
}
