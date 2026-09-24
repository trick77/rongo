// Package repostatus assembles what the Repos page shows: the indexing state
// of every repository, plus how many modules its index currently falls into.
//
// It is read-only by construction. The repository list lives in repos.yaml and
// credentials never do, so there is nothing here to edit — the page is status,
// not a maintenance form.
package repostatus

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/modules"
)

// Store reads repository state and derives the module count from the index.
type Store struct {
	db      *sql.DB
	state   *indexer.StateStore
	history *history.Store
	opts    modules.Opts
	// derived is what the index said last time, per repository, keyed by the
	// state it was read at: the clustering is a scan of every file and
	// chunk, the page is read on every turn, and neither number moves until
	// the index does.
	mu      sync.Mutex
	derived map[string]derived
	// clusters counts the clusterings run, for the test that pins the cache.
	clusters int
}

// derived is the module count and the commit lane's summary, with the
// index state they were read at.
type derived struct {
	sha            string
	files, chunks  int
	modules        int
	commits        int
	newestCommitAt time.Time
}

// New builds a Store. opts are the clustering constants; they decide how many
// modules a repository reports, so the page and the routing layer must be given
// the same ones.
func New(db *sql.DB, opts modules.Opts) *Store {
	return &Store{db: db, state: indexer.NewStateStore(db), history: history.New(db), opts: opts, derived: map[string]derived{}}
}

// derivedOf is the clustering and the commit count for one repository, read
// again only when the index moved: a new sha, or the totals a sweep changed.
func (s *Store) derivedOf(ctx context.Context, st indexer.RepoState) (derived, error) {
	s.mu.Lock()
	d, ok := s.derived[st.Name]
	s.mu.Unlock()
	if ok && d.sha == st.LastSHA && d.files == st.Files && d.chunks == st.Chunks {
		return d, nil
	}
	mods, err := modules.Cluster(ctx, s.db, st.Name, s.opts)
	if err != nil {
		return derived{}, fmt.Errorf("cluster %s: %w", st.Name, err)
	}
	commits, newest, err := s.history.Count(ctx, st.Name)
	if err != nil {
		return derived{}, fmt.Errorf("count commits of %s: %w", st.Name, err)
	}
	d = derived{sha: st.LastSHA, files: st.Files, chunks: st.Chunks, modules: len(mods), commits: commits, newestCommitAt: newest}
	s.mu.Lock()
	s.derived[st.Name] = d
	s.clusters++
	s.mu.Unlock()
	return d, nil
}

// RepoStatus reports every active repository in repo_state. One the YAML
// declares with `enabled: false` is parked: it keeps its index and its row,
// is not polled, and is not on the page — parking stops new answers and
// takes the repository out of sight, it never revises what was answered
// before. A repository REMOVED from repos.yaml is not here either: it was
// purged with its row when the list was last read.
func (s *Store) RepoStatus(ctx context.Context) ([]httpapi.RepoStatus, error) {
	all, err := s.state.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("read repository state: %w", err)
	}
	out := make([]httpapi.RepoStatus, 0, len(all))
	for _, st := range all {
		d, err := s.derivedOf(ctx, st)
		if err != nil {
			return nil, err
		}
		out = append(out, httpapi.RepoStatus{
			Commits:        d.commits,
			NewestCommitAt: d.newestCommitAt,
			Name:           st.Name,
			Branch:         st.Branch,
			LastSHA:        st.LastSHA,
			LastRunAt:      st.LastRunAt,
			LastIndexedAt:  st.LastIndexedAt,
			Files:          st.Files,
			Chunks:         st.Chunks,
			Modules:        d.modules,
			Enabled:        st.Enabled,
			Snapshot:       st.Snapshot(),
			LastError:      st.LastError,
			// A row written before projects shipped has no project; it stands
			// as one of its own, the same fallback projects.Load applies.
			Project:       projectOr(st.Project, st.Name),
			Part:          st.Part,
			Description:   st.Description,
			Image:         st.Image,
			Uses:          st.Uses,
			Library:       st.Library,
			Stages:        st.Stages,
			ReindexQueued: st.ReindexRequested > 0,
		})
	}
	return out, nil
}

// projectOr falls back to the repository's own name for a row written before
// projects existed. Grouping those under "" would put every such repository in
// one nameless product on the page.
func projectOr(project, name string) string {
	if project == "" {
		return name
	}
	return project
}
