// Command server runs the ID card scanning + attendance HTTP API.
//
// /api/scan is pure OCR extraction with no database involved, and works
// with no configuration beyond Tesseract being installed — useful for
// testing the scan pipeline in isolation. It is intentionally left
// unauthenticated: it performs no database writes and identifies no one.
//
// /api/attendance/scan additionally matches the extracted identifiers
// against the students table and marks attendance, so it requires
// DATABASE_URL to be set, AND requires a valid hub login session
// (POST /api/auth/hub-login first) — see internal/hubauth.
//
// See README.md for the full roadmap.
package main

import (
	"log"
	"net/http"
	"os"

	"idscan/internal/api"
	"idscan/internal/db"
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

		mux.HandleFunc("/api/auth/hub-tags", api.NewHubTagsHandler(conn))
		mux.HandleFunc("/api/auth/hub-login", api.NewHubLoginHandler(conn))
		mux.HandleFunc("/api/attendance/scan", api.RequireHubSession(conn, api.NewAttendanceScanHandler(conn)))
		log.Printf("database connected — /api/auth/hub-tags, /api/auth/hub-login and /api/attendance/scan enabled")

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
		mux.HandleFunc("/api/auth/hub-tags", unavailable)
		mux.HandleFunc("/api/auth/hub-login", unavailable)
		mux.HandleFunc("/api/attendance/scan", unavailable)
		log.Printf("DATABASE_URL not set — hub login and attendance marking disabled, /api/scan still works")
	}

	// Serves the camera-capture frontend at "/" (web/index.html) and the
	// plain file-upload dev fallback at "/test.html".
	mux.Handle("/", http.FileServer(http.Dir("./web")))

	addr := ":" + port
	log.Printf("idscan server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
