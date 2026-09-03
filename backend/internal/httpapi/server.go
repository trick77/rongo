// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
	"github.com/trick77/rongo/web"
)

// Threads is everything the HTTP layer needs from the thread record. An
// interface, not the concrete *threads.Store, so a test can fake a single
// method's failure (a Clarify that cannot write, for instance) without
// carrying a whole database through it. *threads.Store satisfies this
// structurally.
type Threads interface {
	Create(ctx context.Context, subject, question string) (threads.Thread, error)
	SetTitle(ctx context.Context, id int64, title string) error
	AddQuestion(ctx context.Context, threadID int64, audience, language, question string) (threads.Message, error)
	Finish(ctx context.Context, messageID int64, answer string, citations []ask.Citation) error
	Fail(ctx context.Context, messageID int64, msg string) error
	List(ctx context.Context, subject string) ([]threads.Thread, error)
	Message(ctx context.Context, subject string, messageID int64) (threads.Message, bool, error)
	Messages(ctx context.Context, subject string, threadID int64) ([]threads.Message, error)
	Owns(ctx context.Context, subject string, threadID int64) (bool, error)
	Clarify(ctx context.Context, messageID int64, c ask.Clarification) (int64, error)
	Clarification(ctx context.Context, subject string, messageID int64) (*threads.Clarification, error)
	CandidateHits(ctx context.Context, subject string, clarificationID int64, idx int) (ask.Understanding, []retrieve.Hit, error)
	LinkChoice(ctx context.Context, subject string, messageID, clarificationID int64, idx int) error
	SaveSources(ctx context.Context, messageID int64, sources []ask.Source) error
	Sources(ctx context.Context, subject string, messageID int64) (sources []ask.Source, total int, err error)
	// SaveUsage records the paid calls one turn made, however it ended.
	SaveUsage(ctx context.Context, messageID int64, calls []usage.Call) error
}

// Deps holds every collaborator the HTTP layer needs. Phase 1 has only Auth,
// wired as its concrete *auth.Service; later phases add the indexer, the LLM
// client and the retriever the same way, one field each. A nil field means
// the feature is unconfigured; its endpoints answer 503.
type Deps struct {
	Auth *auth.Service
	// OIDC drives the login redirect and the callback. An interface, not the
	// concrete *auth.OIDCService, so a test can make a callback fail without a
	// provider. Nil means this deployment has no OIDC (dev or token mode), and
	// the two auth routes say so with a 503.
	OIDC OIDCService
	// OIDCAdminGroup is the group that grants admin. Empty means no check; see
	// auth.Service.CreateSessionFromClaims.
	OIDCAdminGroup string
	// CookieSecure marks the session cookie Secure. It comes from the OIDC
	// redirect URL, because behind a TLS-terminating proxy the process only
	// ever sees plain HTTP and nothing about the request says otherwise.
	CookieSecure bool
	// Repos backs the Repos page. Nil means this deployment cannot report
	// repository status, which its endpoint says with a 503 rather than an
	// empty list.
	Repos RepoStatusSource
	// Ask runs the question pipeline; Threads persists the record. Nil means
	// this deployment cannot answer questions, which its routes say with a 503.
	Ask     Asker
	Threads Threads
	// Titler names a thread. Optional: without it the sidebar keeps the first
	// words of the question, which is a worse label but never a broken one.
	Titler func(ctx context.Context, question string) string
	// Prices turns stored tokens into money, per model. Empty means the
	// browser sees tokens only — the honest default when nobody has told
	// rongo what the endpoint charges.
	Prices usage.Prices
}

// OIDCService is the login half of authentication, as the HTTP layer needs it.
// *auth.OIDCService satisfies it structurally.
type OIDCService interface {
	StartLogin(w http.ResponseWriter, r *http.Request)
	HandleCallback(r *http.Request) (auth.Claims, error)
	ClearTransientCookies(w http.ResponseWriter)
}

// Server routes requests and owns the middleware chain.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	handler http.Handler
}

// NewServer builds the router and wraps it in the middleware chain once,
// rather than per request.
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	// logging outermost: a panicked request must still produce an access-log
	// line (as a 500), so recovery has to run inside logging, not around it.
	s.handler = logging(recovery(s.mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// routes is the single place every route is registered.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	// Login and callback sit outside requireAuth: a caller who still has to
	// sign in has no session, and gating them would be a redirect loop.
	// Logout does not, because revoking a session needs one.
	s.mux.HandleFunc("GET /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("GET /api/auth/callback", s.handleAuthCallback)
	s.mux.Handle("POST /api/auth/logout", s.requireAuth(http.HandlerFunc(s.handleAuthLogout)))
	s.mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
	s.mux.Handle("GET /api/repos", s.requireAuth(http.HandlerFunc(s.handleRepos)))
	s.mux.Handle("GET /api/threads", s.requireAuth(http.HandlerFunc(s.handleThreads)))
	s.mux.Handle("GET /api/threads/{id}", s.requireAuth(http.HandlerFunc(s.handleThread)))
	s.mux.Handle("POST /api/ask", s.requireAuth(http.HandlerFunc(s.handleAsk)))
	s.mux.Handle("POST /api/messages/{id}/reexplain", s.requireAuth(http.HandlerFunc(s.handleReexplain)))

	// "/" is the catch-all: everything not matched above goes to the SPA.
	s.mux.Handle("/", web.Handler())
}

// requireAuth is the single gate every authenticated route goes through.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.deps.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
		})
	}
	return s.deps.Auth.Middleware(next)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"subject":  u.Subject,
		"email":    u.Email,
		"is_admin": u.IsAdmin,
	})
}
