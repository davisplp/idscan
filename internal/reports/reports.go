// Package reports serves read-only attendance reporting data, built
// entirely on top of tables that already exist for the scanning feature
// (attendance_records, scans, students, hub_tags) — no new schema.
//
// Scoped by hub_id: the REAL hubs.id (the one students.hub_id already
// references, and the same one hms-frontend's /hubs/[hub-id] routes use)
// — not hub_tags.id. Those are deliberately different things: hub_id is
// "which hub this student is enrolled at"; scanning_hub_tag_id (surfaced
// per-record here) is "which physical scanning session actually
// performed this scan", which can legitimately differ (see the
// AIY-scans-a-YOM-student case worked through earlier in this project).
package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Stats is the at-a-glance summary for a hub's attendance page.
type Stats struct {
	PresentTodayCount    int `json:"presentTodayCount"`
	PresentThisWeekCount int `json:"presentThisWeekCount"`
	TotalStudentsCount   int `json:"totalStudentsCount"`
	NeverScannedCount    int `json:"neverScannedCount"`

	// HomeHubTagCode/Name identify this hub's OWN scanning device/session
	// (set via migration 0008's hub_tags.hub_id mapping — see that
	// migration for why this is an explicit, admin-set fact rather than
	// something derived from the card-number data). Both are nil until
	// someone sets hub_tags.hub_id = this hub for the relevant tag.
	// The frontend can compare this against a record's own
	// scanningHubTagCode to render a "visiting hub" badge: same code =
	// scanned by this hub's own device; different code = another hub's
	// device performed the scan.
	HomeHubTagCode *string `json:"homeHubTagCode"`
	HomeHubTagName *string `json:"homeHubTagName"`
}

