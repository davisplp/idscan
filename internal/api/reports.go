package api

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"idscan/internal/reports"
)

// reportsAPIKey is set once at startup from an env var (see main.go).
// Empty means reporting endpoints are disabled entirely, same pattern as
// DATABASE_URL gating the rest of the DB-backed routes.
var reportsAPIKey string

// SetReportsAPIKey configures the key RequireServiceKey checks against.
func SetReportsAPIKey(key string) { reportsAPIKey = key }

// RequireServiceKey protects the reporting endpoints. This is
// service-to-service auth (the hms-frontend Next.js server calling this
// API from its own backend, never from a browser) — deliberately
// separate from hubauth's PIN-based sessions, which authenticate a
// physical scanning device, not a reporting consumer. Mixing the two
// would be confusing and wrong: a hub's scanning PIN should not also be
// able to read every hub's attendance data.
func RequireServiceKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-API-Key")
		if reportsAPIKey == "" || provided == "" ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(reportsAPIKey)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": false, "message": "invalid or missing API key"})
			return
		}
		next(w, r)
	}
}

// Pagination mirrors hms-frontend's existing Pageable shape
// ({page, size, count}) so the frontend's parsing code needs no
// special-casing for this API versus its own backend's responses.
type Pagination struct {
	Page  int `json:"page"`
	Size  int `json:"size"`
	Count int `json:"count"`
}

// reportsResponse mirrors hms-frontend's ApiResponse<T> shape
// (data/status/meta) for the same reason — deliberate cross-repo
// consistency, not an accident.
type reportsResponse struct {
	Status bool `json:"status"`
	Data   any  `json:"data,omitempty"`
	Meta   any  `json:"meta,omitempty"`
}

// NewHubAttendanceStatsHandler builds GET /api/reports/hubs/{hubId}/attendance/stats.
func NewHubAttendanceStatsHandler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hubID := r.PathValue("hubId")
		if hubID == "" {
			writeJSON(w, http.StatusBadRequest, reportsResponse{Status: false})
			return
		}

		stats, err := reports.GetStats(r.Context(), dbConn, hubID, time.Now())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, reportsResponse{Status: false})
			return
		}
		writeJSON(w, http.StatusOK, reportsResponse{Status: true, Data: stats})
	}
}

// NewHubAttendanceListHandler builds GET /api/reports/hubs/{hubId}/attendance.
// Query params: page (0-based), size, from, to (YYYY-MM-DD).
func NewHubAttendanceListHandler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hubID := r.PathValue("hubId")
		if hubID == "" {
			writeJSON(w, http.StatusBadRequest, reportsResponse{Status: false})
			return
		}

		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("size"))

		records, total, err := reports.List(r.Context(), dbConn, hubID, reports.ListParams{
			Page: page,
			Size: size,
			From: q.Get("from"),
			To:   q.Get("to"),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, reportsResponse{Status: false})
			return
		}
		if records == nil {
			records = []reports.Record{} // JSON [] not null, friendlier for the frontend
		}

		effectiveSize := size
		if effectiveSize <= 0 || effectiveSize > 200 {
			effectiveSize = 25
		}

		writeJSON(w, http.StatusOK, reportsResponse{
			Status: true,
			Data:   records,
			Meta: map[string]any{
				"pagination": Pagination{Page: page, Size: effectiveSize, Count: total},
			},
		})
	}
}
