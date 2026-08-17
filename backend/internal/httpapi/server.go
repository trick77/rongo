// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/web"
)

// Deps holds every collaborator the HTTP layer needs. Phase 1 has only Auth,
// wired as its concrete *auth.Service; later phases add the indexer, the LLM
// client and the retriever the same way, one field each. A nil field means
// the feature is unconfigured; its endpoints answer 503.
type Deps struct {
	Auth *auth.Service
	// Repos backs the Repos page. Nil means this deployment cannot report
	// repository status, which its endpoint says with a 503 rather than an
	// empty list.
	Repos RepoStatusSource
	// Ask runs the question pipeline; Threads persists the record. Nil means
	// this deployment cannot answer questions, which its routes say with a 503.
	Ask     Asker
	Threads *threads.Store
	// Titler names a thread. Optional: without it the sidebar keeps the first
	// words of the question, which is a worse label but never a broken one.
	Titler func(ctx context.Context, question string) string
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
	s.mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
	s.mux.Handle("GET /api/repos", s.requireAuth(http.HandlerFunc(s.handleRepos)))
	s.mux.Handle("GET /api/threads", s.requireAuth(http.HandlerFunc(s.handleThreads)))
	s.mux.Handle("GET /api/threads/{id}", s.requireAuth(http.HandlerFunc(s.handleThread)))
	s.mux.Handle("POST /api/ask", s.requireAuth(http.HandlerFunc(s.handleAsk)))

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
