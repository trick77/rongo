package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/ratelimit"
)

// handleAuthLogin starts a sign-in. Proxy mode has none to start: the proxy
// did it, so the browser goes to the app, or to an error when the proxy named
// nobody. Password mode has a form. OIDC mode hands over to the provider.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.proxyMode() {
		if strings.TrimSpace(r.Header.Get(auth.ProxyUserHeader)) == "" {
			http.Redirect(w, r, "/?auth_error=proxy", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.passwordMode() {
		http.Redirect(w, r, "/?login=password", http.StatusFound)
		return
	}
	if s.deps.OIDC == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	s.deps.OIDC.Start(w, r)
}

func (s *Server) passwordMode() bool {
	return s.deps.Auth != nil && s.deps.Auth.Mode() == "password"
}

func (s *Server) proxyMode() bool {
	return s.deps.Auth != nil && s.deps.Auth.Mode() == "proxy"
}

// handleAuthPassword signs the one account in from the form. Behind kit's
// throttle: one attempt per loginGap per client (an IPv4 address, an IPv6
// /64), queued up to loginMaxWait and refused past that with the real wait,
// so guessing is capped per client while a flood from one client cannot keep
// anyone else from signing in.
func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if !s.passwordMode() || s.deps.Sessions == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "password login is not configured")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad request")
		return
	}
	key := ratelimit.Key(clientIP(r))
	if err := s.logins.Wait(r.Context(), key); err != nil {
		var throttled *httpx.ThrottledError
		if errors.As(err, &throttled) {
			httpx.TooManyRequests(w, throttled.RetryAfter)
			return
		}
		// The client left while queued: nothing was checked.
		httpx.WriteError(w, http.StatusServiceUnavailable, "login not checked")
		return
	}
	claims, err := s.deps.Auth.LoginPassword(r.Context(), in.Username, in.Password)
	if errors.Is(err, auth.ErrBadCredentials) {
		slog.WarnContext(r.Context(), "password login rejected", "ip", clientIP(r))
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		serverError(w, "password login failed", err)
		return
	}
	if err := s.deps.Sessions.Issue(w, claims); err != nil {
		serverError(w, "issue session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthCallback finishes an OIDC sign-in: kit checked state, PKCE and
// the ID token; the identity becomes a local user and the session cookie.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.deps.OIDC == nil || s.deps.Auth == nil || s.deps.Sessions == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	claims, err := s.deps.OIDC.Callback(w, r)
	if err != nil {
		slog.WarnContext(r.Context(), "oidc callback failed", "err", err)
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
	if _, err := s.deps.Auth.AdmitClaims(r.Context(), claims, s.deps.OIDCAdminGroup); err != nil {
		slog.ErrorContext(r.Context(), "admit oidc user failed", "err", err)
		http.Redirect(w, r, "/?auth_error=session_failed", http.StatusFound)
		return
	}
	if err := s.deps.Sessions.Issue(w, claims); err != nil {
		slog.ErrorContext(r.Context(), "issue session failed", "err", err)
		http.Redirect(w, r, "/?auth_error=session_failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleAuthLogout clears the cookie. There is nothing to revoke: the claims
// travel in the cookie, as in every service of the family. The redirect names
// where the browser goes next: proxy mode's sign-out is the proxy's.
func (s *Server) handleAuthLogout(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Sessions != nil {
		s.deps.Sessions.Clear(w)
	}
	target := "/?signed_out=1"
	if s.passwordMode() {
		target = "/?signed_out=local"
	}
	if s.proxyMode() {
		target = "/oauth/sign_out"
	}
	writeJSON(w, map[string]string{"redirect_url": target})
}
