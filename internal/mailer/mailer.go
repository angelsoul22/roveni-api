package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
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
	SenderName     string // Nombre del remitente (ej. "Roveni", "Roveni Society")
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

	// Classification & Private Event Information
	IsCourtesy  bool
	IsPrivate   bool
	PrivateType string
	AccessCode  string
	EventURL    string
}

func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	t := strings.Map(func(r rune) rune {
		switch r {
		case 'á', 'à', 'ä', 'â':
			return 'a'
		case 'é', 'è', 'ë', 'ê':
			return 'e'
		case 'í', 'ì', 'ï', 'î':
			return 'i'
		case 'ó', 'ò', 'ö', 'ô':
			return 'o'
		case 'ú', 'ù', 'ü', 'û':
			return 'u'
		case 'ñ':
			return 'n'
		default:
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			if r == ' ' || r == '-' || r == '_' {
				return '-'
			}
			return -1
		}
	}, text)

	for strings.Contains(t, "--") {
		t = strings.ReplaceAll(t, "--", "-")
	}
	return strings.Trim(t, "-")
}

func BuildCleanEventURL(eventID int64, eventName string) string {
	baseURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("NEXT_PUBLIC_SITE_URL"), "/")
	}
	if baseURL == "" {
		baseURL = "https://rovenisociety.com"
	}
	slug := slugify(eventName)
	if slug != "" {
		return fmt.Sprintf("%s/eventos/%d_%s", baseURL, eventID, slug)
	}
	return fmt.Sprintf("%s/eventos/%d", baseURL, eventID)
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
// BuildConfirmationEmailHTML generates the subject and HTML body according to Roveni Society luxury aesthetic.
func BuildConfirmationEmailHTML(data EmailData) (string, string) {
	isInstitutional := data.IsPrivate && strings.ToLower(strings.TrimSpace(data.PrivateType)) == "solo_lectura"
	isCourtesy := data.IsCourtesy && !isInstitutional

	subject := fmt.Sprintf("Boletos Confirmados · %s (Orden #%s)", data.EventName, data.OrderNumber)
	if isInstitutional {
		subject = fmt.Sprintf("Invitación Oficial · %s (Acreditación #%s)", data.EventName, data.OrderNumber)
	} else if isCourtesy {
		subject = fmt.Sprintf("Acceso Especial Confirmado · %s (Orden #%s)", data.EventName, data.OrderNumber)
	} else if data.IsPrivate {
		subject = fmt.Sprintf("Acceso Privado Confirmado · %s (Orden #%s)", data.EventName, data.OrderNumber)
	}

	bannerBadgeHTML := `<div style="display: inline-block; background-color: #d0c0ad; color: #0a0b0e; border: 1px solid #d0c0ad; border-radius: 4px; padding: 6px 18px; margin-bottom: 18px;">
                                <span style="color: #0a0b0e; font-weight: bold; font-size: 11px; text-transform: uppercase; letter-spacing: 2px;">✦ ACCESO CONFIRMADO · ORDEN OFICIAL</span>
                            </div>`
	bannerTitle := fmt.Sprintf("Bienvenido(a), %s", data.CustomerName)
	bannerSubtitle := "Tu compra ha sido procesada y confirmada con éxito. Hemos generado tus boletos digitales con firma criptográfica y código QR de alta seguridad. Tus archivos PDF oficiales se encuentran adjuntos a este correo para tu acceso al recinto."

	if isInstitutional {
		bannerBadgeHTML = `<div style="display: inline-block; background-color: #d0c0ad; color: #0a0b0e; border: 1px solid #d0c0ad; border-radius: 4px; padding: 6px 18px; margin-bottom: 18px;">
                                <span style="color: #0a0b0e; font-weight: bold; font-size: 11px; text-transform: uppercase; letter-spacing: 2px;">INVITACIÓN OFICIAL</span>
                            </div>`
		bannerTitle = fmt.Sprintf("Distinguido(a) %s,", data.CustomerName)
		bannerSubtitle = "Es un honor confirmarle que ha sido formalmente acreditado(a) para asistir a este acto solemne. Su invitación oficial digital con código QR de acceso personal e intransferible se encuentra adjunta en formato PDF al presente correo para su recepción y control de acceso."
	} else if isCourtesy {
		bannerBadgeHTML = `<div style="display: inline-block; background-color: #43091f; color: #f5efe6; border: 1px solid #43091f; border-radius: 4px; padding: 6px 18px; margin-bottom: 18px;">
                                <span style="color: #f5efe6; font-weight: bold; font-size: 11px; text-transform: uppercase; letter-spacing: 2px;">🎟️ ACCESO ESPECIAL · CORTESÍA AUTORIZADA</span>
                            </div>`
		bannerTitle = fmt.Sprintf("¡Tu acceso especial está listo, %s!", data.CustomerName)
		bannerSubtitle = "Se ha emitido tu pase de cortesía oficial con todas las facilidades de acceso. Encontrarás tus boletos digitales adjuntos en formato PDF con su correspondiente código QR personal listos para su validación en el acceso."
	} else if data.IsPrivate {
		bannerBadgeHTML = `<div style="display: inline-block; background-color: #d0c0ad; color: #0a0b0e; border: 1px solid #d0c0ad; border-radius: 4px; padding: 6px 18px; margin-bottom: 18px;">
                                <span style="color: #0a0b0e; font-weight: bold; font-size: 11px; text-transform: uppercase; letter-spacing: 2px;">🔒 EVENTO PRIVADO · ACCESO EXCLUSIVO</span>
                            </div>`
		bannerTitle = fmt.Sprintf("Acceso Exclusivo Confirmado, %s", data.CustomerName)
		bannerSubtitle = "Tus boletos oficiales para este evento privado exclusivo han sido emitidos con éxito. Encontrarás tus pases adjuntos en formato PDF con código QR individual para tu ingreso al recinto."
	}

	// Build ticket breakdown block (omitted entirely for institutional solo_lectura events)
	var breakdownBlock string
	if !isInstitutional {
		var ticketRows strings.Builder
		for _, t := range data.Tickets {
			priceStr := formatCurrency(t.Price*float64(t.Quantity), data.Currency)
			if isCourtesy {
				priceStr = "Cortesía ($0.00)"
			}
			ticketRows.WriteString(fmt.Sprintf(`
			<tr>
				<td style="padding: 12px 14px; border-bottom: 1px solid rgba(10, 11, 14, 0.08); color: #0a0b0e; font-size: 13px; font-weight: 500;">%s</td>
				<td style="padding: 12px 14px; border-bottom: 1px solid rgba(10, 11, 14, 0.08); color: #0a0b0e; font-size: 13px; text-align: center; font-weight: 700;">%d</td>
				<td style="padding: 12px 14px; border-bottom: 1px solid rgba(10, 11, 14, 0.08); color: #0a0b0e; font-size: 13px; text-align: right; font-weight: 700;">%s</td>
			</tr>
		`, t.Name, t.Quantity, priceStr))
		}

		var totalsHTML string
		if isCourtesy {
			totalsHTML = `
            <table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-top: 16px; border-top: 1px solid rgba(10, 11, 14, 0.15); padding-top: 14px;">
                <tr>
                    <td align="left" style="padding: 4px 0; color: #7a7065; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600;">Modalidad de Ingreso:</td>
                    <td align="right" style="padding: 4px 0; color: #0a0b0e; font-size: 13px; font-weight: bold;">Pase de Cortesía Autorizado</td>
                </tr>
                <tr>
                    <td align="left" style="padding: 4px 0; color: #7a7065; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600;">Total Facturado:</td>
                    <td align="right" style="padding: 4px 0; color: #43091f; font-size: 13px; font-weight: bold;">$0.00 (Acceso Especial)</td>
                </tr>
            </table>`
		} else {
			totalsHTML = fmt.Sprintf(`
            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="margin-top: 16px; border-top: 1px solid rgba(10, 11, 14, 0.15); padding-top: 16px;">
                <tr>
                    <td align="right" style="padding: 4px 0; color: #7a7065; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600;">Subtotal:</td>
                    <td align="right" width="130" style="padding: 4px 0; color: #0a0b0e; font-size: 13px; font-weight: 600;">%s</td>
                </tr>
                <tr>
                    <td align="right" style="padding: 4px 0; color: #7a7065; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600;">Cargos de Servicio:</td>
                    <td align="right" width="130" style="padding: 4px 0; color: #0a0b0e; font-size: 13px; font-weight: 600;">%s</td>
                </tr>
                <tr>
                    <td align="right" style="padding: 10px 0 0 0; color: #0a0b0e; font-size: 14px; font-weight: bold; text-transform: uppercase; letter-spacing: 1px;">Total Pagado:</td>
                    <td align="right" width="130" style="padding: 10px 0 0 0; color: #0a0b0e; font-size: 19px; font-weight: 900;">%s</td>
                </tr>
            </table>`,
				formatCurrency(data.Subtotal, data.Currency),
				formatCurrency(data.ServiceFee, data.Currency),
				formatCurrency(data.TotalAmount, data.Currency),
			)
		}

		breakdownBlock = fmt.Sprintf(`
                    <!-- Ticket Breakdown -->
                    <tr>
                        <td style="padding: 0 30px 30px 30px;">
                            <div style="margin: 0 0 12px 0; color: #7a7065; font-size: 11px; text-transform: uppercase; letter-spacing: 1.5px; font-weight: 700;">Desglose de Accesos</div>
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="border-collapse: collapse; border: 1px solid rgba(10, 11, 14, 0.14); border-radius: 6px; overflow: hidden;">
                                <thead>
                                    <tr style="background-color: #d0c0ad; border-bottom: 1px solid rgba(10, 11, 14, 0.18);">
                                        <th align="left" style="padding: 10px 14px; color: #0a0b0e; font-size: 10.5px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700;">Concepto</th>
                                        <th align="center" style="padding: 10px 14px; color: #0a0b0e; font-size: 10.5px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700;">Cant.</th>
                                        <th align="right" style="padding: 10px 14px; color: #0a0b0e; font-size: 10.5px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700;">Total</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    %s
                                </tbody>
                            </table>

                            %s
                        </td>
                    </tr>`, ticketRows.String(), totalsHTML)
	}

	// Build private event information block (Informative without pre-loaded key button)
	var privateEventBlock string
	if data.IsPrivate {
		eventLink := data.EventURL
		if eventLink == "" && data.EventID > 0 {
			eventLink = BuildCleanEventURL(data.EventID, data.EventName)
		}
		if eventLink != "" && data.OrderNumber != "" && !strings.Contains(eventLink, "order=") {
			sep := "?"
			if strings.Contains(eventLink, "?") {
				sep = "&"
			}
			eventLink = fmt.Sprintf("%s%sorder=%s", eventLink, sep, url.QueryEscape(data.OrderNumber))
		}

		var codeRow string
		if strings.TrimSpace(data.AccessCode) != "" {
			codeRow = fmt.Sprintf(`
                <tr>
                    <td style="padding: 12px 0 4px 0;">
                        <span style="color: #7a7065; font-size: 10.5px; text-transform: uppercase; letter-spacing: 1.5px; font-weight: 700; display: block; margin-bottom: 6px;">Clave de Acceso al Evento:</span>
                        <div style="display: inline-block; background-color: #d0c0ad; border: 1px solid #c4b4a1; border-radius: 4px; padding: 8px 18px;">
                            <span style="font-family: 'Courier New', Courier, monospace; font-size: 16px; font-weight: bold; color: #0a0b0e; letter-spacing: 3px;">%s</span>
                        </div>
                    </td>
                </tr>
            `, strings.TrimSpace(data.AccessCode))
		}

		var linkRow string
		if eventLink != "" {
			linkRow = fmt.Sprintf(`
                <tr>
                    <td style="padding: 12px 0 0 0;">
                        <span style="color: #7a7065; font-size: 10.5px; text-transform: uppercase; letter-spacing: 1.5px; font-weight: 700; display: block; margin-bottom: 4px;">Enlace Oficial del Evento:</span>
                        <p style="margin: 0; word-break: break-all; font-size: 13px;">
                            <a href="%s" target="_blank" style="color: #0a0b0e; text-decoration: underline; font-weight: 600;">%s</a>
                        </p>
                    </td>
                </tr>
            `, eventLink, eventLink)
		}

		badgeTitle := "ACCESO EXCLUSIVO A EVENTO PRIVADO"
		descText := "Este evento es de carácter estrictamente privado. Tus boletos oficiales con código QR se encuentran adjuntos a este correo para tu acceso al recinto. A continuación encontrarás la información oficial para consultar la página del evento:"
		if isInstitutional {
			badgeTitle = "CONTROL Y PROTOCOLO INSTITUCIONAL"
			descText = "Este evento cuenta con control estricto de acreditación y aforo reservado."
		}

		privateEventBlock = fmt.Sprintf(`
                    <!-- Private Event Information Block -->
                    <tr>
                        <td style="padding: 0 30px 20px 30px;">
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #F7F4ED; border-radius: 8px; border: 1px solid rgba(10, 11, 14, 0.14); padding: 22px;">
                                <tr>
                                    <td>
                                        <div style="margin-bottom: 12px;">
                                            <span style="display: inline-block; background-color: #d0c0ad; color: #0a0b0e; border: 1px solid #d0c0ad; padding: 4px 12px; font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: 1.5px; border-radius: 4px;">%s</span>
                                        </div>
                                        <p style="margin: 0 0 12px 0; color: #3d3731; font-size: 13px; line-height: 1.6; font-weight: 400;">
                                            %s
                                        </p>
                                        <table width="100%%" border="0" cellspacing="0" cellpadding="0">
                                            %s
                                            %s
                                        </table>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>
`, badgeTitle, descText, codeRow, linkRow)
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
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Confirmación Oficial · Roveni</title>
</head>
<body style="margin: 0; padding: 0; background-color: #F7F4ED; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; color: #0a0b0e;">
    <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #F7F4ED; padding: 40px 15px;">
        <tr>
            <td align="center">
                <table width="600" border="0" cellspacing="0" cellpadding="0" style="background-color: #FFFFFF; border-radius: 12px; border: 1px solid #d0c0ad; overflow: hidden; box-shadow: 0 12px 35px rgba(10, 11, 14, 0.08);">
                    <!-- Header Bar -->
                    <tr>
                        <td align="center" style="padding: 34px 20px; background-color: #d0c0ad; border-bottom: 1px solid rgba(10, 11, 14, 0.15);">
                            <div style="font-family: 'Playfair Display', Georgia, 'Times New Roman', serif; font-size: 24px; font-weight: 700; color: #0a0b0e; letter-spacing: 5px; text-transform: uppercase;">
                                ROVENI
                            </div>
                            <div style="font-size: 10px; font-weight: 600; color: #4a463f; letter-spacing: 3px; text-transform: uppercase; margin-top: 6px;">
                                EL ACCESO A LO EXTRAORDINARIO
                            </div>
                        </td>
                    </tr>

                    <!-- Success Banner -->
                    <tr>
                        <td style="padding: 34px 30px 24px 30px; text-align: center;">
                            %s
                            <h2 style="margin: 0 0 12px 0; font-size: 24px; color: #0a0b0e; font-weight: 700; font-family: 'Playfair Display', Georgia, serif; letter-spacing: 0.5px;">%s</h2>
                            <p style="margin: 0 auto; color: #5a544b; font-size: 13.5px; line-height: 1.6; max-width: 500px;">%s</p>
                        </td>
                    </tr>

                    <!-- Event Info Card -->
                    <tr>
                        <td style="padding: 0 30px 20px 30px;">
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #F7F4ED; border-radius: 8px; border: 1px solid rgba(10, 11, 14, 0.12); padding: 22px;">
                                <tr>
                                    <td>
                                        <h3 style="margin: 0 0 16px 0; color: #0a0b0e; font-size: 20px; font-weight: 700; line-height: 1.35; font-family: 'Playfair Display', Georgia, serif;">%s</h3>
                                        
                                        <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="font-size: 13px; line-height: 1.6;">
                                            <tr>
                                                <td width="24" valign="top" style="padding: 4px 0; font-size: 14px;">📍</td>
                                                <td style="padding: 4px 0;">
                                                    <span style="color: #7a7065; font-size: 11px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700; display: block;">Recinto & Ubicación:</span>
                                                    <span style="color: #0a0b0e; font-size: 13.5px; font-weight: 600;">%s (%s)</span>
                                                </td>
                                            </tr>
                                            <tr>
                                                <td width="24" valign="top" style="padding: 6px 0 4px 0; font-size: 14px;">📅</td>
                                                <td style="padding: 6px 0 4px 0;">
                                                    <span style="color: #7a7065; font-size: 11px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700; display: block;">Fecha & Hora de Inicio:</span>
                                                    <span style="color: #0a0b0e; font-size: 13.5px; font-weight: 600;">%s a las %s hrs</span>
                                                </td>
                                            </tr>
                                            <tr>
                                                <td width="24" valign="top" style="padding: 6px 0 0 0; font-size: 14px;">🎟️</td>
                                                <td style="padding: 6px 0 0 0;">
                                                    <span style="color: #7a7065; font-size: 11px; text-transform: uppercase; letter-spacing: 1px; font-weight: 700; display: block;">Folio Oficial:</span>
                                                    <span style="display: inline-block; font-family: 'Courier New', Courier, monospace; font-size: 14px; font-weight: bold; color: #0a0b0e; background-color: #e8dfd5; padding: 2px 8px; border-radius: 4px; border: 1px solid rgba(10, 11, 14, 0.12); letter-spacing: 1.5px;">%s</span>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

                    %s

                    <!-- Add to Calendar Block -->
                    <tr>
                        <td style="padding: 0 30px 25px 30px;">
                            <table width="100%%" border="0" cellspacing="0" cellpadding="0" style="background-color: #F7F4ED; border-radius: 8px; border: 1px solid rgba(10, 11, 14, 0.12); padding: 18px;">
                                <tr>
                                    <td align="center">
                                        <h4 style="margin: 0 0 6px 0; color: #0a0b0e; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 2px;">📅 Agrega este evento a tu calendario</h4>
                                        <p style="margin: 0 0 14px 0; color: #6e665b; font-size: 12px;">Sincroniza la fecha y hora directamente en tu aplicación preferida:</p>
                                        <table border="0" cellspacing="0" cellpadding="0">
                                            <tr>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #d0c0ad; border: 1px solid #c4b4a1; color: #0a0b0e; text-decoration: none; padding: 7px 14px; border-radius: 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.8px;">Google Calendar</a>
                                                </td>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #d0c0ad; border: 1px solid #c4b4a1; color: #0a0b0e; text-decoration: none; padding: 7px 14px; border-radius: 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.8px;">Outlook</a>
                                                </td>
                                                <td style="padding: 0 4px;">
                                                    <a href="%s" target="_blank" style="display: inline-block; background-color: #d0c0ad; border: 1px solid #c4b4a1; color: #0a0b0e; text-decoration: none; padding: 7px 14px; border-radius: 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.8px;">Apple Calendar</a>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>

%s

                    <!-- Footer -->
                    <tr>
                        <td align="center" style="padding: 28px 20px; background-color: #d0c0ad; border-top: 1px solid rgba(10, 11, 14, 0.15); color: #5a544b; font-size: 12px; line-height: 1.6;">
                            <div style="font-family: 'Playfair Display', Georgia, serif; font-size: 15px; font-weight: 700; color: #0a0b0e; letter-spacing: 4px; text-transform: uppercase; margin-bottom: 4px;">
                                ROVENI
                            </div>
                            <div style="font-size: 10px; color: #4a463f; letter-spacing: 2.5px; text-transform: uppercase; margin-bottom: 12px; font-weight: 600;">
                                EL ACCESO A LO EXTRAORDINARIO
                            </div>
                            <p style="margin: 0 0 6px 0; color: #5a544b; font-size: 11px;">© 2026 Roveni. Todos los derechos reservados.</p>
                            <p style="margin: 0; font-size: 11px; color: #4a463f;">
                                Consultas oficiales: <a href="mailto:info@rovenisociety.com" style="color: #0a0b0e; text-decoration: underline; font-weight: 600;">info@rovenisociety.com</a> • Síguenos en <a href="https://www.instagram.com/rovenisociety/" target="_blank" style="color: #0a0b0e; text-decoration: underline; font-weight: 600;">Instagram</a>
                            </p>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`,
		bannerBadgeHTML,
		bannerTitle,
		bannerSubtitle,
		data.EventName,
		data.VenueAddress,
		data.City,
		data.EventDate,
		data.ShowTime,
		data.OrderNumber,
		privateEventBlock,
		googleURL,
		outlookURL,
		iCalEndpoint,
		breakdownBlock,
	)

	return subject, htmlBody
}

// FormatSenderHeader parses and formats a sender display name and email address according to RFC 5322.
// It returns envelopeEmail (clean email for SMTP envelope MAIL FROM) and headerFrom (display name + email for MIME headers).
func FormatSenderHeader(senderName, rawFrom string) (envelopeEmail string, headerFrom string) {
	rawFrom = strings.TrimSpace(rawFrom)
	if rawFrom == "" {
		rawFrom = "info@rovenisociety.com"
	}

	envelopeEmail = rawFrom
	name := strings.TrimSpace(senderName)

	if parsed, err := mail.ParseAddress(rawFrom); err == nil {
		if name == "" {
			name = parsed.Name
		}
		envelopeEmail = parsed.Address
	}

	if name == "" {
		name = strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	}
	if name == "" {
		name = "Roveni Society"
	}

	headerFrom = (&mail.Address{
		Name:    name,
		Address: envelopeEmail,
	}).String()

	return envelopeEmail, headerFrom
}

// SendPurchaseConfirmationEmail sends a confirmation email to the buyer with order, ticket details and PDF attachments.
func SendPurchaseConfirmationEmail(data EmailData) error {
	envelopeFrom, fromHeader := FormatSenderHeader(data.SenderName, os.Getenv("SMTP_FROM"))

	subject, htmlBody := BuildConfirmationEmailHTML(data)
	start, end := ParseEventTimes(data.EventDate, data.ShowTime)

	boundary := "---ROVENI_MIME_BOUNDARY_2026---"

	var msgBuilder strings.Builder
	msgBuilder.WriteString(fmt.Sprintf("From: %s\r\n", fromHeader))
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
	if smtpUsername == "" {
		smtpUsername = envelopeFrom
	}

	recipients := []string{data.To}
	bccEmail := strings.TrimSpace(os.Getenv("SMTP_BCC"))
	if bccEmail == "" {
		bccEmail = "evidencia@rovenisociety.com"
	}
	if bccEmail != "" && bccEmail != data.To {
		recipients = append(recipients, bccEmail)
	}

	log.Printf("[SMTP INFO] Attempting email delivery via %s:%s (From: %s, To: %v)...", hostClean, smtpPort, fromHeader, recipients)
	err := sendMailViaPort(hostClean, smtpPort, smtpUsername, smtpPassword, envelopeFrom, recipients, msg)
	if err != nil {
		log.Printf("[SMTP WARNING] Primary delivery via %s:%s failed: %v", hostClean, smtpPort, err)

		// Automatic dual-port fallback (if 465 fails, try 587; if 587 fails, try 465)
		fallbackPort := "587"
		if smtpPort == "587" || smtpPort == "25" {
			fallbackPort = "465"
		}
		log.Printf("[SMTP INFO] Retrying via fallback port %s...", fallbackPort)
		fallbackErr := sendMailViaPort(hostClean, fallbackPort, smtpUsername, smtpPassword, envelopeFrom, recipients, msg)
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
