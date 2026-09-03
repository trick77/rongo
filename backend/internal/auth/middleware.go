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

// SetSessionCookie writes the session cookie. secure comes from the OIDC
// redirect URL: behind a TLS-terminating proxy the process only ever sees
// plain HTTP, so nothing it can observe about the request says otherwise.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Now().Add(ttl),
	})
}

// ClearSessionCookie expires the session cookie. Every attribute other than the
// value matches what SetSessionCookie wrote: a browser keys a cookie by name,
// domain and path, so a clear that differs in Path or Secure leaves the
// original in place and the user stays signed in.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
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
