package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TicketItem struct {
	Name     string
	Quantity int
	Price    float64
}

type EmailData struct {
	To             string
	CustomerName   string
	OrderNumber    string
	EventID        int64
	EventName      string
	EventDate      string
	ShowTime       string
	VenueAddress   string
	City           string
	Tickets        []TicketItem
	Subtotal       float64
	ServiceFee     float64
	TotalAmount    float64
	Currency       string
	PdfAttachments []string // Slice of absolute or relative filepaths to PDF tickets
}

func formatCurrency(amount float64, currency string) string {
	curr := strings.ToUpper(strings.TrimSpace(currency))
	if curr == "" {
		curr = "USD"
	}
	switch curr {
	case "MXN":
		return fmt.Sprintf("$%.2f MXN", amount)
	case "EUR":
		return fmt.Sprintf("%.2f € EUR", amount)
	case "GBP":
		return fmt.Sprintf("£%.2f GBP", amount)
	case "CAD":
		return fmt.Sprintf("$%.2f CAD", amount)
	default:
		return fmt.Sprintf("$%.2f %s", amount, curr)
	}
}

// SendPurchaseConfirmationEmail sends a confirmation email to the buyer with order, ticket details and PDF attachments.
func SendPurchaseConfirmationEmail(data EmailData) error {
	smtpFrom := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	if smtpFrom == "" {
		smtpFrom = "info@rovenisociety.com"
	}



	subject := fmt.Sprintf("¡Confirmación de Compra Roveni - Orden %s!", data.OrderNumber)

	// Build ticket rows HTML
	var ticketRows strings.Builder
	for _, t := range data.Tickets {
		ticketRows.WriteString(fmt.Sprintf(`
			<tr>
				<td style="padding: 10px 12px; border-bottom: 1px solid #2d3748; color: #e2e8f0; font-size: 14px;">%s</td>
				<td style="padding: 10px 12px; border-bottom: 1px solid #2d3748; color: #e2e8f0; font-size: 14px; text-align: center;">%d</td>
				<td style="padding: 10px 12px; border-bottom: 1px solid #2d3748; color: #5C7CFA; font-size: 14px; text-align: right; font-weight: bold;">%s</td>
			</tr>
		`, t.Name, t.Quantity, formatCurrency(t.Price*float64(t.Quantity), data.Currency)))
	}

	// Generate Calendar links & ICS file
	start, end := ParseEventTimes(data.EventDate, data.ShowTime)
	googleURL, outlookURL := BuildCalendarURLs(data.EventName, fmt.Sprintf("Tus boletos oficiales para %s (Orden #%s).", data.EventName, data.OrderNumber), data.City, data.VenueAddress, start, end)

	iCalEndpoint := fmt.Sprintf("https://rovenisociety.com/public/events/calendar.ics?id=%d", data.EventID)
	if data.EventID <= 0 {
		iCalEndpoint = "https://rovenisociety.com"
	}

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Confirmación de Compra - Roveni</title>
</head>
<body style="margin: 0; padding: 0; background-color: #0b0f19; font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif; color: #ffffff;">
    <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #0b0f19; padding: 40px 15px;">
        <tr>
            <td align="center">
                <table width="600" border="0" cellspacing="0" cellpadding="0" style="background-color: #131b2e; border-radius: 16px; border: 1px solid #232d42; overflow: hidden;">
                    <!-- Header Bar -->
                    <tr>
                        <td align="center" style="padding: 30px; background: linear-gradient(135deg, #182238 0%%, #0b0f19 100%%); border-bottom: 1px solid #232d42;">
                            <h1 style="margin: 0; color: #5C7CFA; font-size: 26px; letter-spacing: 2px; text-transform: uppercase; font-weight: 900;">ROVENI</h1>
                            <p style="margin: 5px 0 0 0; color: #94a3b8; font-size: 12px; letter-spacing: 1px; text-transform: uppercase;">El acceso a lo extraordinario</p>
                        </td>
                    </tr>

                    <!-- Success Banner -->
                    <tr>
                        <td style="padding: 30px; text-align: center;">
                            <div style="display: inline-block; background-color: rgba(31, 163, 26, 0.1); border: 1px solid rgba(31, 163, 26, 0.3); border-radius: 50px; padding: 8px 24px; margin-bottom: 16px;">
                                <span style="color: rgb(31, 163, 26); font-weight: bold; font-size: 13px; text-transform: uppercase;">✓ Emisión Verificada</span>
                            </div>
                            <h2 style="margin: 0 0 10px 0; font-size: 22px; color: #ffffff; font-weight: 800;">¡Gracias por tu compra, %s!</h2>
                            <p style="margin: 0; color: #94a3b8; font-size: 14px; line-height: 1.5;">Tus <strong>boletos oficiales</strong> han sido emitidos, en este correo los encontraras con su correspondiente código QR de seguridad para el acceso.</p>
                        </td>
                    </tr>

                    <!-- Event Info Card -->
                    <tr>
                        <td style="padding: 0 30px 20px 30px;">
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #0d1322; border-radius: 12px; border: 1px solid #1e293b; padding: 20px;">
                                <tr>
                                    <td>
                                        <h3 style="margin: 0 0 12px 0; color: #ffffff; font-size: 18px; font-weight: 800;">%s</h3>
                                        <p style="margin: 0 0 6px 0; color: #94a3b8; font-size: 13px;">📍 <strong>Lugar:</strong> %s (%s)</p>
                                        <p style="margin: 0 0 6px 0; color: #94a3b8; font-size: 13px;">📅 <strong>Fecha:</strong> %s a las %s</p>
                                        <p style="margin: 0; color: #5C7CFA; font-size: 13px;">🎟️ <strong>Código de Orden:</strong> <strong style="font-family: monospace; font-size: 15px; color: #ffffff;">%s</strong></p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Add to Calendar Block -->
                    <tr>
                        <td style="padding: 0 30px 25px 30px;">
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #0b111e; border-radius: 12px; border: 1px solid #1f293d; padding: 18px;">
                                <tr>
                                    <td align="center">
                                        <h4 style="margin: 0 0 8px 0; color: #ffffff; font-size: 13px; font-weight: 800; text-transform: uppercase; letter-spacing: 1px;">📅 Agrega este evento a tu calendario</h4>
                                        <p style="margin: 0 0 14px 0; color: #94a3b8; font-size: 12px;">Haz clic en tu calendario preferido para no olvidar la fecha:</p>
                                        <table border="0" cellspacing="0" cellpadding="0">
                                            <tr>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #4285F4; color: #ffffff; text-decoration: none; padding: 8px 14px; border-radius: 8px; font-size: 11px; font-weight: bold;">Google Calendar</a>
                                                </td>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #0078D4; color: #ffffff; text-decoration: none; padding: 8px 14px; border-radius: 8px; font-size: 11px; font-weight: bold;">Outlook Web</a>
                                                </td>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #10B981; color: #ffffff; text-decoration: none; padding: 8px 14px; border-radius: 8px; font-size: 11px; font-weight: bold;">Apple Calendar</a>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Ticket Breakdown -->
                    <tr>
                        <td style="padding: 0 30px 30px 30px;">
                            <h4 style="margin: 0 0 12px 0; color: #94a3b8; font-size: 13px; text-transform: uppercase; letter-spacing: 1px;">Desglose de Boletos</h4>
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="border-collapse: collapse;">
                                <thead>
                                    <tr style="background-color: #0d1322;">
                                        <th align="left" style="padding: 10px 12px; color: #64748b; font-size: 12px; text-transform: uppercase;">Tipo de Boleto</th>
                                        <th align="center" style="padding: 10px 12px; color: #64748b; font-size: 12px; text-transform: uppercase;">Cant.</th>
                                        <th align="right" style="padding: 10px 12px; color: #64748b; font-size: 12px; text-transform: uppercase;">Total</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    %s
                                </tbody>
                            </table>

                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="margin-top: 16px; border-t: 1px solid #232d42; pt: 16px;">
                                <tr>
                                    <td align="right" style="padding: 4px 0; color: #94a3b8; font-size: 13px;">Subtotal:</td>
                                    <td align="right" width="120" style="padding: 4px 0; color: #ffffff; font-size: 13px; font-weight: bold;">%s</td>
                                </tr>
                                <tr>
                                    <td align="right" style="padding: 4px 0; color: #94a3b8; font-size: 13px;">Cargos de Servicio:</td>
                                    <td align="right" width="120" style="padding: 4px 0; color: #ffffff; font-size: 13px; font-weight: bold;">%s</td>
                                </tr>
                                <tr>
                                    <td align="right" style="padding: 8px 0 0 0; color: #ffffff; font-size: 15px; font-weight: bold;">Total Pagado:</td>
                                    <td align="right" width="120" style="padding: 8px 0 0 0; color: #5C7CFA; font-size: 17px; font-weight: 900;">%s</td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    <!-- Footer -->
                    <tr>
                        <td align="center" style="padding: 24px; background-color: #0b0f19; border-t: 1px solid #232d42; color: #64748b; font-size: 12px; line-height: 1.6;">
                            <p style="margin: 0;">Roveni © 2024 - 2026. Todos los derechos reservados.</p>
                            <p style="margin: 4px 0 0 0;">Si tienes alguna inquietud sobre tu orden, contáctanos en <a href="https://www.instagram.com/rovenisociety/" target="_blank" style="color: #5C7CFA;">Instagram</a></p>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`,
		data.CustomerName,
		data.EventName,
		data.VenueAddress,
		data.City,
		data.EventDate,
		data.ShowTime,
		data.OrderNumber,
		googleURL,
		outlookURL,
		iCalEndpoint,
		ticketRows.String(),
		formatCurrency(data.Subtotal, data.Currency),
		formatCurrency(data.ServiceFee, data.Currency),
		formatCurrency(data.TotalAmount, data.Currency),
	)

	boundary := "---ROVENI_MIME_BOUNDARY_2026---"

	var msgBuilder strings.Builder
	msgBuilder.WriteString(fmt.Sprintf("From: %s\r\n", smtpFrom))
	msgBuilder.WriteString(fmt.Sprintf("To: %s\r\n", data.To))
	msgBuilder.WriteString(fmt.Sprintf("Bcc: %s\r\n", "evidencia@rovenisociety.com"))
	msgBuilder.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msgBuilder.WriteString("MIME-Version: 1.0\r\n")
	msgBuilder.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary))

	// HTML Body Part
	msgBuilder.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msgBuilder.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n\r\n")
	msgBuilder.WriteString(htmlBody)
	msgBuilder.WriteString("\r\n\r\n")

	// Attach .ics file directly to email
	icsContent := GenerateICSContent(
		data.EventName,
		fmt.Sprintf("Tus boletos oficiales para %s (Orden #%s).", data.EventName, data.OrderNumber),
		data.City,
		data.VenueAddress,
		data.OrderNumber,
		start,
		end,
	)
	encodedICS := base64.StdEncoding.EncodeToString([]byte(icsContent))
	icsFilename := fmt.Sprintf("evento_roveni_%s.ics", data.OrderNumber)

	msgBuilder.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msgBuilder.WriteString(fmt.Sprintf("Content-Type: text/calendar; method=REQUEST; name=\"%s\"\r\n", icsFilename))
	msgBuilder.WriteString("Content-Transfer-Encoding: base64\r\n")
	msgBuilder.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", icsFilename))
	for i := 0; i < len(encodedICS); i += 76 {
		endIdx := i + 76
		if endIdx > len(encodedICS) {
			endIdx = len(encodedICS)
		}
		msgBuilder.WriteString(encodedICS[i:endIdx] + "\r\n")
	}
	msgBuilder.WriteString("\r\n")

	// PDF Attachments Parts
	for _, pdfFile := range data.PdfAttachments {
		fileBytes, err := os.ReadFile(pdfFile)
		if err != nil {
			log.Printf("[SMTP ERROR] Could not read PDF file for attachment %s: %v", pdfFile, err)
			continue
		}

		filename := filepath.Base(pdfFile)
		encoded := base64.StdEncoding.EncodeToString(fileBytes)

		msgBuilder.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msgBuilder.WriteString(fmt.Sprintf("Content-Type: application/pdf; name=\"%s\"\r\n", filename))
		msgBuilder.WriteString("Content-Transfer-Encoding: base64\r\n")
		msgBuilder.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", filename))

		// Write base64 string in 76-character chunks as required by MIME standards
		for i := 0; i < len(encoded); i += 76 {
			endIdx := i + 76
			if endIdx > len(encoded) {
				endIdx = len(encoded)
			}
			msgBuilder.WriteString(encoded[i:endIdx] + "\r\n")
		}
		msgBuilder.WriteString("\r\n")
	}

	msgBuilder.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	msg := []byte(msgBuilder.String())

	// Normalize and clean environment configuration
	smtpHostRaw := os.Getenv("SMTP_HOST")
	smtpPortRaw := os.Getenv("SMTP_PORT")
	smtpUsername := strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	smtpPassword := strings.TrimSpace(os.Getenv("SMTP_PASSWORD"))
	smtpFrom = strings.TrimSpace(os.Getenv("SMTP_FROM"))

	hostClean, extractedPort := cleanSMTPHost(smtpHostRaw)
	if hostClean == "" {
		hostClean = "smtp.hostinger.com"
	}

	smtpPort := strings.TrimSpace(smtpPortRaw)
	if smtpPort == "" {
		if extractedPort != "" {
			smtpPort = extractedPort
		} else {
			smtpPort = "465"
		}
	}
	if smtpFrom == "" {
		smtpFrom = "info@rovenisociety.com"
	}
	if smtpUsername == "" {
		smtpUsername = smtpFrom
	}

	recipients := []string{data.To}
	bccEmail := strings.TrimSpace(os.Getenv("SMTP_BCC"))
	if bccEmail == "" {
		bccEmail = "evidencia@rovenisociety.com"
	}
	if bccEmail != "" && bccEmail != data.To {
		recipients = append(recipients, bccEmail)
	}

	log.Printf("[SMTP INFO] Attempting email delivery via %s:%s (From: %s, To: %v)...", hostClean, smtpPort, smtpFrom, recipients)
	err := sendMailViaPort(hostClean, smtpPort, smtpUsername, smtpPassword, smtpFrom, recipients, msg)
	if err != nil {
		log.Printf("[SMTP WARNING] Primary delivery via %s:%s failed: %v", hostClean, smtpPort, err)

		// Automatic dual-port fallback (if 465 fails, try 587; if 587 fails, try 465)
		fallbackPort := "587"
		if smtpPort == "587" || smtpPort == "25" {
			fallbackPort = "465"
		}
		log.Printf("[SMTP INFO] Retrying via fallback port %s...", fallbackPort)
		fallbackErr := sendMailViaPort(hostClean, fallbackPort, smtpUsername, smtpPassword, smtpFrom, recipients, msg)
		if fallbackErr != nil {
			log.Printf("[SMTP ERROR] Both primary (%s:%s) and fallback (%s:%s) SMTP attempts failed. Primary err: %v | Fallback err: %v", hostClean, smtpPort, hostClean, fallbackPort, err, fallbackErr)
			return fmt.Errorf("smtp delivery failed (primary: %v, fallback: %v)", err, fallbackErr)
		}
		log.Printf("[SMTP SUCCESS] Confirmation email successfully delivered via fallback port %s to %s for Order %s!", fallbackPort, data.To, data.OrderNumber)
		return nil
	}

	log.Printf("[SMTP SUCCESS] Confirmation email with %d PDF attachments successfully sent via %s:%s to %s for Order %s!", len(data.PdfAttachments), hostClean, smtpPort, data.To, data.OrderNumber)
	return nil
}

func cleanSMTPHost(rawHost string) (host string, port string) {
	h := strings.TrimSpace(rawHost)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	if strings.Contains(h, ":") {
		parts := strings.Split(h, ":")
		h = parts[0]
		if len(parts) > 1 && parts[1] != "" {
			port = parts[1]
		}
	}
	return h, port
}

func sendMailViaPort(host, port, username, password, from string, recipients []string, msg []byte) error {
	addr := net.JoinHostPort(host, port)
	var auth smtp.Auth
	if username != "" && password != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         host,
	}

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}

	if port == "465" {
		// Implicit SSL / TLS connection
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("tls dial to %s failed: %w", addr, err)
		}
		defer conn.Close()

		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("smtp new client failed: %w", err)
		}
		defer c.Close()

		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth failed: %w", err)
			}
		}

		if err := c.Mail(from); err != nil {
			return fmt.Errorf("smtp mail from failed: %w", err)
		}
		for _, rcpt := range recipients {
			if err := c.Rcpt(rcpt); err != nil {
				log.Printf("[SMTP WARNING] Rcpt TO failed for %s: %v", rcpt, err)
			}
		}

		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("smtp data failed: %w", err)
		}
		if _, err := w.Write(msg); err != nil {
			w.Close()
			return fmt.Errorf("smtp write message failed: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("smtp close data writer failed: %w", err)
		}
		_ = c.Quit()
		return nil
	} else {
		// Explicit STARTTLS connection (Port 587 or 25)
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("dial to %s failed: %w", addr, err)
		}
		defer conn.Close()

		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("smtp new client failed: %w", err)
		}
		defer c.Close()

		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("starttls failed: %w", err)
			}
		}

		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth failed: %w", err)
			}
		}

		if err := c.Mail(from); err != nil {
			return fmt.Errorf("smtp mail from failed: %w", err)
		}
		for _, rcpt := range recipients {
			if err := c.Rcpt(rcpt); err != nil {
				log.Printf("[SMTP WARNING] Rcpt TO failed for %s: %v", rcpt, err)
			}
		}

		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("smtp data failed: %w", err)
		}
		if _, err := w.Write(msg); err != nil {
			w.Close()
			return fmt.Errorf("smtp write message failed: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("smtp close data writer failed: %w", err)
		}
		_ = c.Quit()
		return nil
	}
}
