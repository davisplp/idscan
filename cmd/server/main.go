// Command server runs the ID card scanning + attendance HTTP API.
//
// /api/scan is pure OCR extraction with no database involved, and works
// with no configuration beyond Tesseract being installed — useful for
// testing the scan pipeline in isolation. It is intentionally left
// unauthenticated: it performs no database writes and identifies no one.
//
// /api/hubs/{hubId}/attendance/scan additionally matches the extracted
// identifiers against the students table and marks attendance for that
// hub. It requires DATABASE_URL and KEYCLOAK_ISSUER_URL, and an HMS
// Keycloak access token for a user allowed to act on the hub — see
// internal/kcauth.
//
// See README.md for the full roadmap.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"idscan/internal/api"
	"idscan/internal/db"
	"idscan/internal/kcauth"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/scan", api.ScanHandler)
	mux.HandleFunc("/healthz", api.HealthHandler)

	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		conn, err := db.Connect(dsn)
		if err != nil {
			log.Fatalf("failed to connect to database: %v", err)
		}
		defer conn.Close()

		if issuer := os.Getenv("KEYCLOAK_ISSUER_URL"); issuer != "" {
			verifier, err := kcauth.NewVerifier(context.Background(), issuer)
			if err != nil {
				log.Fatalf("failed to set up Keycloak token verification: %v", err)
			}
			mux.HandleFunc("POST /api/hubs/{hubId}/attendance/scan",
				api.RequireHubUser(verifier, conn, api.NewAttendanceScanHandler(conn)))
			log.Printf("database connected, Keycloak issuer %s — /api/hubs/{hubId}/attendance/scan enabled", issuer)
		} else {
			log.Printf("KEYCLOAK_ISSUER_URL not set — attendance scanning disabled")
		}

		if reportsKey := os.Getenv("REPORTS_API_KEY"); reportsKey != "" {
			api.SetReportsAPIKey(reportsKey)
			mux.HandleFunc("GET /api/reports/hubs/{hubId}/attendance/stats",
				api.RequireServiceKey(api.NewHubAttendanceStatsHandler(conn)))
			mux.HandleFunc("GET /api/reports/hubs/{hubId}/attendance",
				api.RequireServiceKey(api.NewHubAttendanceListHandler(conn)))
			mux.HandleFunc("GET /api/reports/attendance/by-hub",
				api.RequireServiceKey(api.NewAttendanceByHubHandler(conn)))
			mux.HandleFunc("GET /api/reports/scans/reliability",
				api.RequireServiceKey(api.NewScanReliabilityHandler(conn)))
			log.Printf("REPORTS_API_KEY set — reporting endpoints enabled (per-hub stats/list, attendance/by-hub, scans/reliability)")
		} else {
			log.Printf("REPORTS_API_KEY not set — reporting endpoints disabled")
		}
	} else {
		unavailable := func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not configured (DATABASE_URL is not set)", http.StatusServiceUnavailable)
		}
		mux.HandleFunc("POST /api/hubs/{hubId}/attendance/scan", unavailable)
		log.Printf("DATABASE_URL not set — attendance marking disabled, /api/scan still works")
	}

	// Serves the plain file-upload OCR test page at "/test.html". Attendance
	// scanning itself happens in hms-frontend, behind the HMS login.
	mux.Handle("/", http.FileServer(http.Dir("./web")))

	addr := ":" + port
	log.Printf("idscan server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
