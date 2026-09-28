// Package attendance implements the "scan marks attendance" business rule:
// record every scan attempt, match it to a student, and mark attendance —
// but only once per student per day, no matter how many times their card
// is scanned.
package attendance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"idscan/internal/db"
	"idscan/internal/match"
)

// Status describes the outcome of a single scan-and-mark attempt.
type Status string

const (
	// StatusMarked: a new attendance record was created for this student today.
	StatusMarked Status = "marked"
	// StatusAlreadyMarked: this student was already marked present today;
	// no new record was created, but this is a successful outcome, not an
	// error — instructors will often re-scan a card by accident.
	StatusAlreadyMarked Status = "already_marked"
	// StatusUnmatched: OCR found a phone number, but no student record
	// matches it. Needs manual resolution (typo on the card, unregistered
	// student, OCR misread, etc.).
	StatusUnmatched Status = "unmatched"
)

// MatchMethod records which field successfully identified the student,
// for both the instructor-facing response and future accuracy reporting
// (e.g. "what fraction of scans match via card vs phone").
type MatchMethod string

const (
	MatchMethodCard  MatchMethod = "card_number"
	MatchMethodPhone MatchMethod = "phone"
	MatchMethodNone  MatchMethod = ""
)

// Result is what the API layer needs to tell the instructor what happened.
type Result struct {
	Status           Status
	Student          *match.Student
	MatchedVia       MatchMethod
	MarkedAt         time.Time
	PreviousMarkedAt time.Time // only set when Status == StatusAlreadyMarked
}

// ScanInput bundles everything OCR extracted from one scan. A struct
// rather than positional parameters because this has grown past the
// point where positional string arguments stay readable — and it keeps
// this signature stable as more fields get added later.
type ScanInput struct {
	RawOCRText      string
	RawPhoneMatch   string // as read, before normalization; "" if not found
	NormalizedPhone string // "" if not found
	RawCardNumber   string // "" if not found
	Confidence      float64
	// ScanningHubID is the hub whose LOGIN SESSION performed this scan —
	// set by the authenticated hub session (see internal/hubauth), NOT
	// derived from the matched student. Empty if scanning is unauthenticated
	// (e.g. running without hub auth configured).
	ScanningHubID string
}

// nairobi is loaded once; attendance "days" are defined in the hubs' local
// time zone regardless of what time zone the server itself runs in.
var nairobi = mustLoadNairobi()

func mustLoadNairobi() *time.Location {
	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		// Africa/Nairobi has no DST and has been stable for decades; this
		// should never fail on a system with a normal tzdata install. If
		// it does, fall back to a fixed +3 offset rather than crashing the
		// whole service over a missing timezone database.
		return time.FixedZone("EAT", 3*60*60)
	}
	return loc
}

