package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"idscan/internal/hubauth"
)

// LoginRequest is the JSON body for POST /api/auth/hub-login.
type LoginRequest struct {
	LoginCode string `json:"login_code"`
	PIN       string `json:"pin"`
}

// LoginResponse is returned on successful login. Token is shown to the
// client exactly once — only its hash is ever stored server-side.
type LoginResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	Token     string `json:"token,omitempty"`
	HubName   string `json:"hub_name,omitempty"`
	ExpiresIn int    `json:"expires_in_seconds,omitempty"`
}

// NewHubTagsHandler builds the GET /api/auth/hub-tags handler, returning
// the list of available login identities for the frontend's dropdown.
// Read-only, no secrets, safe without authentication.
func NewHubTagsHandler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := hubauth.ListTags(r.Context(), dbConn)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": "could not load hub list"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": tags})
	}
}

// NewHubLoginHandler builds the POST /api/auth/hub-login handler.
func NewHubLoginHandler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, LoginResponse{OK: false, Message: "malformed request body"})
			return
		}
		req.LoginCode = strings.TrimSpace(req.LoginCode)
		req.PIN = strings.TrimSpace(req.PIN)
		if req.LoginCode == "" || req.PIN == "" {
			writeJSON(w, http.StatusBadRequest, LoginResponse{OK: false, Message: "login_code and pin are required"})
			return
		}

		token, _, hubName, err := hubauth.Login(r.Context(), dbConn, req.LoginCode, req.PIN)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, LoginResponse{
				OK:        true,
				Token:     token,
				HubName:   hubName,
				ExpiresIn: int(hubauth.SessionDuration.Seconds()),
			})
		case errors.Is(err, hubauth.ErrInvalidCredentials):
			writeJSON(w, http.StatusUnauthorized, LoginResponse{OK: false, Message: "incorrect login code or PIN"})
		case errors.Is(err, hubauth.ErrAccountLocked):
			writeJSON(w, http.StatusTooManyRequests, LoginResponse{OK: false, Message: "too many failed attempts — try again in 15 minutes"})
		default:
			writeJSON(w, http.StatusInternalServerError, LoginResponse{OK: false, Message: "login failed — try again"})
		}
	}
}

// contextKey avoids collisions with other packages' context keys.
type contextKey string

const hubIDContextKey contextKey = "hub_id"

// RequireHubSession wraps a handler, requiring a valid
// "Authorization: Bearer <token>" header, and injects the authenticated
// hub's ID into the request context for the wrapped handler to read via
// HubIDFromContext.
func RequireHubSession(dbConn *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			writeJSON(w, http.StatusUnauthorized, AttendanceResponse{OK: false, Message: "not logged in"})
			return
		}

		hubID, err := hubauth.ValidateAndTouch(r.Context(), dbConn, token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, AttendanceResponse{OK: false, Message: "session expired — please log in again"})
			return
		}

		ctx := context.WithValue(r.Context(), hubIDContextKey, hubID)
		next(w, r.WithContext(ctx))
	}
}

// HubIDFromContext reads the authenticated hub's ID, set by
// RequireHubSession. Returns "" if called outside that middleware.
func HubIDFromContext(ctx context.Context) string {
	hubID, _ := ctx.Value(hubIDContextKey).(string)
	return hubID
}
