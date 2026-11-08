package memory

import "time"

// Row is one standing instruction as the page and the prompt see it.
//
// Table: memories
type Row struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	// Scope is the project or repository the rule is limited to, empty for
	// a rule that holds everywhere.
	Scope string `json:"scope,omitempty"`
	// ScopeLive says Scope still names a project or repository of the index.
	// A scope that left repos.yaml applies everywhere, and the page says so:
	// applying it to nothing would be a quiet drop.
	ScopeLive bool `json:"scope_live"`
	// members is every repository Scope covers, resolved when the rows are
	// read: a project's members, or the repository itself.
	members []string
	// ThreadID is the public address of the thread the rule was said in, for
	// the page's link; empty once that turn is gone.
	ThreadID  string    `json:"thread_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
