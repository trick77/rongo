package sourceview

import (
	"context"
	"strings"
	"testing"
)

// TestRead_stillServesAParkedRepository: parking stops NEW answers, it does not
// revise old ones. A citation written while the repository was live must still
// open at the commit it was read from, or "every claim is citable" would stop
// holding retroactively for threads that were correct when they were answered.
//
// This is the counterpart to the enabled filters in retrieve, projects and ask:
// the record does not filter, and this test is what keeps somebody from
// "consistently" adding one here.
func TestRead_stillServesAParkedRepository(t *testing.T) {
	// Given: an indexed repository that is then parked
	f := newFixture(t, 1<<20)
	if _, err := f.db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'peeq'`); err != nil {
		t.Fatalf("park peeq: %v", err)
	}

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "internal/a.go", f.first)

	// Then
	if err != nil {
		t.Fatalf("Read: %v, want the cited source still served", err)
	}
	if !strings.Contains(got.Content, "func One()") {
		t.Errorf("content = %q, want the file at the cited commit", got.Content)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want main", got.Branch)
	}
}
