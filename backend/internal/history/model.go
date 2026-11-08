package history

import "time"

// Commit is one search result. Author is deliberately not here.
//
// Table: commits
type Commit struct {
	// ID is the commits row, what a turn's record stores.
	ID          int64
	Repo        string
	Branch      string
	SHA         string
	CommittedAt time.Time
	Subject     string
	Body        string
	Paths       []string
}