// GetStats computes summary counts for one hub. "Today" and "this week"
// are evaluated in Africa/Nairobi time, matching how attendance_records
// .session_date is itself computed (see internal/attendance) — so these
// numbers agree with what a scan just did, not with whatever timezone
// this server process happens to run in.
func GetStats(ctx context.Context, dbConn *sql.DB, hubID string, now time.Time) (Stats, error) {
	var s Stats
	today := now.Format("2006-01-02")
	weekAgo := now.AddDate(0, 0, -6).Format("2006-01-02") // last 7 days inclusive of today

	err := dbConn.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE ar.session_date = $2) AS present_today,
			count(*) FILTER (WHERE ar.session_date >= $3) AS present_this_week
		FROM public.attendance_records ar
		WHERE ar.hub_id = $1`,
		hubID, today, weekAgo,
	).Scan(&s.PresentTodayCount, &s.PresentThisWeekCount)
	if err != nil {
		return Stats{}, fmt.Errorf("reports: computing attendance counts: %w", err)
	}

	err = dbConn.QueryRowContext(ctx, `
		SELECT
			count(*) AS total_students,
			count(*) FILTER (WHERE ar.student_id IS NULL) AS never_scanned
		FROM public.students s
		LEFT JOIN (
			SELECT DISTINCT student_id FROM public.attendance_records
		) ar ON ar.student_id = s.id
		WHERE s.hub_id = $1 AND s.deleted_at IS NULL`,
		hubID,
	).Scan(&s.TotalStudentsCount, &s.NeverScannedCount)
	if err != nil {
		return Stats{}, fmt.Errorf("reports: computing student counts: %w", err)
	}

	// Best-effort: if multiple tags somehow claim the same hub_id (not
	// expected, but not prevented at the DB level either), just take one
	// deterministically rather than erroring the whole stats call over
	// a setup mistake elsewhere.
	err = dbConn.QueryRowContext(ctx, `
		SELECT code, name FROM public.hub_tags WHERE hub_id = $1 ORDER BY code LIMIT 1`,
		hubID,
	).Scan(&s.HomeHubTagCode, &s.HomeHubTagName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Stats{}, fmt.Errorf("reports: looking up home hub tag: %w", err)
	}

	return s, nil
}

// HubSummary is one hub's attendance breakdown, as returned by
// AttendanceByHub — the same shape as Stats, plus which hub this is, so
// the whole organization's attendance-per-hub picture can be fetched in
// one call instead of one request per hub.
type HubSummary struct {
	HubID                string  `json:"hubId"`
	HubName              string  `json:"hubName"`
	PresentTodayCount    int     `json:"presentTodayCount"`
	PresentThisWeekCount int     `json:"presentThisWeekCount"`
	TotalStudentsCount   int     `json:"totalStudentsCount"`
	NeverScannedCount    int     `json:"neverScannedCount"`
	HomeHubTagCode       *string `json:"homeHubTagCode"`
	HomeHubTagName       *string `json:"homeHubTagName"`
}

// AttendanceByHub answers "attendance per hub" directly: one row per real
// hub that has at least one active student, each with the same counts
// Stats computes for a single hub. Deliberately excludes hubs with zero
// students (test/placeholder hub rows, of which this schema has several)
// rather than padding the response with rows that carry no signal.
func AttendanceByHub(ctx context.Context, dbConn *sql.DB, now time.Time) ([]HubSummary, error) {
	today := now.Format("2006-01-02")
	weekAgo := now.AddDate(0, 0, -6).Format("2006-01-02")

	rows, err := dbConn.QueryContext(ctx, `
		WITH student_counts AS (
			SELECT hub_id,
				count(*) AS total_students,
				count(*) FILTER (
					WHERE NOT EXISTS (
						SELECT 1 FROM public.attendance_records ar WHERE ar.student_id = s.id
					)
				) AS never_scanned
			FROM public.students s
			WHERE s.deleted_at IS NULL
			GROUP BY hub_id
		),
		attendance_counts AS (
			SELECT hub_id,
				count(DISTINCT student_id) FILTER (WHERE session_date = $1) AS present_today,
				count(DISTINCT student_id) FILTER (WHERE session_date >= $2) AS present_this_week
			FROM public.attendance_records
			GROUP BY hub_id
		)
		SELECT
			h.id, h.name,
			coalesce(sc.total_students, 0), coalesce(sc.never_scanned, 0),
			coalesce(ac.present_today, 0), coalesce(ac.present_this_week, 0),
			ht.code, ht.name
		FROM public.hubs h
		JOIN student_counts sc ON sc.hub_id = h.id
		LEFT JOIN attendance_counts ac ON ac.hub_id = h.id
		LEFT JOIN public.hub_tags ht ON ht.hub_id = h.id
		WHERE h.deleted_at IS NULL
		ORDER BY h.name`,
		today, weekAgo,
	)
	if err != nil {
		return nil, fmt.Errorf("reports: querying attendance by hub: %w", err)
	}
	defer rows.Close()

	var summaries []HubSummary
	for rows.Next() {
		var s HubSummary
		if err := rows.Scan(
			&s.HubID, &s.HubName,
			&s.TotalStudentsCount, &s.NeverScannedCount,
			&s.PresentTodayCount, &s.PresentThisWeekCount,
			&s.HomeHubTagCode, &s.HomeHubTagName,
		); err != nil {
			return nil, fmt.Errorf("reports: scanning hub summary: %w", err)
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// Reliability summarizes how well OCR/matching is actually performing
// across all scans — a different question from attendance itself (see
// the original project notes on why the scans table exists as a
// standalone audit trail, independent of whether a scan led to a match).
type Reliability struct {
	TotalScans           int      `json:"totalScans"`
	MatchedCount         int      `json:"matchedCount"`
	MatchedViaCardCount  int      `json:"matchedViaCardCount"`
	MatchedViaPhoneCount int      `json:"matchedViaPhoneCount"`
	UnmatchedCount       int      `json:"unmatchedCount"`    // identifier read, but no student found
	NoIdentifierCount    int      `json:"noIdentifierCount"` // OCR found neither phone nor card number
	AverageConfidence    *float64 `json:"averageConfidence"`
}

// ScanReliability computes org-wide OCR/matching accuracy stats. Global
// only for now (not scoped per hub) — scans that don't match a student
// have no student hub_id to attribute to, and attributing by
// scanning_hub_tag_id's home hub would silently drop any scan from a tag
// with no hub_id configured yet, which would be a misleading partial
// picture rather than a genuinely hub-scoped one.
func ScanReliability(ctx context.Context, dbConn *sql.DB) (Reliability, error) {
	var r Reliability
	err := dbConn.QueryRowContext(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE matched_student_id IS NOT NULL),
			count(*) FILTER (WHERE matched_via = 'card_number'),
			count(*) FILTER (WHERE matched_via = 'phone'),
			count(*) FILTER (WHERE matched_student_id IS NULL AND (normalized_phone IS NOT NULL OR card_number IS NOT NULL)),
			count(*) FILTER (WHERE normalized_phone IS NULL AND card_number IS NULL),
			avg(confidence) FILTER (WHERE confidence >= 0)
		FROM public.scans`,
	).Scan(
		&r.TotalScans, &r.MatchedCount, &r.MatchedViaCardCount, &r.MatchedViaPhoneCount,
		&r.UnmatchedCount, &r.NoIdentifierCount, &r.AverageConfidence,
	)
	if err != nil {
		return Reliability{}, fmt.Errorf("reports: computing scan reliability: %w", err)
	}
	return r, nil
}

