// Package kcauth authenticates scan requests with the HMS's own Keycloak
// login, replacing the earlier per-hub PIN sessions (internal/hubauth,
// now removed). A user who is logged in to HMS sends their Keycloak access
// token; this package verifies it and decides whether that user may scan
// for a given hub.
//
// The access rules deliberately mirror hms-backend's RequireHubAccess
// (internal/auth/middleware.go in that repo), reading the same users and
// user_hubs tables in the shared database, so "can this person act on
// this hub" has one answer across both services.
package kcauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"idscan/internal/db"
)

var (
	ErrInvalidToken = errors.New("kcauth: invalid or expired token")
	// ErrNoHMSAccess means the token is valid but carries no HMS group or
	// role — the same case hms-backend answers with 403 "no HMS access".
	ErrNoHMSAccess = errors.New("kcauth: user has no HMS access")
)

// User is the subset of a verified Keycloak access token this service needs.
type User struct {
	Subject string // Keycloak user ID; matches users.keycloak_user_id
	Email   string
	Name    string
	Group   string // effective HMS group, resolved the same way hms-backend does
}

// Verifier checks Keycloak access tokens against the realm's published keys.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier fetches the realm's OIDC discovery document, so issuerURL
// must be reachable at startup (e.g. https://<host>/realms/hms).
func NewVerifier(ctx context.Context, issuerURL string) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("kcauth: loading OIDC provider: %w", err)
	}
	// Keycloak access tokens carry aud=account and the client in azp, so
	// the audience check is skipped — same as hms-backend.
	return &Verifier{verifier: provider.Verifier(&oidc.Config{SkipClientIDCheck: true})}, nil
}

type accessClaims struct {
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	Groups []string `json:"groups"`
}

// Verify validates the token's signature and expiry and resolves the
// user's HMS group.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (User, error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	var c accessClaims
	if err := tok.Claims(&c); err != nil {
		return User{}, fmt.Errorf("%w: reading claims: %v", ErrInvalidToken, err)
	}

	group, ok := hmsGroup(c.Groups, c.RealmAccess.Roles)
	if !ok {
		return User{}, ErrNoHMSAccess
	}

	email := c.Email
	if email == "" {
		email = c.PreferredUsername
	}
	return User{Subject: tok.Subject, Email: email, Name: c.Name, Group: group}, nil
}

// groupPriority matches hms-backend's groupFromTokenGroups: a user in
// several groups is treated as the most privileged one.
var groupPriority = map[string]int{
	"super-admin":          9,
	"admin":                8,
	"manager":              7,
	"coordinator":          6,
	"super-instructor":     5,
	"instructor":           4,
	"facilitator":          3,
	"application-reviewer": 2,
	"tot-staff":            1,
}

// rolePriority is hms-backend's fallback for tokens with no group claim:
// realm roles, optionally prefixed "hms-".
var rolePriority = map[string]int{
	"admin":            5,
	"coordinator":      4,
	"super-instructor": 3,
	"instructor":       2,
	"facilitator":      1,
}

func hmsGroup(groups, roles []string) (string, bool) {
	best, found := 0, ""
	for _, g := range groups {
		g = strings.TrimPrefix(g, "/")
		if p := groupPriority[g]; p > best {
			best, found = p, g
		}
	}
	if found != "" {
		return found, true
	}
	for _, r := range roles {
		r = strings.TrimPrefix(r, "hms-")
		if p := rolePriority[r]; p > best {
			best, found = p, r
		}
	}
	return found, found != ""
}

// allHubsGroups may act on every hub without a user_hubs assignment —
// the groups hms-backend's RequireHubAccess lets straight through.
func allHubsGroups(group string) bool {
	switch group {
	case "super-admin", "admin", "super-instructor":
		return true
	}
	return false
}

// CanAccessHub reports whether u may scan for hubID: either their group
// covers every hub, or they have an active HMS user row assigned to it.
func CanAccessHub(ctx context.Context, dbConn *sql.DB, u User, hubID string) (bool, error) {
	if allHubsGroups(u.Group) {
		return true, nil
	}

	var ok bool
	err := db.Retry(func() error {
		return dbConn.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM public.users u
				JOIN public.user_hubs uh ON uh.user_id = u.id
				WHERE u.keycloak_user_id = $1
				  AND u.deleted_at IS NULL
				  AND u.status <> 'inactive'
				  AND uh.hub_id = $2
			)`,
			u.Subject, hubID,
		).Scan(&ok)
	})
	if err != nil {
		return false, fmt.Errorf("kcauth: checking hub access: %w", err)
	}
	return ok, nil
}
