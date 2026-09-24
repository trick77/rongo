package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/rongo/internal/auth"
)

// RepoStatus is one row of the Repos page. The page is read-only status, never
// a maintenance form: the repository list lives in repos.yaml, and credentials
// never do.
type RepoStatus struct {
	Name   string
	Branch string
	// LastSHA is the commit the index was built from. Together with
	// LastIndexedAt it answers "how current is what rongo tells me";
	// LastRunAt is the last poll of any outcome, which answers "is the poller
	// alive" and nothing about the index.
	LastSHA       string
	LastRunAt     time.Time
	LastIndexedAt time.Time
	Files         int
	Chunks        int
	Modules       int
	// Commits is how many commits the lane holds for the branch, and
	// NewestCommitAt the date of the newest: what a "what changed" answer
	// can look back over. Zero and the zero time for a snapshot, which has
	// no history.
	Commits        int
	NewestCommitAt time.Time
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
	// Image is the container image the entry declares, tag-less; empty for
	// a repository nothing deploys.
	Image string
	// Library says the entry is a shared library from the `libraries:` block:
	// a project of one that other projects' uses edges may point at, which is
	// how the page draws it inside their wiring without listing it as a member.
	Library bool
	Stages  []string
	// ReindexQueued says an admin asked for a full re-index and the poller
	// has not run it yet.
	ReindexQueued bool
}

// Reindexer takes the Repos page's one action: a full re-index of a
// repository, or of every active one, at the poller's next pass.
type Reindexer interface {
	RequestReindex(ctx context.Context, name string) (bool, error)
	RequestReindexAll(ctx context.Context) (int, error)
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
		var lastRun, lastIndexed, newestCommit any
		if !st.LastRunAt.IsZero() {
			lastRun = st.LastRunAt.UTC().Format(time.RFC3339)
		}
		if !st.LastIndexedAt.IsZero() {
			lastIndexed = st.LastIndexedAt.UTC().Format(time.RFC3339)
		}
		if !st.NewestCommitAt.IsZero() {
			newestCommit = st.NewestCommitAt.UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"name":             st.Name,
			"branch":           st.Branch,
			"last_sha":         st.LastSHA,
			"last_run_at":      lastRun,
			"last_indexed_at":  lastIndexed,
			"files":            st.Files,
			"chunks":           st.Chunks,
			"modules":          st.Modules,
			"commits":          st.Commits,
			"newest_commit_at": newestCommit,
			"enabled":          st.Enabled,
			"snapshot":         st.Snapshot,
			"last_error":       st.LastError,
			"project":          st.Project,
			"part":             st.Part,
			"description":      st.Description,
			"image":            st.Image,
			"uses":             uses(st.Uses),
			"library":          st.Library,
			"stages":           uses(st.Stages),
			"reindex_queued":   st.ReindexQueued,
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

// handleReindex queues a full re-index of one repository, or of every active
// one when the path names none. Admin only: it is the page's one action, and
// it costs an embedding call for every file whose text changed. 202, because
// the poller runs it on its next pass; the page shows the request queued
// until then.
func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !u.IsAdmin {
		http.Error(w, "re-indexing is for an administrator", http.StatusForbidden)
		return
	}
	if s.deps.Reindex == nil {
		http.Error(w, "re-indexing unavailable", http.StatusServiceUnavailable)
		return
	}
	queued := 0
	if name := r.PathValue("name"); name != "" {
		found, err := s.deps.Reindex.RequestReindex(r.Context(), name)
		if err != nil {
			slog.Error("re-index request failed", "repo", name, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !found {
			// Unknown and parked read the same: neither is a repository the
			// poller serves.
			http.Error(w, "no such active repository", http.StatusNotFound)
			return
		}
		queued = 1
	} else {
		n, err := s.deps.Reindex.RequestReindexAll(r.Context())
		if err != nil {
			slog.Error("re-index request failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		queued = n
	}
	slog.Info("full re-index requested", "by", u.Subject, "repositories", queued)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"queued": queued})
}
