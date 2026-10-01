package tickets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	EventImage    string
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

	// Warm Luxury Base Background (#d0c0ad -> RGB: 208, 192, 173)
	pdf.SetFillColor(208, 192, 173)
	pdf.Rect(0, 0, 210, 120, "F")

	// Header Bar Background (#0a0b0e -> RGB: 10, 11, 14)
	pdf.SetFillColor(10, 11, 14)
	pdf.Rect(0, 0, 210, 20, "F")

	// --- LOGO ROVENI DEFENSA ---
	logoPath := filepath.Join("uploads", "logos", "logoRb.png")
	if _, err := os.Stat(logoPath); err != nil {
		logoPath = filepath.Join("uploads", "logos", "logoRb.png")
	}

	if _, err := os.Stat(logoPath); err == nil {
		pdf.ImageOptions(logoPath, 10, 3, 36, 0, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	} else {
		pdf.SetFont("Helvetica", "B", 13)
		pdf.SetTextColor(208, 192, 173)
		pdf.SetXY(12, 6)
		pdf.Cell(40, 6, "ROVENI SOCIETY")
	}

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(208, 192, 173)
	pdf.SetXY(70, 7)
	pdf.Cell(70, 4, "BOLETO DIGITAL OFICIAL")

	// Ticket Serial Badge (Top Right)
	pdf.SetFillColor(196, 180, 161) // #c4b4a1
	pdf.Rect(145, 4, 55, 10, "F")
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14) // #0a0b0e
	pdf.SetXY(145, 6)
	pdf.CellFormat(55, 6, fmt.Sprintf("SERIE: %s", data.TicketSerial), "", 0, "C", false, 0, "")

	// Vertical Separator
	pdf.SetDrawColor(10, 11, 14)
	pdf.SetLineWidth(0.6)
	pdf.Line(140, 22, 140, 114)

	// --- LEFT COLUMN ---
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	// Check and render Event Image if present
	hasImage := false
	imageWidth := 34.0
	imageHeight := 34.0
	if data.EventImage != "" {
		cleanImgPath := strings.TrimPrefix(data.EventImage, "/")
		fullImgPath := filepath.Join(".", cleanImgPath)
		if _, err := os.Stat(fullImgPath); err == nil {
			hasImage = true
			pdf.ImageOptions(fullImgPath, 12, 24, imageWidth, imageHeight, false, gofpdf.ImageOptions{}, 0, "")
		}
	}

	leftMargin := 12.0
	contentWidth := 122.0
	if hasImage {
		leftMargin = 50.0
		contentWidth = 84.0
	}

	// Event Name
	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(leftMargin, 24)
	pdf.MultiCell(contentWidth, 5.5, tr(data.EventName), "", "L", false)

	currY := pdf.GetY() + 2
	if hasImage && currY < 24+imageHeight {
		// Category Badge
		pdf.SetFillColor(196, 180, 161)
		pdf.Rect(leftMargin, currY, 55, 6, "F")
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(10, 11, 14)
		pdf.SetXY(leftMargin+2, currY+0.8)
		pdf.Cell(51, 4.5, tr(fmt.Sprintf("CAT: %s", strings.ToUpper(data.CategoryName))))

		currY = 24 + imageHeight + 4
	} else {
		// Category Badge
		pdf.SetFillColor(196, 180, 161)
		pdf.Rect(12, currY, 65, 6, "F")
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(10, 11, 14)
		pdf.SetXY(14, currY+0.8)
		pdf.Cell(60, 4.5, tr(fmt.Sprintf("CATEGORIA: %s", strings.ToUpper(data.CategoryName))))

		currY += 8
	}

	// --- LUGAR ---
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "LUGAR:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(37, currY)
	venueText := fmt.Sprintf("%s (%s, %s)", data.VenueAddress, data.City, data.Country)
	pdf.MultiCell(97, 4, tr(venueText), "", "L", false)

	currY = pdf.GetY() + 2

	// --- FECHA Y HORA ---
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "FECHA Y HORA:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(37, currY)
	pdf.Cell(97, 4, tr(fmt.Sprintf("%s | Show: %s (Puertas: %s)", data.EventDate, data.ShowStartTime, data.DoorsOpenTime)))

	// --- TITULAR ---
	currY += 5.5
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "TITULAR:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(37, currY)
	pdf.Cell(97, 4, tr(data.CustomerName))

	// --- ORDEN Y BOLETO ---
	currY += 5.5
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, currY)
	pdf.Cell(25, 4, "ORDEN:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(37, currY)
	pdf.Cell(45, 4, data.OrderNumber)

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(82, currY)
	pdf.Cell(15, 4, "BOLETO:")

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(97, currY)
	pdf.Cell(25, 4, fmt.Sprintf("%d de %d", data.TicketIndex, data.TotalTickets))

	pdf.SetFont("Helvetica", "B", 6)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, 110)
	pdf.Cell(125, 4, "Presenta este boleto digital en el acceso principal.")

	pdf.SetFont("Helvetica", "B", 6)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(12, 113)
	pdf.Cell(125, 4, tr("Validación y seguridad oficial Roveni Society."))

	// --- RIGHT COLUMN ---
	pdf.SetFillColor(196, 180, 161)
	pdf.Rect(146, 24, 54, 54, "F")
	pdf.ImageOptions(qrPath, 148, 26, 50, 50, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetFont("Helvetica", "B", 7)
	pdf.SetTextColor(90, 78, 64)
	pdf.SetXY(145, 81)
	pdf.CellFormat(55, 4, "CODIGO DE SEGURIDAD", "", 0, "C", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(10, 11, 14)
	pdf.SetXY(145, 86)
	pdf.CellFormat(55, 5, fmt.Sprintf("SIG: %s", qrSig), "", 0, "C", false, 0, "")

	pdf.SetFont("Helvetica", "B", 6)
	pdf.SetTextColor(90, 78, 64)
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
	eventImage string,
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
		t.EventImage = eventImage
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
			return nil, err
		}
		pdfPaths = append(pdfPaths, path)
	}

	return pdfPaths, nil
}
