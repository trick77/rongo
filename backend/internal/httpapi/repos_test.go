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
	indexed := time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{{
		Name: "peeq", Branch: "master", LastSHA: "abc1234", LastRunAt: when, LastIndexedAt: indexed,
		Files: 412, Chunks: 3120, Modules: 34, Enabled: true, Image: "registry.example.invalid/acme/peeq",
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
		"name":            "peeq",
		"branch":          "master",
		"last_sha":        "abc1234",
		"last_run_at":     "2026-08-17T09:30:00Z",
		"last_indexed_at": "2026-08-15T06:00:00Z",
		"files":           float64(412),
		"chunks":          float64(3120),
		"modules":         float64(34),
		"enabled":         true,
		"image":           "registry.example.invalid/acme/peeq",
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

// TestRepos_isNeverCached: the endpoint carried no cache directives, so a
// browser was free to serve its own copy heuristically — and did. After an
// operator corrected repos.yaml and restarted, the page went on drawing the
// previous configuration's `uses` arrows, which reads as rongo ignoring the
// file rather than as the browser ignoring the server. A status page that can
// be served stale is a status page that lies.
func TestRepos_isNeverCached(t *testing.T) {
	// Given
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{{
		Name: "peeq", Branch: "master", Enabled: true,
	}}}}

	// When
	rec := getRepos(t, deps)

	// Then
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

type fakeReindex struct {
	names []string
	all   int
	known map[string]bool
}

func (f *fakeReindex) RequestReindex(_ context.Context, name string) (bool, error) {
	if !f.known[name] {
		return false, nil
	}
	f.names = append(f.names, name)
	return true, nil
}

func (f *fakeReindex) RequestReindexAll(context.Context) (int, error) {
	f.all++
	return len(f.known), nil
}

func postReindex(t *testing.T, deps Deps, name string, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/repos/reindex"
	if name != "" {
		path = "/api/repos/" + name + "/reindex"
	}
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = req.WithContext(auth.WithUser(req.Context(), auth.User{ID: 1, Subject: "someone", IsAdmin: admin}))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	// Straight at the handler, not through the middleware: every header mode
	// of the middleware admits an admin, and it is the handler's own gate that
	// is under test here. The middleware's admission is tested in internal/auth.
	NewServer(deps).handleReindex(rec, req)
	return rec
}

func TestReindex_isTheAdminsToAsk(t *testing.T) {
	f := &fakeReindex{known: map[string]bool{"peeq": true}}
	deps := Deps{Auth: devAuth(t), Reindex: f}

	if rec := postReindex(t, deps, "peeq", false); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin: status %d, want 403", rec.Code)
	}
	if len(f.names) != 0 {
		t.Fatal("a non-admin's request reached the poller")
	}
	rec := postReindex(t, deps, "peeq", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("admin: status %d (%s), want 202", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["queued"] != 1 {
		t.Errorf("body = %v, %v; want queued 1", body, err)
	}
	if len(f.names) != 1 || f.names[0] != "peeq" {
		t.Errorf("requested %v, want peeq", f.names)
	}
}

func TestReindex_unknownOrParkedIs404AndAllCountsWhatItQueued(t *testing.T) {
	f := &fakeReindex{known: map[string]bool{"peeq": true, "loom": true}}
	deps := Deps{Auth: devAuth(t), Reindex: f}

	if rec := postReindex(t, deps, "nobody", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: status %d, want 404", rec.Code)
	}
	rec := postReindex(t, deps, "", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("all: status %d, want 202", rec.Code)
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["queued"] != 2 {
		t.Errorf("body = %v, %v; want queued 2", body, err)
	}
	if f.all != 1 {
		t.Errorf("RequestReindexAll called %d times, want 1", f.all)
	}
	// No poller here: the page offers nothing and the endpoint says so.
	if rec := postReindex(t, Deps{Auth: devAuth(t)}, "", true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without a poller: status %d, want 503", rec.Code)
	}
}

func TestRepos_saysWhenAReindexIsQueued(t *testing.T) {
	deps := Deps{Auth: devAuth(t), Repos: fakeRepos{out: []RepoStatus{{Name: "peeq", ReindexQueued: true}}}}
	rec := getRepos(t, deps)
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0]["reindex_queued"] != true {
		t.Errorf("body = %v, want reindex_queued true", out)
	}
}

type failingReindex struct{}

func (failingReindex) RequestReindex(context.Context, string) (bool, error) {
	return false, errors.New("locked")
}

func (failingReindex) RequestReindexAll(context.Context) (int, error) { return 0, errors.New("locked") }

func TestReindex_aStoreFailureIs500AndNoUserIs401(t *testing.T) {
	deps := Deps{Auth: devAuth(t), Reindex: failingReindex{}}
	if rec := postReindex(t, deps, "peeq", true); rec.Code != http.StatusInternalServerError {
		t.Errorf("one repository, store failing: status %d, want 500", rec.Code)
	}
	if rec := postReindex(t, deps, "", true); rec.Code != http.StatusInternalServerError {
		t.Errorf("all, store failing: status %d, want 500", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/repos/reindex", nil)
	rec := httptest.NewRecorder()
	NewServer(deps).handleReindex(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no user on the request: status %d, want 401", rec.Code)
	}
}
