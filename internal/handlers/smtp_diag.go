package handlers

import (
	"fmt"
	"net/http"
	"os"

	"roveni/internal/mailer"
)

// TestSMTPHandler permite probar el envío de correos SMTP y diagnosticar errores de conexión.
func TestSMTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		toEmail := r.URL.Query().Get("to")
		if toEmail == "" {
			toEmail = os.Getenv("SMTP_BCC")
			if toEmail == "" {
				toEmail = "evidencia@rovenisociety.com"
			}
		}

		testData := mailer.EmailData{
			To:           toEmail,
			CustomerName: "Prueba Sistema Roveni",
			OrderNumber:  "RV-000001",
			EventName:    "Evento de Prueba SMTP",
			EventDate:    "2026-08-30",
			ShowTime:     "20:00",
			VenueAddress: "Foro Principal",
			City:         "Ciudad de México",
			Tickets: []mailer.TicketItem{
				{Name: "General Prueba", Quantity: 1, Price: 100},
			},
			Subtotal:    100,
			ServiceFee:  20,
			TotalAmount: 120,
			Currency:    "MXN",
		}

		err := mailer.SendPurchaseConfirmationEmail(testData)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":    "error",
				"message":   fmt.Sprintf("Error enviando correo SMTP: %v", err),
				"smtp_host": os.Getenv("SMTP_HOST"),
				"smtp_port": os.Getenv("SMTP_PORT"),
				"smtp_user": os.Getenv("SMTP_USERNAME"),
				"recipient": toEmail,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "success",
			"message":   fmt.Sprintf("Correo de prueba SMTP enviado exitosamente a %s", toEmail),
			"smtp_host": os.Getenv("SMTP_HOST"),
			"smtp_port": os.Getenv("SMTP_PORT"),
			"recipient": toEmail,
		})
	}
}
