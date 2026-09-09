package indexer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// TestLogInventory_announcesEveryRepositoryAndTheTotals: nothing verified that a
// boot says what it holds, which is the whole point of the lines.
func TestLogInventory_announcesEveryRepositoryAndTheTotals(t *testing.T) {
	// Given
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "schadenmeldung",
			Part: "backend"},
		{Name: "acme-ui", Snapshot: true, Enabled: true, Project: "schadenmeldung",
			Part: "ui", Uses: []string{"acme-core"}},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	cap := &capture{}

	// When
	LogInventory(ctx, s, slog.New(cap), "/conf/repos.yaml", true)

	// Then
	var configured int
	for _, r := range cap.records {
		if r.Message == "repository configured" {
			configured++
		}
	}
	if configured != 2 {
		t.Errorf("configured lines = %d, want one per repository", configured)
	}
	rec, ok := cap.find("repository list loaded")
	if !ok {
		t.Fatalf("no summary line; logged %v", cap.messages())
	}
	if got, _ := attr(rec, "from_file"); got.Bool() != true {
		t.Errorf("from_file = %v, want true", got)
	}
	if got, _ := attr(rec, "repositories"); got.Int64() != 2 {
		t.Errorf("repositories = %v, want 2", got)
	}
	if got, _ := attr(rec, "snapshots"); got.Int64() != 2 {
		t.Errorf("snapshots = %v, want 2", got)
	}
}

// TestLogInventory_namesTheCorpusNobodyConfigured: the boot that used to say
// nothing at all. With no list on disk the database may still carry a whole
// corpus, and an operator has no other way to tell an empty rongo from one
// serving a configuration that exists nowhere.
func TestLogInventory_namesTheCorpusNobodyConfigured(t *testing.T) {
	// Given
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	cap := &capture{}

	// When: the list did not come from the file
	LogInventory(ctx, s, slog.New(cap), "/conf/repos.yaml", false)

	// Then
	rec, ok := cap.find("serving a corpus no repository list describes")
	if !ok {
		t.Fatalf("the stale-corpus boot is not named; logged %v", cap.messages())
	}
	if got, _ := attr(rec, "from_file"); got.Bool() != false {
		t.Errorf("from_file = %v, want false", got)
	}
	if _, ok := cap.find("repository list loaded"); ok {
		t.Error("the summary claims the list was loaded when it was not")
	}
	// The repositories are still announced: what is held is exactly the
	// question this boot leaves open.
	if _, ok := cap.find("repository configured"); !ok {
		t.Error("no repository was announced on the boot where it matters most")
	}
}

// TestLogInventory_survivesAnUnreadableDatabase: the inventory is a log line,
// never a reason to fail a boot.
func TestLogInventory_survivesAnUnreadableDatabase(t *testing.T) {
	// Given: a database closed out from under it
	db := purgeDB(t)
	s := NewStateStore(db)
	db.Close()
	cap := &capture{}

	// When
	LogInventory(context.Background(), s, slog.New(cap), "/conf/repos.yaml", true)

	// Then
	if _, ok := cap.find("repository inventory unavailable"); !ok {
		t.Errorf("the read failure is not reported; logged %v", cap.messages())
	}
}
