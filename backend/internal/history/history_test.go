package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range []string{"shop", "loom"} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES (?,?,?)`,
			r, "file:///"+r, "main"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func at(day int) string {
	return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func fixture(t *testing.T) *Store {
	t.Helper()
	s := New(testDB(t))
	ctx := context.Background()
	if err := s.Replace(ctx, "shop", []gitrepo.Commit{
		{SHA: "c3", CommittedAt: at(17), Author: "jan", Subject: "Snapshot badge on the Repos page", Body: "A snapshot shows a badge, not a branch.", Paths: []string{"ui/src/RepoList.tsx"}},
		{SHA: "c2", CommittedAt: at(15), Author: "jan", Subject: "Bearer token for Bitbucket", Paths: []string{"backend/internal/gitrepo/gitrepo.go"}},
		{SHA: "c1", CommittedAt: at(2), Author: "jan", Subject: "Corpus swap", Body: "loom to rongo", Paths: []string{"docs/measurements/2026-08-20-corpus-swap.md"}},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := s.Replace(ctx, "loom", []gitrepo.Commit{
		{SHA: "l1", CommittedAt: at(16), Subject: "Loom snapshot handling", Paths: []string{"a.go"}},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return s
}

func TestSearch_windowListsNewestFirstAcrossRepos(t *testing.T) {
	s := fixture(t)

	got, err := s.Search(context.Background(), Query{Repos: []string{"shop", "loom"}, Since: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 || got[0].SHA != "c3" || got[1].SHA != "l1" || got[2].SHA != "c2" {
		t.Fatalf("Search = %+v, want c3 l1 c2", got)
	}
	if got[0].Repo != "shop" || got[0].Branch != "main" || got[0].Paths[0] != "ui/src/RepoList.tsx" {
		t.Errorf("first = %+v", got[0])
	}
	if !got[0].CommittedAt.Equal(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", got[0].CommittedAt)
	}
}

func TestSearch_topicFiltersInsideTheWindow(t *testing.T) {
	s := fixture(t)
	since := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	got, err := s.Search(context.Background(), Query{Repos: []string{"shop", "loom"}, Since: since, Topic: "snapshot handling"})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search = %+v, want the two snapshot commits", got)
	}
	for _, c := range got {
		if c.SHA != "c3" && c.SHA != "l1" {
			t.Errorf("unexpected %+v", c)
		}
	}

	// A topic found only in a path still lands.
	byPath, err := s.Search(context.Background(), Query{Repos: []string{"shop"}, Since: since, Topic: "gitrepo"})
	if err != nil || len(byPath) != 1 || byPath[0].SHA != "c2" {
		t.Errorf("Search by path = %+v, %v", byPath, err)
	}
}

func TestSearch_noReposIsEveryEnabledRepo_andLimitHolds(t *testing.T) {
	s := fixture(t)
	all, err := s.Search(context.Background(), Query{Since: time.Time{}})
	if err != nil || len(all) != 4 {
		t.Errorf("Search with no repos = %v, %v; want all 4", all, err)
	}
	if all[0].ID == 0 {
		t.Error("a result carries no row id")
	}
	// A parked repository is not searched, named or not.
	if _, err := s.db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'loom'`); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Search(context.Background(), Query{Since: time.Time{}})
	named, _ := s.Search(context.Background(), Query{Repos: []string{"loom"}, Since: time.Time{}})
	if len(active) != 3 || len(named) != 0 {
		t.Errorf("parked repo searched: all=%d named=%d", len(active), len(named))
	}
	one, err := s.Search(context.Background(), Query{Repos: []string{"shop"}, Since: time.Time{}, Limit: 1})
	if err != nil || len(one) != 1 || one[0].SHA != "c3" {
		t.Errorf("Search limit 1 = %v, %v", one, err)
	}
}

func TestAppend_skipsHeldCommits_ReplaceDropsThem(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if err := s.Append(ctx, "shop", []gitrepo.Commit{
		{SHA: "c3", CommittedAt: at(17), Subject: "duplicate"},
		{SHA: "c4", CommittedAt: at(18), Subject: "New"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	n, newest, err := s.Count(ctx, "shop")
	if err != nil || n != 4 || newest.Day() != 18 {
		t.Errorf("Count = %d %v %v, want 4 on the 18th", n, newest, err)
	}
	got, _ := s.Search(ctx, Query{Repos: []string{"shop"}, Since: time.Time{}, Topic: "duplicate"})
	if len(got) != 0 {
		t.Errorf("a held commit was overwritten: %+v", got)
	}

	if err := s.Replace(ctx, "shop", nil); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	n, _, _ = s.Count(ctx, "shop")
	var mirror int
	s.db.QueryRow(`SELECT count(*) FROM commits_fts`).Scan(&mirror)
	if n != 0 || mirror != 1 {
		t.Errorf("after Replace(nil): %d commits, %d mirror rows; want 0 and loom's 1", n, mirror)
	}
}

func TestInsert_refusesAnUnparseableDate(t *testing.T) {
	s := New(testDB(t))
	err := s.Append(context.Background(), "shop", []gitrepo.Commit{{SHA: "x", CommittedAt: "yesterday", Subject: "s"}})
	if err == nil {
		t.Error("a bad date was stored")
	}
}
