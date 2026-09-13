package stages

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
)

func stagesDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range []struct {
		name    string
		enabled int
	}{{"acme-infra", 1}, {"acme-service", 1}, {"old-infra", 0}} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, enabled) VALUES (?, 'x', 'main', ?)`, r.name, r.enabled); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

var declared = []repos.Stage{
	{Name: "prod", Prefix: "prod/", Aliases: []string{"production", "produktion"}},
	{Name: "intg", Prefix: "intg/"},
}

func TestSyncAndLoad(t *testing.T) {
	db := stagesDB(t)
	ctx := context.Background()
	if err := Sync(ctx, db, "acme-infra", declared); err != nil {
		t.Fatal(err)
	}
	// A parked repository's stages are stored and not loaded.
	if err := Sync(ctx, db, "old-infra", declared[:1]); err != nil {
		t.Fatal(err)
	}

	got, err := Load(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := Set{
		{Repo: "acme-infra", Name: "intg", Prefix: "intg/"},
		{Repo: "acme-infra", Name: "prod", Prefix: "prod/", Aliases: []string{"production", "produktion"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	// Replaced, not accumulated: dropping a stage from the file drops it.
	if err := Sync(ctx, db, "acme-infra", declared[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ = Load(ctx, db)
	if len(got) != 1 || got[0].Name != "prod" {
		t.Errorf("after re-sync Load = %+v, want prod only", got)
	}
}

func testSet() Set {
	return Set{
		{Repo: "acme-infra", Name: "prod", Prefix: "prod/", Aliases: []string{"production", "produktion"}},
		{Repo: "acme-infra", Name: "intg", Prefix: "intg/"},
		{Repo: "other-infra", Name: "prod", Prefix: "overlays/prod/"},
	}
}

func TestSet_namesAndResolve(t *testing.T) {
	s := testSet()
	if got := s.Names(); !reflect.DeepEqual(got, []string{"intg", "prod"}) {
		t.Errorf("Names = %v", got)
	}
	for word, want := range map[string]string{"prod": "prod", "Production": "prod", "PRODUKTION": "prod", "intg": "intg", "integration": "", "": ""} {
		got, ok := s.Resolve(word)
		if got != want || ok != (want != "") {
			t.Errorf("Resolve(%q) = %q, %v; want %q", word, got, ok, want)
		}
	}
}

func TestSet_mentioned(t *testing.T) {
	s := testSet()
	cases := map[string][]string{
		"how often is the digest sent in production?":          {"prod"},
		"Wie oft wird der Digest in der Produktion verschickt": {"prod"},
		"and on intg, and prod?":                               {"intg", "prod"},
		"how is the RIMEX integration done":                    nil,
		"the productive path through the code":                 nil,
		"how often is the digest sent":                         nil,
	}
	for q, want := range cases {
		if got := s.Mentioned(q); !reflect.DeepEqual(got, want) {
			t.Errorf("Mentioned(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestSet_prefixesAndOf(t *testing.T) {
	s := testSet()
	if got := s.Prefixes("prod"); !reflect.DeepEqual(got, map[string]string{"acme-infra": "prod/", "other-infra": "overlays/prod/"}) {
		t.Errorf("Prefixes(prod) = %v", got)
	}
	// A repository declaring stages but not the asked one is narrowed to
	// nothing, not left wide open.
	if got := s.Prefixes("intg"); got["acme-infra"] != "intg/" || got["other-infra"] != noSuchStage {
		t.Errorf("Prefixes(intg) = %v", got)
	}
	if got := s.Prefixes(""); got != nil {
		t.Errorf("Prefixes(\"\") = %v, want nil", got)
	}
	for _, c := range []struct{ repo, path, want string }{
		{"acme-infra", "prod/intranet/application.properties", "prod"},
		{"acme-infra", "intg/x.yaml", "intg"},
		{"acme-infra", "production/x.yaml", ""},
		{"acme-infra", "resources/base.yaml", ""},
		{"acme-service", "prod/x.yaml", ""},
	} {
		if got := s.Of(c.repo, c.path); got != c.want {
			t.Errorf("Of(%s, %s) = %q, want %q", c.repo, c.path, got, c.want)
		}
	}
}
