package projects

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

// seed opens a migrated database and fills repo_state and repo_uses directly.
// SyncSpecs is the real writer, but this package only reads, and going through
// the indexer would drag its whole dependency tree into a test about grouping.
func seed(t *testing.T, rows [][4]string, edges [][2]string) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO repo_state (name, clone_url, project, part, description)
			 VALUES (?, ?, ?, ?, ?)`,
			r[0], "https://example.invalid/"+r[0]+".git", r[1], r[2], r[3]); err != nil {
			t.Fatalf("seed repo_state(%s): %v", r[0], err)
		}
	}
	for _, e := range edges {
		if _, err := db.Exec(`INSERT INTO repo_uses (repo, uses) VALUES (?, ?)`, e[0], e[1]); err != nil {
			t.Fatalf("seed repo_uses(%s -> %s): %v", e[0], e[1], err)
		}
	}
	return db
}

func shopDB(t *testing.T) *sql.DB {
	t.Helper()
	return seed(t,
		[][4]string{
			{"shop-ui", "shop", "ui", "Customer-facing storefront, React."},
			{"shop-backend", "shop", "backend", "Storefront API and checkout."},
			{"shop-events", "shop", "consumer", "Kafka consumer, ingests order events."},
			{"legacy-crm", "legacy-crm", "", ""},
		},
		[][2]string{{"shop-ui", "shop-backend"}},
	)
}

func TestLoad_groupsMembersUnderTheirProject(t *testing.T) {
	// Given
	m, err := Load(context.Background(), shopDB(t))

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if got := m.Members("shop"); len(got) != 3 ||
		got[0] != "shop-backend" || got[1] != "shop-events" || got[2] != "shop-ui" {
		t.Errorf("Members(shop) = %v, want the three sorted by name", got)
	}
	if got := m.Of("shop-ui"); got != "shop" {
		t.Errorf("Of(shop-ui) = %q, want shop", got)
	}
}

func TestLoad_aProjectOfOneIsStillAProject(t *testing.T) {
	// The common case, and rongo's own: one repository, named after itself.
	m, _ := Load(context.Background(), shopDB(t))

	if got := m.Of("legacy-crm"); got != "legacy-crm" {
		t.Errorf("Of(legacy-crm) = %q, want legacy-crm", got)
	}
	if got := m.Members("legacy-crm"); len(got) != 1 || got[0] != "legacy-crm" {
		t.Errorf("Members(legacy-crm) = %v, want [legacy-crm]", got)
	}
}

func TestOf_anUnknownRepositoryIsItsOwnProject(t *testing.T) {
	// A map read a moment before a repository was added must not answer with an
	// empty label: an empty project name would group every unknown repository
	// together and card them as one product. Falling back to the repository's
	// own name degrades to the behaviour rongo had before projects existed.
	m, _ := Load(context.Background(), shopDB(t))

	if got := m.Of("never-heard-of-it"); got != "never-heard-of-it" {
		t.Errorf("Of() = %q, want the repository name back", got)
	}
	if got := m.Members("never-heard-of-it"); len(got) != 0 {
		t.Errorf("Members() = %v, want empty for a project that does not exist", got)
	}
}

func TestLoad_carriesPartDescriptionAndEdges(t *testing.T) {
	// The structure block and the Projects page both read this.
	m, _ := Load(context.Background(), shopDB(t))

	p, ok := m.Project("shop")
	if !ok {
		t.Fatal("Project(shop) not found")
	}
	by := map[string]Repo{}
	for _, r := range p.Members {
		by[r.Name] = r
	}
	if by["shop-ui"].Part != "ui" || by["shop-ui"].Description != "Customer-facing storefront, React." {
		t.Errorf("shop-ui = %+v, want its part and description", by["shop-ui"])
	}
	if got := by["shop-ui"].Uses; len(got) != 1 || got[0] != "shop-backend" {
		t.Errorf("shop-ui.Uses = %v, want [shop-backend]", got)
	}
	if got := by["shop-events"].Uses; len(got) != 0 {
		t.Errorf("shop-events.Uses = %v, want empty — a queue reader has no sibling", got)
	}
}

func TestLoad_treatsAMissingProjectAsAProjectOfOne(t *testing.T) {
	// repos.Load refuses an entry without a project, so a row with an empty one
	// can only come from a database written before this shipped. It groups
	// under its own name rather than under "", which would card every such
	// repository as a single nameless product.
	db := seed(t, [][4]string{{"old-repo", "", "", ""}}, nil)

	m, err := Load(context.Background(), db)

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if got := m.Of("old-repo"); got != "old-repo" {
		t.Errorf("Of(old-repo) = %q, want old-repo", got)
	}
}

func TestSpans_countsProjectsNotRepositories(t *testing.T) {
	// The whole point of the routing change: three repositories of one product
	// are one thing to choose, not three.
	m, _ := Load(context.Background(), shopDB(t))

	if got := m.Distinct([]string{"shop-ui", "shop-backend", "shop-events"}); got != 1 {
		t.Errorf("Distinct(three shop repos) = %d, want 1", got)
	}
	if got := m.Distinct([]string{"shop-ui", "legacy-crm"}); got != 2 {
		t.Errorf("Distinct(shop + legacy-crm) = %d, want 2", got)
	}
	if got := m.Distinct(nil); got != 0 {
		t.Errorf("Distinct(nil) = %d, want 0", got)
	}
}

func TestCovers_isTrueOnlyWhenEveryMemberIsThere(t *testing.T) {
	// Scope.Projects is what suppresses answerCompare, so a partial cover must
	// NOT count: a reader who named one repository of a project asked about
	// that repository, and the turn behaves exactly as it did before.
	m, _ := Load(context.Background(), shopDB(t))

	if got := m.Covered([]string{"shop-ui", "shop-backend", "shop-events"}); len(got) != 1 || got[0] != "shop" {
		t.Errorf("Covered(all three) = %v, want [shop]", got)
	}
	if got := m.Covered([]string{"shop-ui", "shop-backend"}); len(got) != 0 {
		t.Errorf("Covered(two of three) = %v, want empty", got)
	}
	if got := m.Covered([]string{"shop-ui", "shop-backend", "shop-events", "legacy-crm"}); len(got) != 2 {
		t.Errorf("Covered(both projects whole) = %v, want both", got)
	}
}
