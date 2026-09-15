package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/rongo/internal/auth"
)

// handleAuthLogin sends the browser to the provider. It is registered outside
// requireAuth on purpose: a caller who has to sign in has no session yet, and a
// 401 here would be a redirect loop.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.passwordMode() {
		// The SPA lands here on every 401 without knowing the mode. In
		// password mode the "provider" is its own form, so send it back with
		// the marker that renders one.
		http.Redirect(w, r, "/?login=password", http.StatusFound)
		return
	}
	if s.deps.OIDC == nil {
		http.Error(w, "oidc is not configured", http.StatusServiceUnavailable)
		return
	}
	s.deps.OIDC.StartLogin(w, r)
}

// passwordMode reports whether the login form is the way in.
func (s *Server) passwordMode() bool {
	return s.deps.Auth != nil && s.deps.Auth.Mode() == "password"
}

// handleAuthPassword takes the form's credentials and mints the session.
// Outside requireAuth for the same reason handleAuthLogin is. Every failure
// is the same 401: which half was wrong is exactly what a guesser wants.
func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if !s.passwordMode() {
		http.Error(w, "password login is not configured", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token, expiresAt, err := s.deps.Auth.LoginPassword(in.Username, in.Password)
	if errors.Is(err, auth.ErrBadCredentials) {
		// The one place a failed guess is recorded; bcrypt keeps it slow, the
		// log keeps it visible.
		slog.Warn("password login rejected", "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.Error("password login failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	auth.SetSessionCookie(w, token, s.deps.CookieSecure, time.Until(expiresAt))
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthCallback finishes the flow and mints the session.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.deps.OIDC == nil || s.deps.Auth == nil {
		http.Error(w, "oidc is not configured", http.StatusServiceUnavailable)
		return
	}
	claims, err := s.deps.OIDC.HandleCallback(r)
	// Cleared whether the callback worked or not, so a spent or abandoned login
	// does not leave a usable state value in the browser.
	s.deps.OIDC.ClearTransientCookies(w)
	if err != nil {
		// The browser only ever sees the one generic code. HandleCallback has
		// four distinct failure modes — bad state, bad nonce, rejected code
		// exchange, failed token verification — and telling them apart from
		// outside is exactly what an attacker probing the callback wants. The
		// log keeps the real one, because otherwise a misconfigured provider is
		// undebuggable.
		slog.Warn("oidc callback failed", "err", err)
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
	token, expiresAt, _, err := s.deps.Auth.CreateSessionFromClaims(claims, s.deps.OIDCAdminGroup)
	if err != nil {
		slog.Error("session create failed", "err", err)
		http.Redirect(w, r, "/?auth_error=session_failed", http.StatusFound)
		return
	}
	auth.SetSessionCookie(w, token, s.deps.CookieSecure, time.Until(expiresAt))
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleAuthLogout revokes the session and clears the cookie. It answers JSON
// rather than redirecting because the SPA calls it with fetch and follows
// redirectUrl itself; a 302 here would be followed by fetch and return the
// index page as the response body.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		if err := s.deps.Auth.DeleteSession(c.Value); err != nil {
			// The cookie is cleared either way, but a session left live in the
			// database is security-relevant enough not to vanish silently.
			slog.Error("session revoke failed", "err", err)
		}
	}
	auth.ClearSessionCookie(w, s.deps.CookieSecure)
	w.Header().Set("Content-Type", "application/json")
	// Not "/": the provider's session is untouched by this, so landing on the
	// app root would auto-redirect to the provider, get a token without a
	// prompt, and sign the user straight back in — a logout button that visibly
	// does nothing. The marker tells the SPA to stop and say what happened.
	// Password mode has no provider, so its marker says only "signed out".
	target := "/?signed_out=1"
	if s.passwordMode() {
		target = "/?signed_out=local"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect_url": target})
}
