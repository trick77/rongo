package ask

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
)

// libraryMap is shop and billing built on acme-commons, and legacy-crm, which
// is not.
func libraryMap(t *testing.T) projects.Map {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}
	for _, r := range [][3]string{
		{"shop-ui", "shop", "0"}, {"shop-backend", "shop", "0"},
		{"billing-api", "billing", "0"}, {"legacy-crm", "legacy-crm", "0"},
		{"acme-commons", "acme-commons", "1"},
	} {
		if _, err := db.Exec(
			`INSERT INTO repo_state (name, clone_url, project, library) VALUES (?, ?, ?, ?)`,
			r[0], "https://example.invalid/"+r[0]+".git", r[1], r[2]); err != nil {
			t.Fatalf("seed %s: %v", r[0], err)
		}
	}
	for _, e := range [][2]string{{"shop-backend", "acme-commons"}, {"billing-api", "acme-commons"}} {
		if _, err := db.Exec(`INSERT INTO repo_uses (repo, uses) VALUES (?, ?)`, e[0], e[1]); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}
	m, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load() err = %v", err)
	}
	return m
}

func TestProjectCandidatesFoldALibraryIntoTheProductBesideIt(t *testing.T) {
	pm := libraryMap(t)
	repos := repoCandidates([]Candidate{
		{Repo: "acme-commons", ModuleKey: "util", Score: 0.70, Hits: []retrieve.Hit{{ChunkID: 1, Score: 0.70}}},
		{Repo: "shop-backend", ModuleKey: "checkout", Score: 0.60, Hits: []retrieve.Hit{{ChunkID: 2, Score: 0.60}}},
	})

	got := projectCandidates(repos, pm)

	// One button, shop, carrying the library as a member and the library's
	// score: the hit in the shared code is evidence for the product built on it.
	if len(got) != 1 || got[0].Repo != "shop" {
		t.Fatalf("got %+v, want one candidate, shop", got)
	}
	if !reflect.DeepEqual(got[0].Members, []string{"acme-commons", "shop-backend"}) {
		t.Errorf("Members = %v, want the library stored with the product", got[0].Members)
	}
	if got[0].Score != 0.70 || len(got[0].Hits) != 2 {
		t.Errorf("Score = %v, hits = %d, want the library's score and both hits", got[0].Score, len(got[0].Hits))
	}
	if SpansRepos(repos, 0, pm) {
		t.Error("a library beside a product using it does not span two projects")
	}
}

func TestProjectCandidatesPutALibraryOnEveryProductBesideItThatUsesIt(t *testing.T) {
	pm := libraryMap(t)
	repos := repoCandidates([]Candidate{
		{Repo: "shop-backend", ModuleKey: "checkout", Score: 0.60, Hits: []retrieve.Hit{{ChunkID: 1, Score: 0.60}}},
		{Repo: "billing-api", ModuleKey: "invoice", Score: 0.55, Hits: []retrieve.Hit{{ChunkID: 2, Score: 0.55}}},
		{Repo: "acme-commons", ModuleKey: "util", Score: 0.50, Hits: []retrieve.Hit{{ChunkID: 3, Score: 0.50}}},
	})

	got := projectCandidates(repos, pm)

	// Two products, a card between them, and the library on both entries: a
	// chosen entry searches what it stored, and the reader asked about a
	// product built on the library either way.
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want shop and billing", len(got))
	}
	for _, c := range got {
		if !reflect.DeepEqual(c.Members, []string{"acme-commons", c.Repo + "-" + map[string]string{"shop": "backend", "billing": "api"}[c.Repo]}) {
			t.Errorf("%s.Members = %v, want the library beside the product's own repository", c.Repo, c.Members)
		}
	}
	if !SpansRepos(repos, 0, pm) {
		t.Error("two products still span, library or not")
	}
}

func TestProjectCandidatesLeaveALibraryAloneWhenNoProductBesideItUsesIt(t *testing.T) {
	pm := libraryMap(t)
	repos := repoCandidates([]Candidate{
		{Repo: "legacy-crm", ModuleKey: "crm", Score: 0.60, Hits: []retrieve.Hit{{ChunkID: 1, Score: 0.60}}},
		{Repo: "acme-commons", ModuleKey: "util", Score: 0.50, Hits: []retrieve.Hit{{ChunkID: 2, Score: 0.50}}},
	})

	got := projectCandidates(repos, pm)

	if len(got) != 2 || got[0].Repo != "legacy-crm" || got[1].Repo != "acme-commons" {
		t.Fatalf("got %+v, want legacy-crm and the library as a project of one", got)
	}
	if !reflect.DeepEqual(got[1].Members, []string{"acme-commons"}) {
		t.Errorf("library Members = %v", got[1].Members)
	}
}
