package tickets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/skip2/go-qrcode"
)

type TicketPDFData struct {
	OrderNumber   string
	TicketSerial  string
	TicketIndex   int
	TotalTickets  int
	EventName     string
	VenueAddress  string
	City          string
	Country       string
	EventDate     string
	DoorsOpenTime string
	ShowStartTime string
	CategoryName  string
	CustomerName  string
	CustomerEmail string
	Price         float64
	Currency      string
}

func GenerateTicketQRPayload(data TicketPDFData) (string, string) {
	secretKey := os.Getenv("QR_SECRET_KEY")
	if secretKey == "" {
		secretKey = "Roveni_Secret_Scan_Key_2026_HMAC"
	}

	rawString := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		data.OrderNumber,
		data.TicketSerial,
		data.EventName,
		data.CategoryName,
		data.CustomerName,
		data.CustomerEmail,
	)

	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(rawString))
	signature := hex.EncodeToString(h.Sum(nil))

	payload := map[string]any{
		"app":       "Roveni",
		"order":     data.OrderNumber,
		"serial":    data.TicketSerial,
		"event":     data.EventName,
		"category":  data.CategoryName,
		"holder":    data.CustomerName,
		"issued_at": time.Now().Unix(),
		"sig":       signature[:16],
	}

	jsonBytes, _ := json.Marshal(payload)
	return string(jsonBytes), signature[:16]
}

