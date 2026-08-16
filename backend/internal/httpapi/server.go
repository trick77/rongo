// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import "net/http"

// Deps holds every collaborator the HTTP layer needs. Each field is an
// interface declared here (consumer-side), so later phases can add the
// indexer, the LLM client and the retriever without touching call sites.
// A nil field means the feature is unconfigured; its endpoints answer 503.
type Deps struct{}

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
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
