// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/web"
)

// Deps holds every collaborator the HTTP layer needs. Each field is an
// interface declared here (consumer-side), so later phases can add the
// indexer, the LLM client and the retriever without touching call sites.
// A nil field means the feature is unconfigured; its endpoints answer 503.
type Deps struct {
	Auth *auth.Service
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
	s.handler = recovery(logging(s.mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// routes is the single place every route is registered.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))

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
