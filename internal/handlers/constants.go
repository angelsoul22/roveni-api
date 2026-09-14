package handlers

import "strings"

// Standardized Event Status Constants for Roveni
const (
	EventStatusPublicado  = "Publicado"
	EventStatusPausado    = "Pausado"
	EventStatusAgotado    = "Agotado"
	EventStatusCancelado  = "Cancelado"
	EventStatusFinalizado = "Finalizado"
)

// NormalizeEventStatus cleans and standardizes any status string to canonical TitleCase.
func NormalizeEventStatus(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "pausado", "paused":
		return EventStatusPausado
	case "cancelado", "cancelled":
		return EventStatusCancelado
	case "agotado", "sold_out", "soldout":
		return EventStatusAgotado
	case "finalizado", "completed", "ended":
		return EventStatusFinalizado
	case "publicado", "active", "published":
		return EventStatusPublicado
	default:
		if raw != "" {
			return strings.TrimSpace(raw)
		}
		return EventStatusPublicado
	}
}

// IsPubliclyVisibleStatus returns true if the event status is visible to public users.
func IsPubliclyVisibleStatus(raw string) bool {
	st := NormalizeEventStatus(raw)
	return st == EventStatusPublicado || st == EventStatusAgotado
}

// IsEventInactiveStatus returns true if the event is paused, cancelled, or finalized.
func IsEventInactiveStatus(raw string) bool {
	st := NormalizeEventStatus(raw)
	return st == EventStatusPausado || st == EventStatusCancelado || st == EventStatusFinalizado
}
