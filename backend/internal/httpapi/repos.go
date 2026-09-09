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
	// Snapshot is true for a hand-extracted source drop rather than a clone.
	// The page says so because a snapshot's LastSHA never moves on its own:
	// without the word, a correct one-off index is indistinguishable from a
	// poller that stopped working.
	Snapshot bool
	// Enabled is false for a repository the YAML declares with `enabled: false`.
	// It keeps its index and its row here — it is a repository being left alone,
	// not one being retired. A repository REMOVED from repos.yaml is purged and
	// no longer appears on this page at all.
	Enabled bool
	// LastError carries the failure of the last run verbatim — a branch that
	// vanished upstream above all. A silent stop leaves the index frozen at
	// months-old code while the page looks healthy.
	LastError string
	// Project is the product this repository belongs to, and the unit the page
	// groups by. Part, Description and Uses are what it declares about its part
	// in that product: all four come from repos.yaml, none from the code.
	Project     string
	Part        string
	Description string
	Uses        []string
}

// RepoStatusSource reports the state of every repository rongo knows about.
type RepoStatusSource interface {
	RepoStatus(ctx context.Context) ([]RepoStatus, error)
}

// uses never encodes null: the browser reads it as a list to draw arrows from,
// and a null there is one more thing every caller has to guard.
func uses(u []string) []string {
	if u == nil {
		return []string{}
	}
	return u
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
			"snapshot":    st.Snapshot,
			"last_error":  st.LastError,
			"project":     st.Project,
			"part":        st.Part,
			"description": st.Description,
			"uses":        uses(st.Uses),
		})
	}
	// no-store, because this is a STATUS page and a cached status page lies.
	// The endpoint carried no cache directives at all, so a browser was free to
	// serve its own copy heuristically — and it did: after an operator fixed
	// repos.yaml and restarted, the page went on drawing the previous
	// configuration's `uses` arrows, which reads as rongo ignoring the file
	// rather than as the browser ignoring the server. Everything here changes
	// on a restart or a poll, so none of it is worth caching for any interval.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
