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

	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/modules"
)

// Store reads repository state and derives the module count from the index.
type Store struct {
	db    *sql.DB
	state *indexer.StateStore
	opts  modules.Opts
}

// New builds a Store. opts are the clustering constants; they decide how many
// modules a repository reports, so the page and the routing layer must be given
// the same ones.
func New(db *sql.DB, opts modules.Opts) *Store {
	return &Store{db: db, state: indexer.NewStateStore(db), opts: opts}
}

// RepoStatus reports every repository, including deactivated ones. A repository
// that left repos.yaml keeps its index until an explicit purge and stays on the
// page: a typo in the YAML must not make hours of indexing look like a
// repository that never existed.
func (s *Store) RepoStatus(ctx context.Context) ([]httpapi.RepoStatus, error) {
	all, err := s.state.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("read repository state: %w", err)
	}
	out := make([]httpapi.RepoStatus, 0, len(all))
	for _, st := range all {
		mods, err := modules.Cluster(ctx, s.db, st.Name, s.opts)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", st.Name, err)
		}
		out = append(out, httpapi.RepoStatus{
			Name:      st.Name,
			Branch:    st.Branch,
			LastSHA:   st.LastSHA,
			LastRunAt: st.LastRunAt,
			Files:     st.Files,
			Chunks:    st.Chunks,
			Modules:   len(mods),
			Enabled:   st.Enabled,
			LastError: st.LastError,
		})
	}
	return out, nil
}
