package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type ExchangeRatesResponse struct {
	Result string             `json:"result"`
	Rates  map[string]float64 `json:"rates"`
}

var (
	ratesCache     map[string]float64
	ratesCacheTime time.Time
	ratesMutex     sync.RWMutex
)

// countryToCurrency maps 2-letter ISO country code to currency code
var countryToCurrency = map[string]string{
	"MX": "MXN",
	"CO": "COP",
	"AR": "ARS",
	"CL": "CLP",
	"PE": "PEN",
	"BR": "BRL",
	"ES": "EUR",
	"FR": "EUR",
	"DE": "EUR",
	"IT": "EUR",
	"PT": "EUR",
	"NL": "EUR",
	"BE": "EUR",
	"AT": "EUR",
	"GB": "GBP",
	"CA": "CAD",
	"US": "USD",
}

// GetLiveRates fetches live exchange rates with 1-hour in-memory cache and Redis backup
func GetLiveRates(rdb *redis.Client) map[string]float64 {
	ratesMutex.RLock()
	if ratesCache != nil && time.Since(ratesCacheTime) < 1*time.Hour {
		defer ratesMutex.RUnlock()
		return ratesCache
	}
	ratesMutex.RUnlock()

	ratesMutex.Lock()
	defer ratesMutex.Unlock()

	// Check again under write lock
	if ratesCache != nil && time.Since(ratesCacheTime) < 1*time.Hour {
		return ratesCache
	}

	// Default fallback rates
	fallbackRates := map[string]float64{
		"USD": 1.0,
		"MXN": 18.50,
		"COP": 4100.00,
		"ARS": 950.00,
		"CLP": 940.00,
		"EUR": 0.92,
		"GBP": 0.78,
		"CAD": 1.36,
		"BRL": 5.50,
		"PEN": 3.75,
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://open.er-api.com/v6/latest/USD")
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var ratesResp ExchangeRatesResponse
		if errDecode := json.NewDecoder(resp.Body).Decode(&ratesResp); errDecode == nil && len(ratesResp.Rates) > 0 {
			ratesCache = ratesResp.Rates
			ratesCacheTime = time.Now()
			return ratesCache
		}
	}

	ratesCache = fallbackRates
	ratesCacheTime = time.Now()
	return ratesCache
}

// GetGeoCurrencyHandler returns client's detected currency based on IP, CF headers, and live exchange rates
func GetGeoCurrencyHandler(db interface{}, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		clientIP := r.Header.Get("CF-Connecting-IP")
		if clientIP == "" {
			clientIP = r.Header.Get("X-Forwarded-For")
			if clientIP != "" {
				clientIP = strings.TrimSpace(strings.Split(clientIP, ",")[0])
			}
		}
		if clientIP == "" {
			clientIP = r.Header.Get("X-Real-IP")
		}
		if clientIP == "" {
			clientIP = strings.Split(r.RemoteAddr, ":")[0]
		}

		countryCode := strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
		currency := "USD"

		if countryCode != "" && countryCode != "XX" && countryCode != "T1" {
			if curr, ok := countryToCurrency[countryCode]; ok {
				currency = curr
			}
		} else {
			// Query external GeoIP service if IP is public
			if clientIP != "" && clientIP != "127.0.0.1" && clientIP != "::1" && !strings.HasPrefix(clientIP, "192.168.") && !strings.HasPrefix(clientIP, "10.") {
				client := &http.Client{Timeout: 2 * time.Second}
				geoResp, err := client.Get(fmt.Sprintf("https://ipapi.co/%s/json/", clientIP))
				if err == nil && geoResp.StatusCode == http.StatusOK {
					var geoData struct {
						Country  string `json:"country"`
						Currency string `json:"currency"`
					}
					if errDecode := json.NewDecoder(geoResp.Body).Decode(&geoData); errDecode == nil && geoData.Currency != "" {
						currency = geoData.Currency
						if geoData.Country != "" {
							countryCode = geoData.Country
						}
					}
					geoResp.Body.Close()
				}
			}
		}

		rates := GetLiveRates(rdb)

		writeJSON(w, http.StatusOK, map[string]any{
			"ip":          clientIP,
			"country":     countryCode,
			"currency":    currency,
			"rates":       rates,
			"timestamp":   time.Now().Unix(),
		})
	}
}
