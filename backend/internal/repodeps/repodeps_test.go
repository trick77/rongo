package repodeps

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

// testDB opens a fresh migrated database and returns it ready for repo_deps
// writes. Callers add repo_state rows for whatever repository names they use,
// since repo_deps.repo is a foreign key into repo_state(name).
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}
	// repo_deps.repo is a foreign key into repo_state(name); seed the rows
	// for every repository name the tests in this file use.
	for _, repo := range []string{"peeq", "go-sqlite3", "loom"} {
		if _, err := db.Exec(
			`INSERT INTO repo_state (name, clone_url) VALUES (?, ?)`,
			repo, "https://example.invalid/"+repo+".git"); err != nil {
			t.Fatalf("seed repo_state(%s): %v", repo, err)
		}
	}
	return db
}

// mustSync inserts the repo_state row a repository needs (if not already
// present) and syncs its single go.mod into repo_deps.
func mustSync(t *testing.T, db *sql.DB, repo, goMod string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO repo_state (name, clone_url) VALUES (?, ?)`,
		repo, "https://example.invalid/"+repo+".git"); err != nil {
		t.Fatalf("insert repo_state(%s): %v", repo, err)
	}
	if err := Sync(context.Background(), db, repo, map[string][]byte{"go.mod": []byte(goMod)}); err != nil {
		t.Fatalf("sync %s: %v", repo, err)
	}
}

// assertDepends checks DependsOn(a, b) against want.
func assertDepends(t *testing.T, db *sql.DB, a, b string, want bool) {
	t.Helper()
	got, err := DependsOn(context.Background(), db, a, b)
	if err != nil {
		t.Fatalf("DependsOn(%s, %s): %v", a, b, err)
	}
	if got != want {
		t.Errorf("DependsOn(%s, %s) = %v, want %v", a, b, got, want)
	}
}

func TestParseReadsTheModuleLineAndItsRequirements(t *testing.T) {
	// Given a go.mod in the shape the corpus actually has: a require block,
	// an indirect marker, a comment and a replace directive.
	src := []byte(`module github.com/trick77/peeq

go 1.24

require (
	github.com/ncruces/go-sqlite3 v0.23.3
	github.com/asg017/sqlite-vec-go-bindings v0.1.7-alpha.2 // indirect
)

require golang.org/x/sync v0.8.0

replace github.com/ncruces/go-sqlite3 => ../go-sqlite3
`)

	// When
	publishes, requires, err := Parse(src)

	// Then
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if publishes != "github.com/trick77/peeq" {
		t.Errorf("publishes = %q", publishes)
	}
	want := map[string]bool{
		"github.com/ncruces/go-sqlite3":            true,
		"github.com/asg017/sqlite-vec-go-bindings": true,
		"golang.org/x/sync":                        true,
	}
	if len(requires) != len(want) {
		t.Fatalf("requires = %v, want %d entries", requires, len(want))
	}
	for _, r := range requires {
		if !want[r] {
			t.Errorf("unexpected requirement %q", r)
		}
	}
}

func TestParseRejectsAFileWithoutAModuleLine(t *testing.T) {
	// A go.mod with no module path names nothing, and writing an empty
	// coordinate would make every repo look like it publishes the same thing.
	if _, _, err := Parse([]byte("go 1.24\n")); err == nil {
		t.Fatal("want an error for a go.mod without a module line")
	}
}

func TestSyncAndDependsOnJoinTwoRepositories(t *testing.T) {
	// Given peeq requires go-sqlite3, go-sqlite3 publishes it, and peeq also
	// requires forty coordinates nobody in the corpus publishes.
	db := testDB(t)
	ctx := context.Background()

	if err := Sync(ctx, db, "peeq", map[string][]byte{
		"backend/go.mod": []byte("module github.com/trick77/peeq\n\nrequire (\n\tgithub.com/ncruces/go-sqlite3 v0.23.3\n\tgithub.com/spf13/cobra v1.8.0\n)\n"),
	}); err != nil {
		t.Fatalf("sync peeq: %v", err)
	}
	if err := Sync(ctx, db, "go-sqlite3", map[string][]byte{
		"go.mod": []byte("module github.com/ncruces/go-sqlite3\n"),
	}); err != nil {
		t.Fatalf("sync go-sqlite3: %v", err)
	}

	// When / Then: the edge that joins is the one where a requirement meets a
	// published coordinate. The unpublished forty simply never join.
	assertDepends(t, db, "peeq", "go-sqlite3", true)
	assertDepends(t, db, "go-sqlite3", "peeq", false)
}

func TestDependsOnAcceptsASubpackagePath(t *testing.T) {
	// A repository that requires .../go-sqlite3/driver depends on the
	// repository publishing .../go-sqlite3. Requiring a prefix match in the
	// other direction would make every repo depend on every shorter path.
	db := testDB(t)
	mustSync(t, db, "loom", "module github.com/trick77/loom\n\nrequire github.com/ncruces/go-sqlite3/driver v0.23.3\n")
	mustSync(t, db, "go-sqlite3", "module github.com/ncruces/go-sqlite3\n")

	assertDepends(t, db, "loom", "go-sqlite3", true)
}

func TestSyncWritesTheGoodManifestAndReportsTheBrokenOne(t *testing.T) {
	// Given a repository with two go.mod files, one that parses and one that
	// does not. Losing the good edges because a sibling manifest is broken
	// would force the router to ask a clarifying question where composition
	// actually holds — worse than the alternative.
	db := testDB(t)
	ctx := context.Background()

	err := Sync(ctx, db, "peeq", map[string][]byte{
		"backend/go.mod": []byte("module github.com/trick77/peeq\n\nrequire github.com/ncruces/go-sqlite3 v0.23.3\n"),
		"tools/go.mod":   []byte("not a go.mod at all"),
	})

	// Then: an error names the skipped path, but the good manifest's edges
	// still landed.
	if err == nil {
		t.Fatal("want a non-nil error naming the skipped manifest")
	}
	if !strings.Contains(err.Error(), "tools/go.mod") {
		t.Errorf("error %q does not name the skipped path", err.Error())
	}

	mustSync(t, db, "go-sqlite3", "module github.com/ncruces/go-sqlite3\n")
	assertDepends(t, db, "peeq", "go-sqlite3", true)
}

func TestSyncWritesNoRowsWhenTheOnlyManifestIsBroken(t *testing.T) {
	// Given a repository whose single go.mod does not parse.
	db := testDB(t)
	ctx := context.Background()

	err := Sync(ctx, db, "peeq", map[string][]byte{
		"backend/go.mod": []byte("not a go.mod at all"),
	})
	if err == nil {
		t.Fatal("want a non-nil error for the unparsable manifest")
	}

	// Then: repo_deps holds zero rows for peeq — there is nothing to compose
	// or route with.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM repo_deps WHERE repo = ?`, "peeq").Scan(&count); err != nil {
		t.Fatalf("count repo_deps: %v", err)
	}
	if count != 0 {
		t.Errorf("repo_deps rows for peeq = %d, want 0", count)
	}
}

func TestSyncReplacesTheRepositorysRows(t *testing.T) {
	// A repository that drops a dependency must stop depending on it: leaving
	// the old row would keep suppressing a clarification that is now correct.
	db := testDB(t)
	mustSync(t, db, "peeq", "module github.com/trick77/peeq\n\nrequire github.com/ncruces/go-sqlite3 v0.23.3\n")
	mustSync(t, db, "peeq", "module github.com/trick77/peeq\n")
	mustSync(t, db, "go-sqlite3", "module github.com/ncruces/go-sqlite3\n")

	assertDepends(t, db, "peeq", "go-sqlite3", false)
}
