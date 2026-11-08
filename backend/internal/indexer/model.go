package indexer

import "time"

// Counts is what one indexing run produced.
//
// Table: -
type Counts struct {
	Files  int
	Chunks int
}

// RepoState is a repository's recorded indexing state.
//
// Table: repo_state
type RepoState struct {
	Name     string
	CloneURL string
	Branch   string
	// TokenEnv names the environment variable holding this repository's forge
	// token. The name travels; the value is read at fetch time and never
	// persisted.
	TokenEnv string
	// TokenUser is the basic-auth username the token is sent with; see
	// repos.Spec. Empty means x-access-token.
	TokenUser string
	// TokenAuth is "" for basic auth or "bearer" for a header; see repos.Spec.
	TokenAuth string
	Enabled   bool
	LastSHA   string
	LastError string
	// LastRunAt is the last poll, whatever it found: the poller's pulse.
	LastRunAt time.Time
	// LastIndexedAt is the last time the index was written: the moment a
	// push became answerable. Zero until the first index run.
	LastIndexedAt time.Time
	Files         int
	Chunks        int
	// ReindexRequested is the pending request generation, zero for none: an
	// admin asked for a full re-index and the poller has not run it yet.
	ReindexRequested int
	// Project, Part, Description and Uses come from repos.yaml, not from the
	// checkout: they say which product this repository belongs to and what part
	// it plays in it. Nothing here is derived from code, and none of it is ever
	// embedded, indexed or cited.
	Project     string
	Part        string
	Description string
	Uses        []string
	// Image is the container image the repository is built into, tag-less;
	// what a release turn pairs a deployed version with. See repos.Spec.Image.
	Image string
	// Library says the entry came from the `libraries:` block: a project of
	// one that any project's uses may name. See repos.Spec.Library.
	Library bool
	// Stages are the deployment stages the entry declares, by name; empty
	// for every ordinary repository.
	Stages []string
}

// Snapshot reports whether this is a hand-extracted source drop rather than a
// clone: no remote to fetch from, and a commit rongo made itself.
//
// Derived rather than stored. repos.Load refuses a clone_url on a snapshot and
// requires one everywhere else, so the empty URL already IS the fact; a column
// beside it could only ever disagree with it, and there is no migration to get
// wrong.
func (r RepoState) Snapshot() bool { return r.CloneURL == "" }

// VecCompaction is what one CompactVectors call found and did. Chunks are
// vec0's storage chunks (chunks_vec_chunks rows), not rongo's chunks.
//
// Table: -
type VecCompaction struct {
	Compacted    bool
	Rows         int64
	ChunksBefore int64
	ChunksAfter  int64
}