// Record is one attendance entry as shown in the reporting table.
type Record struct {
	ID                 string  `json:"id"`
	StudentID          string  `json:"studentId"`
	StudentFirstName   string  `json:"studentFirstName"`
	StudentLastName    string  `json:"studentLastName"`
	SessionDate        string  `json:"sessionDate"`
	ScannedAt          string  `json:"scannedAt"`
	MatchedVia         *string `json:"matchedVia"`
	ScanningHubTagCode *string `json:"scanningHubTagCode"`
	ScanningHubTagName *string `json:"scanningHubTagName"`
}

// ListParams controls pagination and date filtering. Page is 0-based,
// matching this codebase's existing HubReportSearchQueryParams convention
// (see hms-frontend's lib/actions/hub_reports/types.ts).
type ListParams struct {
	Page int
	Size int
	From string // "YYYY-MM-DD", inclusive; "" means no lower bound
	To   string // "YYYY-MM-DD", inclusive; "" means no upper bound
}

// List returns one page of attendance records for a hub, newest first,
// plus the total count for pagination.
func List(ctx context.Context, dbConn *sql.DB, hubID string, p ListParams) ([]Record, int, error) {
	if p.Size <= 0 || p.Size > 200 {
		p.Size = 25
	}
	if p.Page < 0 {
		p.Page = 0
	}

	var total int
	err := dbConn.QueryRowContext(ctx, `
		SELECT count(*)
		FROM public.attendance_records ar
		WHERE ar.hub_id = $1
		  AND ($2 = '' OR ar.session_date >= $2::date)
		  AND ($3 = '' OR ar.session_date <= $3::date)`,
		hubID, p.From, p.To,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("reports: counting records: %w", err)
	}

	rows, err := dbConn.QueryContext(ctx, `
		SELECT
			ar.id, ar.student_id, s.first_name, s.last_name,
			ar.session_date::text, ar.scanned_at,
			sc.matched_via,
			ht.code, ht.name
		FROM public.attendance_records ar
		JOIN public.students s ON s.id = ar.student_id
		LEFT JOIN public.scans sc ON sc.id = ar.scan_id
		LEFT JOIN public.hub_tags ht ON ht.id = ar.scanning_hub_tag_id
		WHERE ar.hub_id = $1
		  AND ($2 = '' OR ar.session_date >= $2::date)
		  AND ($3 = '' OR ar.session_date <= $3::date)
		ORDER BY ar.scanned_at DESC
		LIMIT $4 OFFSET $5`,
		hubID, p.From, p.To, p.Size, p.Page*p.Size,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("reports: querying records: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var (
			r         Record
			scannedAt time.Time
		)
		if err := rows.Scan(
			&r.ID, &r.StudentID, &r.StudentFirstName, &r.StudentLastName,
			&r.SessionDate, &scannedAt,
			&r.MatchedVia,
			&r.ScanningHubTagCode, &r.ScanningHubTagName,
		); err != nil {
			return nil, 0, fmt.Errorf("reports: scanning record: %w", err)
		}
		r.ScannedAt = scannedAt.Format(time.RFC3339)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reports: iterating records: %w", err)
	}

	return records, total, nil
}
