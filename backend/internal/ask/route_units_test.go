package ask

import (
	"context"
	"testing"
)

// TestRelated_twoUnitsOfOneRepositoryThatUseEachOtherCompose is the in-repo
// form of the manifest edge: an app and the library it imports are parts of
// one build, and a card asking which was meant would make the reader pick
// half an answer. Two units with no declared link stay two candidates.
func TestRelated_twoUnitsOfOneRepositoryThatUseEachOtherCompose(t *testing.T) {
	db := testDBWithDeps(t, nil)
	for _, u := range [][2]string{{"apps/claims", "claims"}, {"libs/shared", "shared"}, {"apps/other", "other"}} {
		if _, err := db.Exec(`INSERT INTO units (repo, key, kind, name) VALUES ('peeq', ?, 'nx-app', ?)`, u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO unit_deps (repo, from_key, to_key) VALUES ('peeq', 'apps/claims', 'libs/shared')`); err != nil {
		t.Fatal(err)
	}
	r := newTestRouter(t, testLLM(t, func(string) string { return "" }), db)

	linked, err := r.anyDependency(context.Background(), []Candidate{
		{Repo: "peeq", ModuleKey: "apps/claims"}, {Repo: "peeq", ModuleKey: "libs/shared"},
	})
	if err != nil || !linked {
		t.Errorf("app and the library it uses: related = %v, %v; want true", linked, err)
	}
	linked, err = r.anyDependency(context.Background(), []Candidate{
		{Repo: "peeq", ModuleKey: "apps/claims"}, {Repo: "peeq", ModuleKey: "apps/other"},
	})
	if err != nil || linked {
		t.Errorf("two apps with no declared link: related = %v, %v; want false", linked, err)
	}
	linked, err = r.anyDependency(context.Background(), []Candidate{
		{Repo: "peeq", ModuleKey: "backend/internal/rag"}, {Repo: "peeq", ModuleKey: "backend/internal/httpapi"},
	})
	if err != nil || linked {
		t.Errorf("two directories that are not units: related = %v, %v; want false", linked, err)
	}
}
