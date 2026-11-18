package mailer

import (
	"os"
	"strings"
	"testing"
)

func TestBuildConfirmationEmailHTML(t *testing.T) {
	cases := []struct {
		name         string
		data         EmailData
		mustContain  []string
		mustNotConta []string
	}{
		{
			name: "Commercial purchase",
			data: EmailData{
				To:           "cliente@ejemplo.com",
				CustomerName: "Alejandro Garza",
				EventID:      1,
				EventName:    "Gala Sinfónica de Otoño",
				VenueAddress: "Teatro Degollado",
				City:         "Guadalajara, JAL",
				EventDate:    "2026-11-25",
				ShowTime:     "20:30",
				OrderNumber:  "RV-2026-8812",
				Subtotal:     2400.00,
				ServiceFee:   240.00,
				TotalAmount:  2640.00,
				Currency:     "MXN",
				Tickets: []TicketItem{
					{Name: "Palco Preferente", Quantity: 2, Price: 1200.00},
				},
				IsPrivate: false,
			},
			mustContain: []string{
				"ROVENI SOCIETY",
				"EL ACCESO A LO EXTRAORDINARIO",
				"Bienvenido(a), Alejandro Garza",
				"Teatro Degollado",
				"RV-2026-8812",
				"Palco Preferente",
				"Total Pagado:",
				"width=\"100%\"",
			},
			mustNotConta: []string{
				"100%%",
				"CONFIRMACIÓN DE COMPRA", // old blue badge
			},
		},
		{
			name: "Institutional invitation",
			data: EmailData{
				To:           "rectoria@udg.mx",
				CustomerName: "Dr. Ricardo Villanueva",
				EventID:      2,
				EventName:    "Ceremonia Magna de Reconocimiento",
				VenueAddress: "Conjunto Santander",
				City:         "Zapopan, JAL",
				EventDate:    "2026-12-05",
				ShowTime:     "18:00",
				OrderNumber:  "RV-INST-0041",
				TotalAmount:  0.0,
				Currency:     "MXN",
				Tickets: []TicketItem{
					{Name: "Presídium e Invitados Especiales", Quantity: 1, Price: 0.0},
				},
				IsPrivate:   true,
				PrivateType: "solo_lectura",
				AccessCode:  "PROTOCOLO-2026",
			},
			mustContain: []string{
				"INVITACIÓN OFICIAL · ACREDITACIÓN NOMINAL",
				"Distinguido(a) Dr. Ricardo Villanueva",
				"acto solemne",
				"Modalidad de Ingreso:",
				"Invitación Oficial Acreditada",
				"PROTOCOLO-2026",
			},
			mustNotConta: []string{
				"100%%",
				"Total Pagado:",
			},
		},
		{
			name: "Courtesy / Special access",
			data: EmailData{
				To:           "invitado@cortesia.com",
				CustomerName: "Sofía Morales",
				EventID:      3,
				EventName:    "Roveni Summer Nights",
				VenueAddress: "Palacio de la Cultura",
				City:         "Zapopan, JAL",
				EventDate:    "2026-12-10",
				ShowTime:     "21:00",
				OrderNumber:  "RV-CORT-0105",
				TotalAmount:  0.0,
				Currency:     "MXN",
				IsCourtesy:   true,
				Tickets: []TicketItem{
					{Name: "Acceso Especial VIP", Quantity: 2, Price: 0.0},
				},
				IsPrivate: false,
			},
			mustContain: []string{
				"ACCESO ESPECIAL · CORTESÍA AUTORIZADA",
				"¡Tu acceso especial está listo, Sofía Morales!",
				"Pase de Cortesía Autorizado",
				"$0.00 (Acceso Especial)",
			},
			mustNotConta: []string{
				"100%%",
				"Total Pagado:",
			},
		},
		{
			name: "Private interactive event",
			data: EmailData{
				To:           "socio@club.com",
				CustomerName: "Carlos Slim Domit",
				EventID:      4,
				EventName:    "Cata Exclusiva Millésime",
				VenueAddress: "Club de Industriales",
				City:         "CDMX",
				EventDate:    "2026-12-18",
				ShowTime:     "20:00",
				OrderNumber:  "RV-PRIV-4011",
				Subtotal:     12000.00,
				ServiceFee:   1200.00,
				TotalAmount:  13200.00,
				Currency:     "MXN",
				Tickets: []TicketItem{
					{Name: "Pase Degustación", Quantity: 2, Price: 6000.00},
				},
				IsPrivate:   true,
				PrivateType: "compra",
				AccessCode:  "MILLESIME-2026",
			},
			mustContain: []string{
				"EVENTO PRIVADO · ACCESO EXCLUSIVO",
				"Acceso Exclusivo Confirmado, Carlos Slim Domit",
				"ACCESO EXCLUSIVO A EVENTO PRIVADO",
				"MILLESIME-2026",
				"Enlace Oficial del Evento:",
				"width=\"100%\"",
			},
			mustNotConta: []string{
				"100%%",
				"?pwd=", // no pre-loaded password in URL button
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subj, html := BuildConfirmationEmailHTML(tc.data)
			if subj == "" {
				t.Errorf("subject should not be empty")
			}
			for _, exp := range tc.mustContain {
				if !strings.Contains(html, exp) {
					t.Errorf("expected HTML to contain %q, but was missing", exp)
				}
			}
			for _, notExp := range tc.mustNotConta {
				if strings.Contains(html, notExp) {
					t.Errorf("expected HTML to NOT contain %q, but was found", notExp)
				}
			}

			// Write sample output for review
			cleanName := strings.ReplaceAll(tc.name, " ", "_")
			_ = os.WriteFile("/tmp/roveni_email_"+cleanName+".html", []byte(html), 0644)
		})
	}
}
