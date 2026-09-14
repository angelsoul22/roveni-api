package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"roveni/internal/cache"
	"roveni/internal/database"
	"roveni/internal/handlers"
)

func loadEnv() {
	filenames := []string{".env", "../.env"}
	var target string
	for _, f := range filenames {
		if _, err := os.Stat(f); err == nil {
			target = f
			break
		}
	}

	if target == "" {
		fmt.Println("[ENV NOTICE] No local .env file found. Using system/container environment variables.")
		return
	}

	file, err := os.Open(target)
	if err != nil {
		fmt.Printf("[ENV ERROR] Could not open %s: %v\n", target, err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		if idx := strings.Index(line, " #"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
				(strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`)) {
				value = value[1 : len(value)-1]
			}

			// Solo establecer si la variable NO existe previamente en el contenedor
			if os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}
	}
}

func main() {
	loadEnv()

	db, err := database.InitDB()
	if err != nil {
		fmt.Printf("Error DB: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	rdb, err := cache.InitRedis()
	if err != nil || rdb == nil {
		fmt.Printf("[REDIS NOTICE] Running API without Redis session cache.\n")
	} else {
		defer rdb.Close()
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"mensaje": "Backend de Roveni en Go listo"}`)
	})

	// Endpoints
	mux.HandleFunc("/organizers/register", handlers.RegisterOrganizerHandler(db))
	mux.HandleFunc("/organizers/login", handlers.LoginOrganizerHandler(db, rdb))
	mux.HandleFunc("/promoters/register", handlers.RegisterPromoterHandler(db, rdb))
	mux.HandleFunc("/users/register", handlers.RegisterPromoterHandler(db, rdb))
	mux.HandleFunc("/public/users/register", handlers.RegisterCustomerHandler(db, rdb))
	mux.HandleFunc("/public/users/login", handlers.LoginUserHandler(db, rdb))
	mux.HandleFunc("/session", handlers.SessionOrganizerHandler(db, rdb))
	mux.HandleFunc("/organizers/me", handlers.SessionOrganizerHandler(db, rdb))
	mux.HandleFunc("/organizers/logout", handlers.LogoutOrganizerHandler(rdb))
	mux.HandleFunc("/users/list", handlers.ListUsersHandler(db, rdb))
	mux.HandleFunc("/organizers/list", handlers.ListUsersHandler(db, rdb))
	mux.HandleFunc("/users/me", handlers.GetMeHandler(db, rdb))
	mux.HandleFunc("/users/logout", handlers.LogoutUserHandler(db, rdb))
	mux.HandleFunc("/users/my-tickets", handlers.GetMyTicketsHandler(db, rdb))

	mux.HandleFunc("/events/create", handlers.CreateEventHandler(db, rdb))
	mux.HandleFunc("/events/update", handlers.UpdateEventHandler(db, rdb))
	mux.HandleFunc("/events/cancel", handlers.CancelEventHandler(db, rdb))
	mux.HandleFunc("/events/list", handlers.ListEventsHandler(db, rdb))
	mux.HandleFunc("/events/detail", handlers.GetEventDetailHandler(db, rdb))

	mux.HandleFunc("/public/events/list", handlers.PublicListEventsHandler(db))
	mux.HandleFunc("/public/events/detail", handlers.PublicEventDetailHandler(db))
	mux.HandleFunc("/public/events/calendar.ics", handlers.GetEventICSHandler(db))

	mux.HandleFunc("/public/checkout/create-intent", handlers.CreatePaymentIntentHandler(db, rdb))
	mux.HandleFunc("/public/checkout/confirm", handlers.ConfirmCheckoutHandler(db, rdb))
	mux.HandleFunc("/public/checkout/create-session", handlers.CreateCheckoutSessionHandler(db))
	mux.HandleFunc("/public/checkout/confirm-session", handlers.ConfirmSessionHandler(db, rdb))
	mux.HandleFunc("/public/tickets/download", handlers.DownloadTicketsPDFHandler(db))
	mux.HandleFunc("/public/tickets/download-zip", handlers.DownloadTicketsZipHandler(db))
	mux.HandleFunc("/public/tickets/validate", handlers.ValidateTicketScanHandler(db))
	mux.HandleFunc("/public/tickets/scan-stats", handlers.GetScanStatsHandler(db))
	mux.HandleFunc("/public/geo-currency", handlers.GetGeoCurrencyHandler(db, rdb))
	mux.HandleFunc("/public/test-smtp", handlers.TestSMTPHandler())
	mux.HandleFunc("/organizers/events/stats", handlers.GetOrganizerEventStatsHandler(db, rdb))
	mux.HandleFunc("/organizers/events/export-attendees-csv", handlers.ExportAttendeesCSVHandler(db, rdb))
	mux.HandleFunc("/dashboard/overview", handlers.GetDashboardOverviewHandler(db, rdb))

	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir("./uploads"))))

	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "3030" // Asegurar puerto 3030 por defecto
	}
	port := ":" + portStr

	// Middleware de CORS dinámico corregido
	corsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Reflejar el origen exacto del cliente para permitir cookies/tokens
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}

		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Roveni-Session, x-roveni-session, X-AltumPass-Session, x-altumpass-session, Accept, Origin, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")

		// Responder inmediatamente a las peticiones Preflight (OPTIONS)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		mux.ServeHTTP(w, r)
	})

	fmt.Printf("Servidor de Go escuchando en puerto %s\n", port)
	if err := http.ListenAndServe(port, corsHandler); err != nil {
		fmt.Printf("Error fatal al arrancar el servidor: %v\n", err)
	}
}
