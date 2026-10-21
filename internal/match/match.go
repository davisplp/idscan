// Package match looks up a student by phone number in the existing
// students table, tolerating whatever formatting happens to be stored
// there (local, international, with or without spaces/dashes).
package match

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"idscan/internal/db"
)

// Student is the subset of the students table an attendance flow needs.
// Deliberately not the whole row — this package has one job (find the
// student), not "be a students repository".
type Student struct {
	ID        string
	HubID     string
	FirstName string
	LastName  string
}

// ErrNotFound is returned when no student matches the given phone number.
// Callers should treat this as an expected outcome (surface an "unmatched"
// result to the instructor), not a server error.
var ErrNotFound = errors.New("match: no student found for this phone number")

// ByPhone finds a student by normalized E.164 Kenyan phone number
// (e.g. "+254740133967"). It matches against students.phone_number by
// stripping non-digit characters from the stored value and comparing the
// last 9 digits — this tolerates the mix of formats already present in a
// hand-entered student roster (+254..., 0740..., with spaces, etc.)
// without requiring a prior data-cleanup pass.
//
// See migrations/0002_phone_lookup_index.sql for the matching expression
// index that keeps this fast as the roster grows; without it, this query
// does a sequential scan, which is fine for a roster of a few thousand
// students but worth adding before that becomes a bottleneck.
func ByPhone(ctx context.Context, dbConn *sql.DB, normalizedPhone string) (Student, error) {
	suffix, err := last9Digits(normalizedPhone)
	if err != nil {
		return Student{}, fmt.Errorf("match: %w", err)
	}

	const query = `
		SELECT id, hub_id, first_name, last_name
		FROM public.students
		WHERE right(regexp_replace(phone_number, '\D', '', 'g'), 9) = $1
		  AND deleted_at IS NULL
		LIMIT 2` // LIMIT 2, not 1: lets us detect and report an ambiguous
	// match (two students sharing the same last-9-digits, e.g. a data
	// entry error) instead of silently picking whichever row Postgres
	// happens to return first.

	var rows *sql.Rows
	queryErr := db.Retry(func() error {
		var err error
		rows, err = dbConn.QueryContext(ctx, query, suffix)
		return err
	})
	if queryErr != nil {
		return Student{}, fmt.Errorf("match: querying students: %w", queryErr)
	}
	defer rows.Close()

	var students []Student
	for rows.Next() {
		var s Student
		if err := rows.Scan(&s.ID, &s.HubID, &s.FirstName, &s.LastName); err != nil {
			return Student{}, fmt.Errorf("match: scanning row: %w", err)
		}
		students = append(students, s)
	}
	if err := rows.Err(); err != nil {
		return Student{}, fmt.Errorf("match: iterating rows: %w", err)
	}

	switch len(students) {
	case 0:
		return Student{}, ErrNotFound
	case 1:
		return students[0], nil
	default:
		// Two students with the same phone suffix on file. This points to
		// a data quality issue in the roster (duplicate/shared number),
		// not something the scanner should silently guess at.
		return Student{}, fmt.Errorf(
			"match: ambiguous — %d students share phone suffix %q; needs manual resolution",
			len(students), suffix)
	}
}

// ByCardNumber finds a student by their printed card_number, matched
// case-insensitively. This is preferred over phone matching when
// available: it's guaranteed unique per card (once the
// students_card_number_uidx constraint is in place) and isn't subject to
// the formatting inconsistencies phone numbers accumulate in a
// hand-maintained roster.
//
// Both sides are compared by cardKey, which tolerates the OCR errors seen
// in production — Tesseract read "PLP-26-YOM-001-S9Z" as
// "PLP-26-YOM-0OOI-S9Z" — without general fuzzy matching: real card
// numbers in the roster are as close as two edits apart, so "allow one
// edit" could match a neighbour's card. cardKey only removes ambiguity
// that can't distinguish two real cards (checked collision-free against
// the roster when added), and the LIMIT 2 check below still refuses to
// guess if a collision ever appears.
func ByCardNumber(ctx context.Context, dbConn *sql.DB, cardNumber string) (Student, error) {
	if cardNumber == "" {
		return Student{}, fmt.Errorf("match: empty card number")
	}

	query := `
		SELECT id, hub_id, first_name, last_name
		FROM public.students
		WHERE ` + cardKey("card_number") + ` = ` + cardKey("$1") + `
		  AND deleted_at IS NULL
		LIMIT 2`

	var rows *sql.Rows
	queryErr := db.Retry(func() error {
		var err error
		rows, err = dbConn.QueryContext(ctx, query, cardNumber)
		return err
	})
	if queryErr != nil {
		return Student{}, fmt.Errorf("match: querying students by card number: %w", queryErr)
	}
	defer rows.Close()

	var students []Student
	for rows.Next() {
		var s Student
		if err := rows.Scan(&s.ID, &s.HubID, &s.FirstName, &s.LastName); err != nil {
			return Student{}, fmt.Errorf("match: scanning row: %w", err)
		}
		students = append(students, s)
	}
	if err := rows.Err(); err != nil {
		return Student{}, fmt.Errorf("match: iterating rows: %w", err)
	}

	switch len(students) {
	case 0:
		return Student{}, ErrNotFound
	case 1:
		return students[0], nil
	default:
		return Student{}, fmt.Errorf(
			"match: ambiguous — %d students share card number %q; needs manual resolution",
			len(students), cardNumber)
	}
}

// cardKey is the SQL expression card numbers are compared by: uppercased,
// OCR look-alikes folded (O -> 0, I and L -> 1, applied to the whole code
// so letters like "YOM" fold the same on both sides), and leading zeros
// dropped from each dash-separated segment, so an extra or missing pad
// zero in the sequence number still matches ("0OOI" -> "0001" -> "1",
// same as "001"). Every segment must otherwise match exactly, including
// the trailing check code.
func cardKey(expr string) string {
	return `regexp_replace(translate(upper(` + expr + `), 'OIL', '011'), '(^|-)0+([0-9])', '\1\2', 'g')`
}

func last9Digits(normalizedPhone string) (string, error) {
	digits := ""
	for _, r := range normalizedPhone {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	if len(digits) < 9 {
		return "", fmt.Errorf("phone number %q has fewer than 9 digits", normalizedPhone)
	}
	return digits[len(digits)-9:], nil
}
