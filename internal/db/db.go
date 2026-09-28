// Package db provides a single shared *sql.DB connection pool, configured
// from a standard Postgres connection string.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" driver
)

// Connect opens a connection pool and verifies connectivity with a ping.
// databaseURL is a standard Postgres URL, e.g.
// "postgres://user:pass@host:5432/dbname?sslmode=disable".
func Connect(databaseURL string) (*sql.DB, error) {
	conn, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: opening connection: %w", err)
	}

	// Conservative pool defaults for a small scanning service — this isn't
	// a high-throughput API, and a handful of connections is plenty for
	// instructors scanning cards at a hub entrance.
	conn.SetMaxOpenConns(10)

	// Deliberately 0, not a small positive number: this database sits
	// behind a proxy/pooler (nonstandard port) that has been observed
	// killing connections after as little as ~30 seconds idle — shorter
	// than any idle-timeout value we could safely tune to. Rather than
	// guess at the proxy's exact timeout, this just never lets a
	// connection sit idle in the pool waiting to be killed: each request
	// opens fresh. For a scanning app doing one request every 30-90
	// seconds, the added latency of a new connection is negligible.
	conn.SetMaxIdleConns(0)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: ping failed: %w", err)
	}

	return conn, nil
}

// IsRetryable reports whether err looks like a transient connection
// problem (reset, broken pipe, timeout) rather than a real query error
// (bad SQL, constraint violation, etc.). Only errors that pass this check
// should ever be retried — retrying a genuine query error would just fail
// the same way twice.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "bad connection") ||
		strings.Contains(msg, "EOF")
}

// Retry runs fn, retrying up to 3 times total with a short increasing
// backoff (100ms, 300ms) if it fails with a retryable error.
//
// This was originally written assuming a fixed idle-connection timeout
// (one retry to get a fresh connection would be enough), but real-world
// testing showed failures with no consistent idle-time pattern — closer
// to intermittent network flakiness on the path to the database than a
// deterministic timeout. Multiple attempts with backoff are a reasonable
// mitigation for that regardless of the exact root cause, though the real
// fix, if the network path itself turns out to be unreliable, is fixing
// that path (see README.md "Known issues").
//
// Only wrap read-only or idempotent operations with this (a plain SELECT,
// or an INSERT ... ON CONFLICT DO NOTHING) — retrying a non-idempotent
// write after an ambiguous network failure (request may have succeeded
// server-side before the response was lost) can duplicate the write.
func Retry(fn func() error) error {
	backoffs := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil || !IsRetryable(err) || attempt >= len(backoffs) {
			return err
		}
		time.Sleep(backoffs[attempt])
	}
}