func GenerateSingleTicketPDF(data TicketPDFData) (string, error) {
	outputDir := filepath.Join(".", "uploads", "tickets")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create tickets directory: %w", err)
	}

	pdfPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s.pdf", data.OrderNumber, data.TicketSerial))

	// 1. Generate QR Code image PNG
	qrPayload, qrSig := GenerateTicketQRPayload(data)
	qrPath := filepath.Join(outputDir, fmt.Sprintf("qr_%s_%s.png", data.OrderNumber, data.TicketSerial))

	err := qrcode.WriteFile(qrPayload, qrcode.Medium, 256, qrPath)
	if err != nil {
		return "", fmt.Errorf("failed to generate QR code: %w", err)
	}
	defer os.Remove(qrPath)

	// 2. Initialize PDF in Landscape Card Format (210mm x 120mm)
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		UnitStr: "mm",
		Size:    gofpdf.SizeType{Wd: 210, Ht: 120},
	})
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	// Dark Background (#0B0F19)
	pdf.SetFillColor(11, 15, 25)
	pdf.Rect(0, 0, 210, 120, "F")

	// Header Bar Background (#131B2E)
	pdf.SetFillColor(19, 27, 46)
	pdf.Rect(0, 0, 210, 18, "F")

	// --- LOGO ENGINE DEFENSA ---
	logoPath := filepath.Join("uploads", "logos", "logoAP.png")
	if _, err := os.Stat(logoPath); err == nil {
		// Alto en 0 para auto-escalado proporcional
		pdf.ImageOptions(logoPath, 12, 4, 35, 0, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	} else {
		// Fallback visual si el asset no está en el directorio en tiempo de ejecución
		pdf.SetFont("Helvetica", "B", 12)
		pdf.SetTextColor(255, 255, 255)
		pdf.SetXY(12, 6)
		pdf.Cell(40, 6, "ROVENI")
	}

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(70, 7)
	pdf.Cell(80, 4, "BOLETO DIGITAL")

	// Ticket Serial Badge (Top Right)
	pdf.SetFillColor(30, 41, 59)
	pdf.Rect(145, 4, 55, 10, "F")
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(145, 6)
	pdf.CellFormat(55, 6, fmt.Sprintf("SERIE: %s", data.TicketSerial), "", 0, "C", false, 0, "")

	// Vertical Separator
	pdf.SetDrawColor(45, 55, 72)
	pdf.SetLineWidth(0.5)
	pdf.Line(140, 26, 140, 114)

	// --- LEFT COLUMN ---
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	// Event Name
	pdf.SetFont("Helvetica", "B", 14)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(12, 28)
	pdf.MultiCell(122, 6, tr(data.EventName), "", "L", false)

	// Category Badge
	currY := pdf.GetY() + 2
	pdf.SetFillColor(24, 34, 56)
	pdf.Rect(12, currY, 65, 7, "F")
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(92, 124, 250)
	pdf.SetXY(14, currY+1)
	pdf.Cell(60, 5, tr(fmt.Sprintf("CATEGORIA: %s", strings.ToUpper(data.CategoryName))))

	// --- LUGAR ---
	currY += 10
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "LUGAR:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(37, currY)
	venueText := fmt.Sprintf("%s (%s)", data.VenueAddress, data.City)
	pdf.MultiCell(97, 4, tr(venueText), "", "L", false)

	currY = pdf.GetY() + 2

	// --- FECHA Y HORA ---
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "FECHA Y HORA:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(37, currY)
	pdf.Cell(97, 4, tr(fmt.Sprintf("%s | Show: %s (Puertas: %s)", data.EventDate, data.ShowStartTime, data.DoorsOpenTime)))

	// --- TITULAR ---
	currY += 6
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "TITULAR:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(37, currY)
	pdf.Cell(97, 4, tr(data.CustomerName))

	// --- ORDEN Y BOLETO ---
	currY += 6
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "ORDEN:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(92, 124, 250)
	pdf.SetXY(37, currY)
	pdf.Cell(45, 4, data.OrderNumber)

	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(82, currY)
	pdf.Cell(15, 4, "BOLETO:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(97, currY)
	pdf.Cell(25, 4, fmt.Sprintf("%d de %d", data.TicketIndex, data.TotalTickets))

	pdf.SetFont("Helvetica", "", 6)
	pdf.SetTextColor(100, 116, 139)
	pdf.SetXY(12, 110)
	pdf.Cell(125, 4, "Presenta este boleto digital a la entrada.")

	pdf.SetFont("Helvetica", "", 6)
	pdf.SetTextColor(100, 116, 139)
	pdf.SetXY(12, 113)
	pdf.Cell(125, 4, "Validacion oficial Roveni.")

	// publicidad := filepath.Join("uploads", "publicidad", "tequila.png")
	// if _, err := os.Stat(publicidad); err == nil {
	//	 pdf.ImageOptions(publicidad, 65, 85, 70, 30, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	// } else {
	//	 pdf.SetFont("Helvetica", "B", 12)
	//	 pdf.SetTextColor(255, 255, 255)
	//	 pdf.SetXY(65, 85)
	//	 pdf.Cell(40, 6, "PUBLICIDAD")
	// }

	// --- RIGHT COLUMN ---
	pdf.ImageOptions(qrPath, 148, 28, 50, 50, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetFont("Helvetica", "B", 7)
	pdf.SetTextColor(148, 163, 184)
	pdf.SetXY(145, 80)
	pdf.CellFormat(55, 4, "CODIGO DE SEGURIDAD", "", 0, "C", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(145, 85)
	pdf.CellFormat(55, 5, fmt.Sprintf("SIG: %s", qrSig), "", 0, "C", false, 0, "")

	pdf.SetFont("Helvetica", "", 6)
	pdf.SetTextColor(100, 116, 139)
	pdf.SetXY(145, 93)
	pdf.MultiCell(55, 3, "Escaneo exclusivo por Roveni Scanner.", "", "C", false)

	// Validar si la librería acumuló errores internos
	if pdf.Err() {
		return "", fmt.Errorf("gofpdf internal error rendering pdf: %w", pdf.Error())
	}

	err = pdf.OutputFileAndClose(pdfPath)
	if err != nil {
		return "", fmt.Errorf("failed to save ticket PDF: %w", err)
	}

	return pdfPath, nil
}

func GenerateAllTicketsForOrder(
	orderNumber string,
	customerName string,
	customerEmail string,
	eventName string,
	venueAddress string,
	city string,
	country string,
	eventDate string,
	doorsOpenTime string,
	showStartTime string,
	tickets []TicketPDFData,
) ([]string, error) {
	var pdfPaths []string
	totalCount := len(tickets)

	for i, t := range tickets {
		t.OrderNumber = orderNumber
		t.CustomerName = customerName
		t.CustomerEmail = customerEmail
		t.EventName = eventName
		t.VenueAddress = venueAddress
		t.City = city
		t.Country = country
		t.EventDate = eventDate
		t.DoorsOpenTime = doorsOpenTime
		t.ShowStartTime = showStartTime
		t.TicketIndex = i + 1
		t.TotalTickets = totalCount
		if t.TicketSerial == "" {
			t.TicketSerial = fmt.Sprintf("%s-%02d", orderNumber, i+1)
		}

		path, err := GenerateSingleTicketPDF(t)
		if err != nil {
			log.Printf("[PDF TICKET ERROR] Failed for ticket %d of order %s: %v", i+1, orderNumber, err)
			continue
		}
		pdfPaths = append(pdfPaths, path)
	}

	return pdfPaths, nil
}
