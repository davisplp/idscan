package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"idscan/internal/kcauth"
)

// contextKey avoids collisions with other packages' context keys.
type contextKey string

const (
	hubIDContextKey contextKey = "hub_id"
	userContextKey  contextKey = "user"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RequireHubUser wraps a handler served under a {hubId} path, requiring
// an HMS Keycloak access token ("Authorization: Bearer <token>") for a
// user allowed to act on that hub. The hub in the path is the scanning
// hub; the wrapped handler reads it via HubIDFromContext and the user via
// UserFromContext.
func RequireHubUser(verifier *kcauth.Verifier, dbConn *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeJSON(w, http.StatusUnauthorized, AttendanceResponse{OK: false, Message: "not logged in"})
			return
		}

		user, err := verifier.Verify(r.Context(), token)
		switch {
		case errors.Is(err, kcauth.ErrNoHMSAccess):
			writeJSON(w, http.StatusForbidden, AttendanceResponse{OK: false, Message: "no HMS access"})
			return
		case err != nil:
			writeJSON(w, http.StatusUnauthorized, AttendanceResponse{OK: false, Message: "session expired — please log in again"})
			return
		}

		hubID := r.PathValue("hubId")
		if !uuidPattern.MatchString(hubID) {
			writeJSON(w, http.StatusBadRequest, AttendanceResponse{OK: false, Message: "invalid hub id"})
			return
		}

		allowed, err := kcauth.CanAccessHub(r.Context(), dbConn, user, hubID)
		if err != nil {
			log.Printf("api: hub access check failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, AttendanceResponse{OK: false, Message: "could not check hub access — try again"})
			return
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, AttendanceResponse{OK: false, Message: "not authorized for this hub"})
			return
		}

		ctx := context.WithValue(r.Context(), hubIDContextKey, hubID)
		ctx = context.WithValue(ctx, userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// HubIDFromContext reads the scanning hub's ID (a hubs.id), set by
// RequireHubUser. Returns "" if called outside that middleware.
func HubIDFromContext(ctx context.Context) string {
	hubID, _ := ctx.Value(hubIDContextKey).(string)
	return hubID
}

// UserFromContext reads the authenticated HMS user, set by RequireHubUser.
func UserFromContext(ctx context.Context) (kcauth.User, bool) {
	u, ok := ctx.Value(userContextKey).(kcauth.User)
	return u, ok
}
