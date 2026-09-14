package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"roveni/internal/mailer"
)

// GetEventICSHandler returns a downloadable .ics file for an event
func GetEventICSHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventIDStr := r.URL.Query().Get("id")
		if eventIDStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing event id"})
			return
		}

		eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid event id"})
			return
		}

		var name, desc, city, venue, eventDate, startTime string
		err = db.QueryRow(`
			SELECT name, description, city, venue_address, event_date, show_start_time 
			FROM events 
			WHERE id = $1
		`, eventID).Scan(&name, &desc, &city, &venue, &eventDate, &startTime)

		if err != nil {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}

		start, end := mailer.ParseEventTimes(eventDate, startTime)
		icsContent := mailer.GenerateICSContent(name, desc, city, venue, "", start, end)

		filename := fmt.Sprintf("evento_roveni_%d.ics", eventID)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(icsContent))
	}
}
