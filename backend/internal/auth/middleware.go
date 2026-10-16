package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/secret"
)

type contextKey struct{}

var userKey contextKey

// devSubject is the fixed identity dev mode signs in. config refuses dev mode
// on a non-loopback address, so this never reaches a network.
const devSubject = "dev-user"

// The headers proxy mode reads. They are what oauth-proxy and its relatives
// set with pass-user-headers on; the groups, comma-separated, are read only
// when an admin group is configured. oauth2-proxy sends them; OpenShift's
// oauth-proxy does not.
const (
	ProxyUserHeader   = "X-Forwarded-User"
	ProxyEmailHeader  = "X-Forwarded-Email"
	ProxyGroupsHeader = "X-Forwarded-Groups"
)

// WithUser is the context a request carries once the middleware admitted
// u; a test drives a handler with the same one.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

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
		// A valid session cookie wins in the modes that issue one. Admin is
		// read off the claims on every request, as the family does, so a
		// changed admin group reaches live sessions; the password account is
		// admin by mode, in password mode only. HasGroup folds case like
		// kit's own group cap does, so a group spelled unlike the IdP's still
		// grants.
		if s.sessions != nil {
			if c, ok := s.sessions.Read(r); ok {
				admin := (s.mode == "password" && c.Subject == passwordSubject) || c.HasGroup(s.oidcAdminGroup)
				s.admitAs(w, r, next, "session", c.Subject, c.Email, admin)
				return
			}
		}

		switch s.mode {
		case "dev":
			s.admit(w, r, next, "dev auto-login", devSubject, "dev@example.invalid")
			return

		case "token":
			presented, ok := httpx.Bearer(r)
			if !ok || !secret.Match(presented, s.adminToken) {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
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
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			s.admitAs(w, r, next, "proxy login", subject, strings.TrimSpace(r.Header.Get(ProxyEmailHeader)),
				s.proxyAdmin(r))
			return

		case "password":
			// No header form: the only way in is the form posting to
			// /api/auth/password, which sets the cookie checked above.
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return

		case "oidc":
			// The seam. Until the OIDC flow lands, an unauthenticated caller
			// gets 401 rather than a misleading 500.
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return

		default:
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	})
}

// proxyAdmin decides admin for a proxied caller. No group configured is no
// check, and the header is not read. With one, a request carrying no groups
// header at all makes nobody admin — OpenShift's oauth-proxy never sends
// one — so that is said once at Warn rather than left to look like a
// permissions bug.
func (s *Service) proxyAdmin(r *http.Request) bool {
	if s.proxyAdminGroup == "" {
		return true
	}
	if len(r.Header.Values(ProxyGroupsHeader)) == 0 {
		s.noGroupsOnce.Do(func() {
			slog.Warn("proxy sent no "+ProxyGroupsHeader+" header; with BACKEND_PROXY_ADMIN_GROUP set nobody is admin",
				"group", s.proxyAdminGroup)
		})
		return false
	}
	return isAdmin(proxyGroups(r), s.proxyAdminGroup)
}

// proxyGroups is the groups the proxy forwarded, comma-separated in one
// header, trimmed.
func proxyGroups(r *http.Request) []string {
	var out []string
	for _, g := range strings.Split(r.Header.Get(ProxyGroupsHeader), ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// admit lets the request through as the admin subject, writing the user
// once per interval (see Admit). what names the mode for the log line.
func (s *Service) admit(w http.ResponseWriter, r *http.Request, next http.Handler, what, subject, email string) {
	s.admitAs(w, r, next, what, subject, email, true)
}

// admitAs is admit with the admin flag decided by the caller.
func (s *Service) admitAs(w http.ResponseWriter, r *http.Request, next http.Handler, what, subject, email string, admin bool) {
	u, err := s.Admit(r.Context(), subject, email, admin)
	if err != nil {
		// A request its client abandoned is not a failed login.
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Error(what+" failed", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
}
