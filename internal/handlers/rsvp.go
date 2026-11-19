package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RSVPRequest struct {
	EventID     int64  `json:"event_id"`
	OrderNumber string `json:"order_number"`
	Email       string `json:"email"`
	Status      string `json:"rsvp_status"` // "confirmed" or "declined"
	Notes       string `json:"notes"`
}

type RSVPResponse struct {
	Success      bool       `json:"success"`
	Message      string     `json:"message"`
	OrderNumber  string     `json:"order_number"`
	CustomerName string     `json:"customer_name"`
	RSVPStatus   string     `json:"rsvp_status"`
	RSVPAt       *time.Time `json:"rsvp_at,omitempty"`
}

// RSVPHandler procesa la confirmación o declinación de asistencia a un evento por parte de un invitado o comprador.
func RSVPHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req RSVPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cuerpo de solicitud inválido"})
			return
		}

		if req.EventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_id es requerido"})
			return
		}

		orderNum := strings.TrimSpace(req.OrderNumber)
		email := strings.ToLower(strings.TrimSpace(req.Email))

		if orderNum == "" && email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debe proporcionar número de orden o correo electrónico"})
			return
		}

		status := strings.ToLower(strings.TrimSpace(req.Status))
		if status != "confirmed" && status != "declined" {
			status = "confirmed"
		}

		// Buscar la compra / invitación oficial correspondiente
		var purchaseID int64
		var currentOrderNum, customerName, customerEmail string
		var queryErr error

		if orderNum != "" && email != "" {
			queryErr = db.QueryRow(`
				SELECT id, order_number, customer_name, customer_email 
				FROM purchases 
				WHERE event_id = $1 AND status = 'completed' 
				  AND (order_number = $2 OR LOWER(customer_email) = $3)
				ORDER BY (order_number = $2) DESC, created_at DESC 
				LIMIT 1
			`, req.EventID, orderNum, email).Scan(&purchaseID, &currentOrderNum, &customerName, &customerEmail)
		} else if orderNum != "" {
			queryErr = db.QueryRow(`
				SELECT id, order_number, customer_name, customer_email 
				FROM purchases 
				WHERE event_id = $1 AND status = 'completed' AND order_number = $2
				LIMIT 1
			`, req.EventID, orderNum).Scan(&purchaseID, &currentOrderNum, &customerName, &customerEmail)
		} else {
			queryErr = db.QueryRow(`
				SELECT id, order_number, customer_name, customer_email 
				FROM purchases 
				WHERE event_id = $1 AND status = 'completed' AND LOWER(customer_email) = $2
				ORDER BY created_at DESC 
				LIMIT 1
			`, req.EventID, email).Scan(&purchaseID, &currentOrderNum, &customerName, &customerEmail)
		}

		if queryErr != nil {
			if queryErr == sql.ErrNoRows {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "No se encontró una invitación o boleto oficial con esos datos para este evento."})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error de base de datos al validar la invitación"})
			return
		}

		now := time.Now().UTC()
		notes := strings.TrimSpace(req.Notes)

		_, updateErr := db.Exec(`
			UPDATE purchases 
			SET rsvp_status = $1, 
			    rsvp_at = $2, 
			    rsvp_notes = CASE WHEN $3 != '' THEN $3 ELSE rsvp_notes END
			WHERE id = $4
		`, status, now, notes, purchaseID)

		if updateErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error al actualizar la confirmación de asistencia"})
			return
		}

		successMsg := "Asistencia confirmada exitosamente."
		if status == "declined" {
			successMsg = "Se ha registrado que no podrá asistir al evento."
		}

		writeJSON(w, http.StatusOK, RSVPResponse{
			Success:      true,
			Message:      successMsg,
			OrderNumber:  currentOrderNum,
			CustomerName: customerName,
			RSVPStatus:   status,
			RSVPAt:       &now,
		})
	}
}

// RSVPStatusHandler permite consultar el estatus actual de confirmación de un invitado.
func RSVPStatusHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		eventIDStr := r.URL.Query().Get("event_id")
		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil || eventID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_id inválido"})
			return
		}

		orderNum := strings.TrimSpace(r.URL.Query().Get("order"))
		if orderNum == "" {
			orderNum = strings.TrimSpace(r.URL.Query().Get("order_number"))
		}
		email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))

		if orderNum == "" && email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debe proporcionar order o email"})
			return
		}

		var purchaseID int64
		var foundOrderNum, customerName, customerEmail, rsvpStatus string
		var rsvpAt sql.NullTime
		var isCourtesy bool

		var queryErr error
		if orderNum != "" {
			queryErr = db.QueryRow(`
				SELECT id, order_number, customer_name, customer_email, 
				       COALESCE(rsvp_status, 'pending'), rsvp_at, is_courtesy
				FROM purchases 
				WHERE event_id = $1 AND status = 'completed' AND order_number = $2
				LIMIT 1
			`, eventID, orderNum).Scan(&purchaseID, &foundOrderNum, &customerName, &customerEmail, &rsvpStatus, &rsvpAt, &isCourtesy)
		} else {
			queryErr = db.QueryRow(`
				SELECT id, order_number, customer_name, customer_email, 
				       COALESCE(rsvp_status, 'pending'), rsvp_at, is_courtesy
				FROM purchases 
				WHERE event_id = $1 AND status = 'completed' AND LOWER(customer_email) = $2
				ORDER BY created_at DESC 
				LIMIT 1
			`, eventID, email).Scan(&purchaseID, &foundOrderNum, &customerName, &customerEmail, &rsvpStatus, &rsvpAt, &isCourtesy)
		}

		if queryErr != nil {
			if queryErr == sql.ErrNoRows {
				writeJSON(w, http.StatusOK, map[string]any{
					"found": false,
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error de base de datos"})
			return
		}

		var rsvpAtPtr *time.Time
		if rsvpAt.Valid {
			rsvpAtPtr = &rsvpAt.Time
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"found":          true,
			"order_number":   foundOrderNum,
			"customer_name":  customerName,
			"customer_email": customerEmail,
			"rsvp_status":    rsvpStatus,
			"rsvp_at":        rsvpAtPtr,
			"is_courtesy":    isCourtesy,
		})
	}
}
