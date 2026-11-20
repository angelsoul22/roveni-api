package tickets

import (
	"os"
	"strings"
	"testing"
)

func TestSingleTicketPDFCounter(t *testing.T) {
	cases := []struct {
		name              string
		data              TicketPDFData
		expectHideCounter bool
	}{
		{
			name: "Commercial single ticket",
			data: TicketPDFData{
				OrderNumber:   "RV-COMM-0001",
				TicketSerial:  "RV-COMM-0001-01",
				TicketIndex:   1,
				TotalTickets:  1,
				EventName:     "Festival de Primavera",
				VenueAddress:  "Av. Chapultepec 100",
				City:          "Guadalajara",
				Country:       "México",
				EventDate:     "2026-12-01",
				ShowStartTime: "20:00",
				CategoryName:  "General",
				CustomerName:  "Carlos Gomez",
				CustomerEmail: "carlos@ejemplo.com",
				Price:         500.00,
				Currency:      "MXN",
				IsPrivate:     false,
			},
			expectHideCounter: false,
		},
		{
			name: "Commercial multi-ticket",
			data: TicketPDFData{
				OrderNumber:   "RV-COMM-0002",
				TicketSerial:  "RV-COMM-0002-01",
				TicketIndex:   1,
				TotalTickets:  3,
				EventName:     "Festival de Primavera",
				VenueAddress:  "Av. Chapultepec 100",
				City:          "Guadalajara",
				Country:       "México",
				EventDate:     "2026-12-01",
				ShowStartTime: "20:00",
				CategoryName:  "VIP",
				CustomerName:  "Carlos Gomez",
				CustomerEmail: "carlos@ejemplo.com",
				Price:         1500.00,
				Currency:      "MXN",
				IsPrivate:     false,
			},
			expectHideCounter: false,
		},
		{
			name: "Private single ticket (formal event)",
			data: TicketPDFData{
				OrderNumber:      "RV-PRIV-0001",
				TicketSerial:     "RV-PRIV-0001-01",
				TicketIndex:      1,
				TotalTickets:     1,
				EventName:        "Primer Informe de Actividades FEU",
				VenueAddress:     "Conjunto Santander",
				City:             "Zapopan",
				Country:          "México",
				EventDate:        "2026-11-25",
				ShowStartTime:    "12:00",
				CategoryName:     "Invitación Especial",
				CustomerName:     "Dra. Maria Elena",
				CustomerEmail:    "maria@udg.mx",
				Price:            0.0,
				Currency:         "MXN",
				IsPrivate:        true,
				TicketDesignMode: "invitacion",
			},
			expectHideCounter: true,
		},
		{
			name: "Private multi-ticket",
			data: TicketPDFData{
				OrderNumber:   "RV-PRIV-0002",
				TicketSerial:  "RV-PRIV-0002-01",
				TicketIndex:   1,
				TotalTickets:  2,
				EventName:     "Cena de Gala Exclusiva",
				VenueAddress:  "Club de Industriales",
				City:          "CDMX",
				Country:       "México",
				EventDate:     "2026-12-15",
				ShowStartTime: "21:00",
				CategoryName:  "Mesa Oro",
				CustomerName:  "Roberto Garza",
				CustomerEmail: "roberto@ejemplo.com",
				Price:         3000.00,
				Currency:      "MXN",
				IsPrivate:     true,
			},
			expectHideCounter: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := GenerateSingleTicketPDF(tc.data)
			if err != nil {
				t.Fatalf("unexpected error generating ticket PDF: %v", err)
			}
			defer os.Remove(path)

			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read generated PDF: %v", err)
			}

			hasBoletoLabel := strings.Contains(string(content), "BOLETO:")
			if tc.expectHideCounter && hasBoletoLabel {
				t.Errorf("expected counter to be hidden, but found 'BOLETO:' in PDF")
			}
			if !tc.expectHideCounter && !hasBoletoLabel {
				t.Errorf("expected counter to be visible, but 'BOLETO:' was missing in PDF")
			}
		})
	}
}

func TestInstitutionalInvitationWithZone(t *testing.T) {
	data := TicketPDFData{
		OrderNumber:      "RV-000001",
		TicketSerial:     "RV-000001-01",
		TicketIndex:      1,
		TotalTickets:     1,
		EventName:        "Primer Informe de Actividades de la Presidencia de la FEU 2026",
		VenueAddress:     "Sala 2 del Conjunto Santander de Artes Escénicas",
		City:             "Guadalajara",
		Country:          "México",
		EventDate:        "2026-11-25",
		DoorsOpenTime:    "12:00",
		ShowStartTime:    "13:00",
		CategoryName:     "Invitación Oficial",
		CustomerName:     "Mtra. Karla Alejandrina Planter Perez",
		CustomerEmail:    "karla.planter@udg.mx",
		Price:            0.00,
		Currency:         "MXN",
		EventZone:        "Azul Rey",
		IsPrivate:        true,
		TicketDesignMode: "invitacion",
		TicketAdImage:    "/uploads/ads/ticket_ad_informe.png",
	}

	path, err := GenerateSingleTicketPDF(data)
	if err != nil {
		t.Fatalf("unexpected error generating ticket PDF: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read generated PDF: %v", err)
	}

	if !strings.Contains(string(content), "ZONA:") {
		t.Errorf("expected 'ZONA:' label in PDF")
	}
	if !strings.Contains(string(content), "AZUL REY") {
		t.Errorf("expected 'AZUL REY' in PDF")
	}

	_ = os.Rename(path, "/tmp/invitacion_con_zona.pdf")
}
