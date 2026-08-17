package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// RepoStatus is one row of the Repos page. The page is read-only status, never
// a maintenance form: the repository list lives in repos.yaml, and credentials
// never do.
type RepoStatus struct {
	Name   string
	Branch string
	// LastSHA is the commit the index was built from. Together with LastRunAt
	// it answers "how current is what rongo tells me".
	LastSHA   string
	LastRunAt time.Time
	Files     int
	Chunks    int
	Modules   int
	// Enabled is false for a repository that left repos.yaml. Its index
	// survives until an explicit purge, and it stays on the page: a typo in the
	// YAML must not make a repository look like it never existed.
	Enabled bool
	// LastError carries the failure of the last run verbatim — a branch that
	// vanished upstream above all. A silent stop leaves the index frozen at
	// months-old code while the page looks healthy.
	LastError string
}

// RepoStatusSource reports the state of every repository rongo knows about.
type RepoStatusSource interface {
	RepoStatus(ctx context.Context) ([]RepoStatus, error)
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	if s.deps.Repos == nil {
		// Not an empty list: "no repositories configured" and "this deployment
		// cannot tell you" are different facts, and the page must not show the
		// first when the second is true.
		http.Error(w, "repository status unavailable", http.StatusServiceUnavailable)
		return
	}
	list, err := s.deps.Repos.RepoStatus(r.Context())
	if err != nil {
		slog.Error("repository status failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	out := make([]map[string]any, 0, len(list))
	for _, st := range list {
		var lastRun any
		if !st.LastRunAt.IsZero() {
			lastRun = st.LastRunAt.UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"name":        st.Name,
			"branch":      st.Branch,
			"last_sha":    st.LastSHA,
			"last_run_at": lastRun,
			"files":       st.Files,
			"chunks":      st.Chunks,
			"modules":     st.Modules,
			"enabled":     st.Enabled,
			"last_error":  st.LastError,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
