package ask

import "time"

// Citation is one entry of the evidence panel. The branch travels with it
// because a forge URL without one may 404 off the default branch. The SHA is
// the commit the cited file was indexed at: the source viewer reads the file
// at that commit, so the cited lines are the lines the answer was written
// from even after the branch has moved on.
//
// A commit citation (Kind = SourceCommit) cites a change rather than lines:
// SHA is the commit, Path and the lines are empty, and Subject and
// CommittedAt are what the chip shows. It opens in the commit view, never in
// the file viewer.
//
// Table: citations
type Citation struct {
	Marker    int    `json:"marker"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	SHA       string `json:"sha"`
	Kind      string `json:"kind,omitempty"`
	Subject   string `json:"subject,omitempty"`
	// CommittedAt is RFC 3339 in UTC; empty for a file citation.
	CommittedAt string `json:"committed_at,omitempty"`
}

// Source is one piece of code the answer may be built on, and the reason it is
// here. The reason is not decoration: it is what lets an answer say "this file
// came in because grant.go calls NewGrant" instead of presenting twenty files
// as equally relevant.
//
// Table: chunks
type Source struct {
	ChunkID   int64
	Repo      string
	Branch    string
	Path      string
	Symbol    string
	StartLine int
	EndLine   int
	// SHA is the commit the file was indexed at; see retrieve.Hit.SHA.
	SHA  string
	Text string
	// Reason is "hit" for something the search returned,
	// "reference:<symbol>" for something a symbol hop reached, or
	// "edge:<kind> <value> from <repo>/<path>" for something reached across a
	// repository boundary on a shared queue name or route, or
	// "include:<stem> from <repo>/<path>" for an M/TEXT file a template
	// includes.
	Reason string
	// Hop is 0 for a search hit and counts up from there.
	Hop int
	// Kind is "" for a chunk of a file and SourceCommit for an entry of the
	// commit lane, where SHA is the commit itself, Path and the lines are
	// empty, Text is the message body, and the fields below are set.
	Kind string
	// CommitID is the commits row, the way ChunkID is the chunks row; it is
	// what the record stores so a re-explain can read the commit back.
	CommitID    int64
	Subject     string
	CommittedAt time.Time
	// Paths are the files the commit changed.
	Paths []string
}

// IsCommit reports whether the source is a commit rather than a chunk.
func (s Source) IsCommit() bool { return s.Kind == SourceCommit }

// ModelRef is one process model file the index carries.
//
// Table: files
type ModelRef struct {
	Path string
	SHA  string
}

// RepoHead is what the release turn needs to know about a repository's
// checkout: the commit the index was built from, the commit the remote
// branch is at, and whether the entry is a snapshot, which has no tags.
//
// Table: repo_state
type RepoHead struct {
	SHA      string
	Remote   string
	Branch   string
	Snapshot bool
}