// RecordScan always inserts a row into scans (the audit trail exists
// whether or not this scan led to a match), then attempts to match and
// mark attendance.
//
// Matching order: card number first, phone number as fallback. The card
// number is an exact, unambiguous identifier once populated (see
// migrations/0003 in the students schema); phone matching stays as the
// fallback for cards where card_number hasn't been backfilled yet, or
// where the card's identifier field wasn't legible in the photo.
func RecordScan(ctx context.Context, dbConn *sql.DB, in ScanInput) (Result, error) {
	var student *match.Student
	var matchedVia MatchMethod

	if in.RawCardNumber != "" {
		s, err := match.ByCardNumber(ctx, dbConn, in.RawCardNumber)
		switch {
		case err == nil:
			student = &s
			matchedVia = MatchMethodCard
		case errors.Is(err, match.ErrNotFound):
			// fall through to try phone below
		default:
			return Result{}, fmt.Errorf("attendance: matching card number: %w", err)
		}
	}

	if student == nil && in.NormalizedPhone != "" {
		s, err := match.ByPhone(ctx, dbConn, in.NormalizedPhone)
		switch {
		case err == nil:
			student = &s
			matchedVia = MatchMethodPhone
		case errors.Is(err, match.ErrNotFound):
			// leave student nil; recorded as unmatched below
		default:
			return Result{}, fmt.Errorf("attendance: matching phone: %w", err)
		}
	}

	var matchedStudentID sql.NullString
	if student != nil {
		matchedStudentID = sql.NullString{String: student.ID, Valid: true}
	}

	var rawCardNumber sql.NullString
	if in.RawCardNumber != "" {
		rawCardNumber = sql.NullString{String: in.RawCardNumber, Valid: true}
	}

	var scanID string
	insertErr := db.Retry(func() error {
		return dbConn.QueryRowContext(ctx, `
			INSERT INTO public.scans (raw_ocr_text, raw_match, normalized_phone, card_number, matched_via, confidence, matched_student_id, scanning_hub_tag_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id`,
			in.RawOCRText, in.RawPhoneMatch, in.NormalizedPhone, rawCardNumber,
			nullIfEmpty(string(matchedVia)), in.Confidence, matchedStudentID, nullIfEmpty(in.ScanningHubID),
		).Scan(&scanID)
	})
	// NOTE: unlike the attendance insert below, this one isn't fully
	// idempotent — if the connection reset after the INSERT committed but
	// before the response reached us, a retry here could log the same
	// scan attempt twice. That's an acceptable tradeoff for an audit
	// table (occasional duplicate log row) versus the alternative of
	// failing the whole attendance-marking request over a transient
	// network blip.
	if insertErr != nil {
		return Result{}, fmt.Errorf("attendance: recording scan: %w", insertErr)
	}

	if student == nil {
		return Result{Status: StatusUnmatched}, nil
	}

	result, err := markAttendance(ctx, dbConn, *student, scanID, in.ScanningHubID)
	if err != nil {
		return Result{}, err
	}
	result.MatchedVia = matchedVia
	return result, nil
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// markAttendance inserts an attendance_records row for today, relying on
// the database's UNIQUE (student_id, session_date) constraint to make
// "already marked today" a normal, race-safe outcome rather than
// something the application has to check-then-insert for (which would
// have a race window between two near-simultaneous scans of the same
// card at two different entrances).
func markAttendance(ctx context.Context, dbConn *sql.DB, student match.Student, scanID, scanningHubID string) (Result, error) {
	now := time.Now().In(nairobi)
	sessionDate := now.Format("2006-01-02")

	var (
		attendanceID string
		scannedAt    time.Time
		inserted     bool
	)
	// Safe to retry: ON CONFLICT DO NOTHING means running this twice has
	// the same effect as running it once, whether the "conflict" comes
	// from a genuine duplicate scan today or from a retried request after
	// a dropped connection.
	err := db.Retry(func() error {
		return dbConn.QueryRowContext(ctx, `
			INSERT INTO public.attendance_records (student_id, hub_id, session_date, scan_id, scanning_hub_tag_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (student_id, session_date) DO NOTHING
			RETURNING id, scanned_at`,
			student.ID, student.HubID, sessionDate, scanID, nullIfEmpty(scanningHubID),
		).Scan(&attendanceID, &scannedAt)
	})

	switch {
	case err == nil:
		inserted = true
	case errors.Is(err, sql.ErrNoRows):
		// ON CONFLICT DO NOTHING with no matching RETURNING row means a
		// record for this student today already existed — not an error.
		inserted = false
	default:
		return Result{}, fmt.Errorf("attendance: inserting attendance record: %w", err)
	}

	if inserted {
		return Result{
			Status:   StatusMarked,
			Student:  &student,
			MarkedAt: scannedAt,
		}, nil
	}

	var previousMarkedAt time.Time
	err = db.Retry(func() error {
		return dbConn.QueryRowContext(ctx, `
			SELECT scanned_at FROM public.attendance_records
			WHERE student_id = $1 AND session_date = $2`,
			student.ID, sessionDate,
		).Scan(&previousMarkedAt)
	})
	if err != nil {
		return Result{}, fmt.Errorf("attendance: fetching existing record: %w", err)
	}

	return Result{
		Status:           StatusAlreadyMarked,
		Student:          &student,
		PreviousMarkedAt: previousMarkedAt,
	}, nil
}
