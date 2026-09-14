package mailer

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ParseEventTimes parses date string (e.g. "2026-08-15") and time string (e.g. "20:00") into start and end time.Time structs
func ParseEventTimes(eventDateStr, startTimeStr string) (time.Time, time.Time) {
	cleanDate := strings.TrimSpace(eventDateStr)
	cleanTime := strings.TrimSpace(startTimeStr)

	if cleanTime == "" {
		cleanTime = "20:00"
	}

	now := time.Now().UTC()
	start := now.Add(24 * 7 * time.Hour)

	var datePart time.Time
	var err error

	formats := []string{
		"2006-01-02",
		"02/01/2006",
		"2006/01/02",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}

	for _, fmtStr := range formats {
		if t, e := time.Parse(fmtStr, cleanDate); e == nil {
			datePart = t
			err = nil
			break
		} else {
			err = e
		}
	}

	if err == nil {
		var hour, min int
		_, _ = fmt.Sscanf(cleanTime, "%d:%d", &hour, &min)
		start = time.Date(datePart.Year(), datePart.Month(), datePart.Day(), hour, min, 0, 0, time.UTC)
	}

	end := start.Add(3 * time.Hour)
	return start, end
}

// FormatICalTime formats time.Time to iCalendar ISO-8601 UTC string (YYYYMMDDTHHMMSSZ)
func FormatICalTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// GenerateICSContent creates a valid RFC 5545 iCalendar string
func GenerateICSContent(eventName, description, location, venue, orderNum string, startTime, endTime time.Time) string {
	nowStr := FormatICalTime(time.Now().UTC())
	startStr := FormatICalTime(startTime)
	endStr := FormatICalTime(endTime)

	fullLocation := strings.TrimSpace(venue)
	if location != "" && location != venue {
		if fullLocation != "" {
			fullLocation += ", " + location
		} else {
			fullLocation = location
		}
	}

	summary := eventName
	if orderNum != "" {
		summary += fmt.Sprintf(" - Orden #%s", orderNum)
	}

	cleanDesc := strings.ReplaceAll(description, "\n", "\\n")
	cleanDesc = strings.ReplaceAll(cleanDesc, "\r", "")

	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\n")
	sb.WriteString("VERSION:2.0\r\n")
	sb.WriteString("PRODID:-//Roveni//Boletos Oficiales 1.0//ES\r\n")
	sb.WriteString("CALSCALE:GREGORIAN\r\n")
	sb.WriteString("METHOD:REQUEST\r\n")
	sb.WriteString("BEGIN:VEVENT\r\n")
	sb.WriteString(fmt.Sprintf("UID:roveni-%d-%s@rovenisociety.com\r\n", time.Now().UnixNano(), orderNum))
	sb.WriteString(fmt.Sprintf("DTSTAMP:%s\r\n", nowStr))
	sb.WriteString(fmt.Sprintf("DTSTART:%s\r\n", startStr))
	sb.WriteString(fmt.Sprintf("DTEND:%s\r\n", endStr))
	sb.WriteString(fmt.Sprintf("SUMMARY:%s\r\n", summary))
	sb.WriteString(fmt.Sprintf("DESCRIPTION:%s\r\n", cleanDesc))
	sb.WriteString(fmt.Sprintf("LOCATION:%s\r\n", fullLocation))
	sb.WriteString("STATUS:CONFIRMED\r\n")
	sb.WriteString("END:VEVENT\r\n")
	sb.WriteString("END:VCALENDAR\r\n")

	return sb.String()
}

// BuildCalendarURLs generates direct web links for Google Calendar and Outlook
func BuildCalendarURLs(eventName, description, location, venue string, startTime, endTime time.Time) (googleURL, outlookURL string) {
	startISO := FormatICalTime(startTime)
	endISO := FormatICalTime(endTime)

	fullLocation := strings.TrimSpace(venue)
	if location != "" && location != venue {
		if fullLocation != "" {
			fullLocation += ", " + location
		} else {
			fullLocation = location
		}
	}

	// Google Calendar URL
	gURL := fmt.Sprintf(
		"https://calendar.google.com/calendar/render?action=TEMPLATE&text=%s&dates=%s/%s&details=%s&location=%s",
		url.QueryEscape(eventName+" - Roveni"),
		startISO,
		endISO,
		url.QueryEscape(description+"\n\nBoletos emitidos por Roveni: https://rovenisociety.com"),
		url.QueryEscape(fullLocation),
	)

	// Outlook Web Calendar URL
	oURL := fmt.Sprintf(
		"https://outlook.live.com/calendar/0/deeplink/compose?path=/calendar/action/compose&rru=addevent&subject=%s&startdt=%s&enddt=%s&body=%s&location=%s",
		url.QueryEscape(eventName+" - Roveni"),
		startISO,
		endISO,
		url.QueryEscape(description+"\n\nBoletos emitidos por Roveni: https://rovenisociety.com"),
		url.QueryEscape(fullLocation),
	)

	return gURL, oURL
}
