// Package hubauth implements hub-level login: a hub authenticates with a
// login_code and a short numeric PIN, and receives a session token used
// on subsequent requests. This identifies WHICH HUB IS SCANNING, separate
// from which hub a matched student belongs to.
package hubauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"idscan/internal/db"
)

// SessionDuration is how long a session stays valid after its last use —
// a sliding window, not a fixed expiry from login time. Matches the
// product decision: a hub logs in once each morning/session and stays
// logged in through a few hours of scanning without needing to re-auth
// between individual scans, but a genuinely idle/forgotten session does
// eventually expire.
const SessionDuration = 4 * time.Hour

// maxFailedAttempts and lockoutDuration are the real defense against a
// short numeric PIN's low entropy — a 4-6 digit PIN (10,000-1,000,000
// combinations) is not meaningfully protected by the hash algorithm
// alone; what stops brute-forcing it is refusing to keep guessing after a
// handful of wrong attempts.
const (
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute
)

var (
	ErrInvalidCredentials = errors.New("hubauth: invalid login code or PIN")
	ErrAccountLocked      = errors.New("hubauth: too many failed attempts, try again later")
	ErrInvalidSession     = errors.New("hubauth: invalid or expired session")
)

// HashPIN returns a bcrypt hash of a PIN, for storing in hubs.pin_hash.
// This is an admin/setup-time operation (assigning a hub its PIN), not
// part of the request path.
func HashPIN(pin string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hubauth: hashing PIN: %w", err)
	}
	return string(h), nil
}

// Login validates a login_code + PIN and, on success, creates a new
// session and returns its bearer token (returned to the caller once —
// only its hash is ever stored).
func Login(ctx context.Context, dbConn *sql.DB, loginCode, pin string) (token string, hubID string, hubName string, err error) {
	var (
		pinHash        string
		failedAttempts int
		lockedUntil    sql.NullTime
	)

	row := dbConn.QueryRowContext(ctx, `
		SELECT id, name, pin_hash, failed_login_attempts, locked_until
		FROM public.hub_tags
		WHERE code = $1`,
		loginCode,
	)
	if err := row.Scan(&hubID, &hubName, &pinHash, &failedAttempts, &lockedUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", ErrInvalidCredentials
		}
		return "", "", "", fmt.Errorf("hubauth: looking up hub: %w", err)
	}

	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return "", "", "", ErrAccountLocked
	}

	if pinHash == "" || bcrypt.CompareHashAndPassword([]byte(pinHash), []byte(pin)) != nil {
		if err := recordFailedAttempt(ctx, dbConn, hubID, failedAttempts+1); err != nil {
			return "", "", "", fmt.Errorf("hubauth: recording failed attempt: %w", err)
		}
		return "", "", "", ErrInvalidCredentials
	}

	// Success: reset the failure counter and issue a session.
	if _, err := dbConn.ExecContext(ctx, `
		UPDATE public.hub_tags SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1`,
		hubID,
	); err != nil {
		return "", "", "", fmt.Errorf("hubauth: resetting lockout state: %w", err)
	}

	token, tokenHash, err := generateToken()
	if err != nil {
		return "", "", "", err
	}

	_, err = dbConn.ExecContext(ctx, `
		INSERT INTO public.hub_tag_sessions (hub_tag_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		hubID, tokenHash, time.Now().Add(SessionDuration),
	)
	if err != nil {
		return "", "", "", fmt.Errorf("hubauth: creating session: %w", err)
	}

	return token, hubID, hubName, nil
}

func recordFailedAttempt(ctx context.Context, dbConn *sql.DB, hubID string, newCount int) error {
	if newCount >= maxFailedAttempts {
		_, err := dbConn.ExecContext(ctx, `
			UPDATE public.hub_tags SET failed_login_attempts = $2, locked_until = $3 WHERE id = $1`,
			hubID, newCount, time.Now().Add(lockoutDuration),
		)
		return err
	}
	_, err := dbConn.ExecContext(ctx, `
		UPDATE public.hub_tags SET failed_login_attempts = $2 WHERE id = $1`,
		hubID, newCount,
	)
	return err
}

// ValidateAndTouch checks a bearer token and, if valid, extends its
// expiration (sliding window) — a hub actively scanning stays logged in;
// one left untouched for SessionDuration expires.
func ValidateAndTouch(ctx context.Context, dbConn *sql.DB, token string) (hubID string, err error) {
	tokenHash := hashToken(token)

	var expiresAt time.Time
	getErr := db.Retry(func() error {
		return dbConn.QueryRowContext(ctx, `
			SELECT hub_tag_id, expires_at FROM public.hub_tag_sessions WHERE token_hash = $1`,
			tokenHash,
		).Scan(&hubID, &expiresAt)
	})
	if getErr != nil {
		if errors.Is(getErr, sql.ErrNoRows) {
			return "", ErrInvalidSession
		}
		return "", fmt.Errorf("hubauth: looking up session: %w", getErr)
	}

	if time.Now().After(expiresAt) {
		return "", ErrInvalidSession
	}

	// Sliding expiration: extend on use. Best-effort — if this update
	// fails, the request still proceeds with the session it already
	// validated; it just won't have extended this particular touch.
	_, _ = dbConn.ExecContext(ctx, `
		UPDATE public.hub_tag_sessions SET expires_at = $2, last_seen_at = now() WHERE token_hash = $1`,
		tokenHash, time.Now().Add(SessionDuration),
	)

	return hubID, nil
}

// Tag is a scan-session identity available to log in as, shown in the
// frontend's dropdown.
type Tag struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ListTags returns all hub_tags, ordered by code, for populating a login
// dropdown. This is read-only, contains no secrets (never pin_hash), and
// is safe to expose without authentication — knowing the list of valid
// codes doesn't help someone log in without the PIN.
func ListTags(ctx context.Context, dbConn *sql.DB) ([]Tag, error) {
	rows, err := dbConn.QueryContext(ctx, `SELECT code, name FROM public.hub_tags ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("hubauth: listing tags: %w", err)
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Code, &t.Name); err != nil {
			return nil, fmt.Errorf("hubauth: scanning tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func generateToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("hubauth: generating token: %w", err)
	}
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
