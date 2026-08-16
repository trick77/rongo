package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type contextKey struct{}

var userKey contextKey

// devSubject is the fixed identity dev mode signs in. config refuses dev mode
// on a non-loopback address, so this never reaches a network.
const devSubject = "dev-user"

// UserFrom returns the authenticated user attached by Middleware.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok
}

// Middleware authenticates a request in whichever mode is configured and
// attaches the user to the request context. It fails closed: any path that
// does not positively identify a caller answers 401.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A valid session cookie wins in every mode.
		if c, err := r.Cookie(SessionCookie); err == nil {
			if u, ok := s.UserByToken(c.Value); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
				return
			}
		}

		switch s.mode {
		case "dev":
			u, err := s.UpsertUser(devSubject, "dev@example.invalid", true)
			if err != nil {
				slog.Error("dev auto-login failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return

		case "token":
			presented, ok := bearerToken(r)
			// Constant-time compare: a length-independent equality check on a
			// shared secret leaks it a byte at a time.
			if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(s.adminToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			u, err := s.UpsertUser("admin-token", "", true)
			if err != nil {
				slog.Error("admin token login failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return

		case "oidc":
			// The seam. Until the OIDC flow lands, an unauthenticated caller
			// gets 401 rather than a misleading 500.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return

		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	})
}

// SetSessionCookie writes the session cookie. Secure is set whenever the
// public URL is https.
func SetSessionCookie(w http.ResponseWriter, token, publicURL string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(publicURL, "https://"),
		Expires:  time.Now().Add(ttl),
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}
