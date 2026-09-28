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
	"fmt"
	"time"
)

// Stats is the at-a-glance summary for a hub's attendance page.
type Stats struct {
	PresentTodayCount    int `json:"presentTodayCount"`
	PresentThisWeekCount int `json:"presentThisWeekCount"`
	TotalStudentsCount   int `json:"totalStudentsCount"`
	NeverScannedCount    int `json:"neverScannedCount"`
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

	return s, nil
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
