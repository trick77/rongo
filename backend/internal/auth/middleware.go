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

// The headers proxy mode reads. They are what oauth-proxy and its relatives
// set with pass-user-headers on.
const (
	ProxyUserHeader  = "X-Forwarded-User"
	ProxyEmailHeader = "X-Forwarded-Email"
)

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
			s.admit(w, r, next, "dev auto-login", devSubject, "dev@example.invalid")
			return

		case "token":
			presented, ok := bearerToken(r)
			// Constant-time compare: a length-independent equality check on a
			// shared secret leaks it a byte at a time.
			if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(s.adminToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			s.admit(w, r, next, "admin token login", "admin-token", "")
			return

		case "proxy":
			// The proxy in front of the process did the login and says who it
			// was. config allows this mode on a loopback listener only, so the
			// header cannot come from anyone but the proxy. The email is what
			// the proxy sends, which for OpenShift's oauth-proxy is a synthetic
			// <user>@cluster.local, not a mailbox.
			subject := strings.TrimSpace(r.Header.Get(ProxyUserHeader))
			if subject == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			s.admit(w, r, next, "proxy login", subject, strings.TrimSpace(r.Header.Get(ProxyEmailHeader)))
			return

		case "password":
			// No header form: the only way in is the form posting to
			// /api/auth/password, which sets the cookie checked above.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are set; Secure is config-driven so local development over plain HTTP still works
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
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are set; Secure is config-driven so local development over plain HTTP still works
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

// admit lets the request through as the admin subject, writing the user
// once per interval (see Admit). what names the mode for the log line.
func (s *Service) admit(w http.ResponseWriter, r *http.Request, next http.Handler, what, subject, email string) {
	u, err := s.Admit(subject, email, true)
	if err != nil {
		slog.Error(what+" failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
}
