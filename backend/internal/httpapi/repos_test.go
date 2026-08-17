package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/store"
)

// fakeRepos stands in for the status source. No test in this package touches a
// database or a git remote.
type fakeRepos struct {
	out []RepoStatus
	err error
}

func (f fakeRepos) RepoStatus(context.Context) ([]RepoStatus, error) { return f.out, f.err }

// authDB is a migrated database, because the auth service records the user it
// logs in. Nothing here reaches a network.
func authDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func devAuth(t *testing.T) *auth.Service {
	t.Helper()
	return auth.NewService(authDB(t), "dev", "")
}

func getRepos(t *testing.T, deps Deps) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	rec := httptest.NewRecorder()
	NewServer(deps).ServeHTTP(rec, req)
	return rec
}

func TestRepos_reportsWhatThePageShows(t *testing.T) {
	// Given
	when := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{{
		Name: "peeq", Branch: "master", LastSHA: "abc1234", LastRunAt: when,
		Files: 412, Chunks: 3120, Modules: 34, Enabled: true,
	}}}}

	// When
	rec := getRepos(t, deps)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d repositories, want 1", len(got))
	}
	for key, want := range map[string]any{
		"name":     "peeq",
		"branch":   "master",
		"last_sha": "abc1234",
		"files":    float64(412),
		"chunks":   float64(3120),
		"modules":  float64(34),
		"enabled":  true,
	} {
		if got[0][key] != want {
			t.Errorf("%s = %v, want %v", key, got[0][key], want)
		}
	}
}

func TestRepos_aVanishedBranchIsLoudNotBlank(t *testing.T) {
	// A configured branch that disappeared upstream must reach the page as an
	// error. A silent stop leaves an index frozen at months-old code while the
	// page looks healthy — and answers keep citing that code as current.
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{{
		Name: "shop-backend", Branch: "release-2024.3", Enabled: true,
		LastError: "branch release-2024.3 is gone upstream",
	}}}}

	rec := getRepos(t, deps)

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got[0]["last_error"] != "branch release-2024.3 is gone upstream" {
		t.Errorf("last_error = %v, want the upstream failure verbatim", got[0]["last_error"])
	}
}

func TestRepos_deactivatedRepositoriesAreListedNotHidden(t *testing.T) {
	// A repository dropped from repos.yaml is deactivated, not deleted: its
	// index survives until an explicit purge. Hiding it would make a typo in
	// the YAML look like a repository that never existed.
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{
		{Name: "peeq", Enabled: true},
		{Name: "legacy-crm", Enabled: false, Chunks: 900},
	}}}

	rec := getRepos(t, deps)

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d repositories, want the deactivated one listed too", len(got))
	}
	if got[1]["enabled"] != false {
		t.Errorf("enabled = %v, want false so the page can mark it", got[1]["enabled"])
	}
}

func TestRepos_requiresAuth(t *testing.T) {
	// Given: token mode, and a caller presenting none. Dev mode would be no
	// test at all — it logs every caller in automatically, so the assertion
	// would pass with the route unguarded.
	deps := Deps{
		Auth:  auth.NewService(authDB(t), "token", "s3cret"),
		Repos: fakeRepos{out: []RepoStatus{{Name: "peeq"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	rec := httptest.NewRecorder()

	// When
	NewServer(deps).ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — the repository list is not public", rec.Code)
	}
}

func TestRepos_withoutASourceAnswers503NotAnEmptyList(t *testing.T) {
	// An empty list would read as "no repositories configured", which is a
	// different fact from "this deployment cannot tell you".
	rec := getRepos(t, Deps{Auth: devAuth(t)})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRepos_aFailingSourceIsAnErrorNotAnEmptyList(t *testing.T) {
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{err: errors.New("database is locked")}}

	rec := getRepos(t, deps)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Error("empty error body")
	}
}
