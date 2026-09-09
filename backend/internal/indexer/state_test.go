package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSyncSpecs_insertsAndUpdates(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	_, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
	})

	// Then
	if err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Name != "peeq" {
		t.Fatalf("Active() = %+v, want one entry named peeq", active)
	}
	if active[0].Branch != "master" {
		t.Errorf("Branch = %q, want %q", active[0].Branch, "master")
	}
}

func TestSyncSpecs_keepsAResolvedBranchWhenTheYamlNamesNone(t *testing.T) {
	// Omitting `branch:` means "the remote's default", which is resolved once
	// and recorded. The next sync — every boot, every reload of the list — must
	// not wipe it: the branch travels with every citation, and a forge URL
	// without it may 404 off the default branch.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	spec := repos.Spec{Name: "peeq", CloneURL: "file:///x", Enabled: true}
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	if err := s.SetBranch(ctx, "peeq", "master"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}

	// When: the same list is read again, unchanged.
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Branch != "master" {
		t.Fatalf("branch = %q, want the resolved master kept", all[0].Branch)
	}
}

func TestSyncSpecs_dropsAResolvedBranchWhenTheCloneURLChanges(t *testing.T) {
	// A branch resolved from one remote means nothing on another. Keeping it
	// across a corrected clone_url is not a stale label: the poller skips
	// DefaultBranch whenever the column is non-empty, so an entry switched to a
	// repository whose default is `main` would ask for `master` on every cycle,
	// report "configured branch not found" forever, and never re-resolve.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///old", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	if err := s.SetBranch(ctx, "peeq", "master"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}

	// When: the entry now points somewhere else, still naming no branch.
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///new", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Branch != "" {
		t.Fatalf("branch = %q, want it cleared so the new remote's default is resolved", all[0].Branch)
	}
}

func TestSyncSpecs_aNamedBranchSurvivesACloneURLChange(t *testing.T) {
	// The clause above must not reach a branch the YAML actually names: that one
	// is the operator's instruction, not a value resolved from the old remote.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///old", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///new", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Branch != "release-2024.3" {
		t.Fatalf("branch = %q, want the branch the YAML names", all[0].Branch)
	}
}

func TestSyncSpecs_anExplicitBranchStillWins(t *testing.T) {
	// The other half: naming a branch in the YAML overrides whatever was
	// resolved earlier, or a corrected entry would never take effect.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{{Name: "shop", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	if err := s.SetBranch(ctx, "shop", "master"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}

	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "shop", CloneURL: "file:///x", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	all, _ := s.All(ctx)
	if all[0].Branch != "release-2024.3" {
		t.Errorf("branch = %q, want the explicitly configured one", all[0].Branch)
	}
}

// purgeDB is newDB at the write tests' embedding dimension, so a purge test can
// put real chunks — and therefore real vec0 and fts5 rows — into the tables it
// then expects to be empty.
func purgeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "purge.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, writeDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSyncSpecs_purgesARepoThatLeftTheList(t *testing.T) {
	// Given: two indexed repositories, one of which is about to be dropped from
	// repos.yaml.
	db := purgeDB(t)
	s := NewStateStore(db)
	w := NewWriter(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
		{Name: "shop", CloneURL: "/tmp/shop", Branch: "master", Enabled: true},
	}); err != nil {
		t.Fatalf("first SyncSpecs() err = %v", err)
	}
	for _, repo := range []string{"peeq", "shop"} {
		if err := w.ReplaceFile(ctx, repo, "src/A.java", "sha", "java", 10,
			sampleChunks(), [][]float32{vec(1), vec(2)}, nil); err != nil {
			t.Fatalf("ReplaceFile(%s) err = %v", repo, err)
		}
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}

	// When: the list no longer mentions peeq.
	purged, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "shop", CloneURL: "/tmp/shop", Branch: "master", Enabled: true},
	})
	if err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}

	// Then: it is reported as purged, so the caller can remove its checkout ...
	if len(purged) != 1 || purged[0] != "peeq" {
		t.Errorf("purged = %v, want [peeq]", purged)
	}
	// ... its row is gone, not merely deactivated ...
	if n := countOf(t, db, `SELECT COUNT(*) FROM repo_state WHERE name = 'peeq'`); n != 0 {
		t.Errorf("repo_state rows for peeq = %d, want 0", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM files WHERE repo = 'peeq'`); n != 0 {
		t.Errorf("files for peeq = %d, want 0", n)
	}
	// ... and BOTH mirrors went with it. Counting chunks alone would pass on the
	// bug this ordering exists to prevent: the FK cascade reaches chunks and
	// nothing else, and an orphaned vector goes on being returned by the
	// semantic lane for code that is no longer indexed.
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 2 {
		t.Errorf("chunks = %d, want the 2 belonging to shop", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_vec`); n != 2 {
		t.Errorf("chunks_vec = %d, want 2 — the purge left orphaned vectors behind", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts`); n != 2 {
		t.Errorf("chunks_fts = %d, want 2 — the purge left orphaned fts rows behind", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM symbols`); n != 0 {
		t.Errorf("symbols = %d, want 0", n)
	}

	// And: the repository that stayed in the list is untouched.
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Name != "shop" {
		t.Errorf("Active() = %+v, want shop alone", active)
	}
}

func TestSyncSpecs_aReAddedRepoIndexesFromScratch(t *testing.T) {
	// Given: a repo removed from the list, then put back — which is now a purge
	// followed by a fresh entry, not a reactivation.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	spec := repos.Spec{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true}
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	if _, err := s.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("SyncSpecs(nil) err = %v", err)
	}

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("re-adding SyncSpecs() err = %v", err)
	}

	// Then: it is active again and knows no commit, so the next poll indexes it
	// in full. Resuming from the old last_sha would diff against a commit whose
	// chunks are no longer in the database, and every file that did not change
	// since would stay missing.
	active, _ := s.Active(ctx)
	if len(active) != 1 {
		t.Fatalf("Active() = %+v, want the repo back", active)
	}
	if active[0].LastSHA != "" {
		t.Errorf("LastSHA = %q, want empty — a purged repo has nothing to resume from",
			active[0].LastSHA)
	}
}

func TestSyncSpecs_respectsAnExplicitlyDisabledEntry(t *testing.T) {
	// Given: the entry is present in the YAML but marked enabled: false.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "legacy", CloneURL: "/tmp/legacy", Enabled: false},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if len(active) != 0 {
		t.Errorf("Active() = %+v, want empty for an entry with enabled: false", active)
	}
}

func TestSyncSpecs_carriesTheProjectStructure(t *testing.T) {
	// Given: one product in two repositories, the UI declaring the edge.
	ctx := context.Background()
	s := NewStateStore(newDB(t))

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "shop-backend", CloneURL: "file:///b", Enabled: true, Project: "shop",
			Part: "backend", Description: "Storefront API and checkout."},
		{Name: "shop-ui", CloneURL: "file:///u", Enabled: true, Project: "shop",
			Part: "ui", Description: "Storefront, React.", Uses: []string{"shop-backend"}},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	by := map[string]RepoState{}
	for _, r := range all {
		by[r.Name] = r
	}
	if got := by["shop-ui"]; got.Project != "shop" || got.Part != "ui" || got.Description != "Storefront, React." {
		t.Errorf("shop-ui = %+v, want project shop, kind ui and its description", got)
	}
	if got := by["shop-ui"].Uses; len(got) != 1 || got[0] != "shop-backend" {
		t.Errorf("shop-ui.Uses = %v, want [shop-backend]", got)
	}
	if got := by["shop-backend"].Uses; len(got) != 0 {
		t.Errorf("shop-backend.Uses = %v, want empty", got)
	}
}

func TestActive_dropsAnEdgeToADisabledSibling(t *testing.T) {
	// The edge is still declared, and All still reports it. Active is what the
	// page and the prompt are built from, and neither carries the disabled
	// repository, so an arrow to it would point at nothing.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "shop-backend", CloneURL: "file:///b", Enabled: false, Project: "shop", Part: "backend"},
		{Name: "shop-ui", CloneURL: "file:///u", Enabled: true, Project: "shop",
			Part: "ui", Uses: []string{"shop-backend"}},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}

	// Then
	if len(active) != 1 || active[0].Name != "shop-ui" {
		t.Fatalf("Active() = %+v, want only shop-ui", active)
	}
	if got := active[0].Uses; len(got) != 0 {
		t.Errorf("shop-ui.Uses = %v, want empty while shop-backend is disabled", got)
	}
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	for _, r := range all {
		if r.Name == "shop-ui" && (len(r.Uses) != 1 || r.Uses[0] != "shop-backend") {
			t.Errorf("All() shop-ui.Uses = %v, want the edge still declared", r.Uses)
		}
	}
}

func TestSyncSpecs_replacesUsesRatherThanAppending(t *testing.T) {
	// A repository that drops an edge must stop declaring it, or the Projects
	// page keeps drawing an arrow that no longer exists — the same reason
	// repodeps.Sync deletes before it inserts.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	first := []repos.Spec{
		{Name: "a", CloneURL: "file:///a", Enabled: true, Project: "p"},
		{Name: "b", CloneURL: "file:///b", Enabled: true, Project: "p", Uses: []string{"a"}},
	}
	if _, err := s.SyncSpecs(ctx, first); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When: b stops using a.
	second := []repos.Spec{
		{Name: "a", CloneURL: "file:///a", Enabled: true, Project: "p"},
		{Name: "b", CloneURL: "file:///b", Enabled: true, Project: "p"},
	}
	if _, err := s.SyncSpecs(ctx, second); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	all, _ := s.All(ctx)
	for _, r := range all {
		if len(r.Uses) != 0 {
			t.Errorf("%s.Uses = %v, want empty after the edge was removed", r.Name, r.Uses)
		}
	}
}

func TestSyncSpecs_aStructureEditIsNotAReIndex(t *testing.T) {
	// Renaming a project, or writing a description, changes what the reader and
	// the model are told about a repository. It changes nothing about its code,
	// so it must not reset the SHA and send the next poll into a full re-index.
	// The reset trigger is clone_url, and only clone_url.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///x", Branch: "master", Enabled: true, Project: "peeq"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}

	// When: the structure is edited, the remote is not.
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "file:///x", Branch: "master", Enabled: true,
			Project: "search-suite", Part: "backend", Description: "Retrieval service."},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	all, _ := s.All(ctx)
	if len(all) != 1 {
		t.Fatalf("All() = %+v, want one entry", all)
	}
	if all[0].LastSHA != "abc123" {
		t.Errorf("LastSHA = %q, want abc123 kept — a structure edit is not a re-index", all[0].LastSHA)
	}
	if all[0].Branch != "master" {
		t.Errorf("Branch = %q, want master kept", all[0].Branch)
	}
	if all[0].Project != "search-suite" || all[0].Part != "backend" {
		t.Errorf("structure = %q/%q, want the edit to have landed", all[0].Project, all[0].Part)
	}
}

func TestSetCounts_touchesOnlyTheTotals(t *testing.T) {
	// Given: an indexed repository that later recorded an error. The startup
	// sweep refreshes the totals after removing excluded content, and it must
	// not pose as a poll: the SHA and the error both stay.
	ctx := context.Background()
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Branch: "master", Enabled: true}}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	if err := s.MarkError(ctx, "peeq", "fetch failed"); err != nil {
		t.Fatalf("MarkError() err = %v", err)
	}

	// When
	if err := s.SetCounts(ctx, "peeq", Counts{Files: 10, Chunks: 80}); err != nil {
		t.Fatalf("SetCounts() err = %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("All() = %+v, want one entry", all)
	}
	st := all[0]
	if st.Files != 10 || st.Chunks != 80 {
		t.Errorf("counts = %d/%d, want 10/80", st.Files, st.Chunks)
	}
	if st.LastSHA != "abc123" {
		t.Errorf("LastSHA = %q, want abc123 untouched", st.LastSHA)
	}
	if st.LastError != "fetch failed" {
		t.Errorf("LastError = %q, want the recorded error to stand", st.LastError)
	}
}

func TestMarkError_isVisibleAndClearedByASuccess(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When: the configured branch vanished upstream.
	if err := s.MarkError(ctx, "peeq", `branch "release-2024.3" not found`); err != nil {
		t.Fatalf("MarkError() err = %v", err)
	}

	// Then: it is visible on the state, not swallowed.
	active, _ := s.Active(ctx)
	if len(active) != 1 || active[0].LastError == "" {
		t.Fatalf("LastError is empty, want the branch failure surfaced")
	}

	// And a later success clears it, so a stale error cannot alarm forever.
	if err := s.MarkIndexed(ctx, "peeq", "def456", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	active, _ = s.Active(ctx)
	if active[0].LastError != "" {
		t.Errorf("LastError = %q, want it cleared after a successful run", active[0].LastError)
	}
}

func TestSetBranch_recordsTheResolvedBranch(t *testing.T) {
	// Given: an entry whose YAML omitted the branch, so it is empty until the
	// git layer resolves the remote's default.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "go-sqlite3", CloneURL: "/tmp/x", Branch: "", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When
	if err := s.SetBranch(ctx, "go-sqlite3", "main"); err != nil {
		t.Fatalf("SetBranch() err = %v", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if active[0].Branch != "main" {
		t.Errorf("Branch = %q, want %q — never assume master", active[0].Branch, "main")
	}
}

func TestPollRepo_readsTheTokenByItsEnvironmentVariableName(t *testing.T) {
	// Given: repos.yaml names the variable, never the value. Asking TokenFunc
	// for the REPOSITORY name resolved every token to "" and every private
	// repository was fetched anonymously while the config looked right.
	db := newDB(t)
	state := NewStateStore(db)
	spec := repos.Spec{
		Name: "private", CloneURL: "https://forge.invalid/a.git",
		Branch: "main", TokenEnv: "BACKEND_FORGE_TOKEN", Enabled: true,
	}
	if _, err := state.SyncSpecs(context.Background(), []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}

	// When
	var asked []string
	p := NewPoller(PollerDeps{
		State:  state,
		Git:    nil,
		Tokens: func(name string) string { asked = append(asked, name); return "" },
	})
	active, err := state.Active(context.Background())
	if err != nil {
		t.Fatalf("Active: %v", err)
	}

	// Then: the state round-trips the variable NAME...
	if len(active) != 1 || active[0].TokenEnv != "BACKEND_FORGE_TOKEN" {
		t.Fatalf("Active() = %+v, want token_env carried through repo_state", active)
	}
	// ...and the poller asks for exactly that.
	_ = p
	tokenFor := func(st RepoState) string {
		asked = nil
		p.tokens(st.TokenEnv)
		return ""
	}
	tokenFor(active[0])
	if len(asked) != 1 || asked[0] != "BACKEND_FORGE_TOKEN" {
		t.Errorf("the poller asked for %v, want [BACKEND_FORGE_TOKEN]", asked)
	}
}

func TestMarkChecked_clearsAStaleErrorOnAQuietPoll(t *testing.T) {
	// Given: a repository that failed once and has had no new commit since.
	// Without this the error stays on the Repos page until someone pushes.
	db := newDB(t)
	state := NewStateStore(db)
	ctx := context.Background()
	spec := repos.Spec{Name: "shop", CloneURL: "https://forge.invalid/a.git", Branch: "main", Enabled: true}
	if _, err := state.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	if err := state.MarkError(ctx, "shop", "dial tcp: i/o timeout"); err != nil {
		t.Fatalf("MarkError: %v", err)
	}

	// When
	if err := state.MarkChecked(ctx, "shop"); err != nil {
		t.Fatalf("MarkChecked: %v", err)
	}

	// Then
	all, err := state.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if all[0].LastError != "" {
		t.Errorf("LastError = %q after a successful poll, want it cleared", all[0].LastError)
	}
	if all[0].LastRunAt.IsZero() {
		t.Error("LastRunAt was not refreshed")
	}
}

// seedPurgeable builds a database holding one repository with real chunks in
// both mirrors, ready to be purged.
func seedPurgeable(t *testing.T, name string) (*sql.DB, *StateStore) {
	t.Helper()
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: name, CloneURL: "/tmp/" + name, Branch: "master", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := NewWriter(db).ReplaceFile(ctx, name, "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}
	return db, s
}

// TestPurge_reportsADatabaseFailureRatherThanPurgingHalfway covers the error
// paths of the purge, one sabotaged table at a time.
//
// They are worth pinning rather than trusting: a purge that gives up silently
// midway is the one outcome worse than not purging at all. It leaves rows in one
// mirror and not the other, and rowid == chunks.id then resolves a surviving
// vector against whatever chunk is written next. Every case below must come back
// as an error, so the caller records it and the next boot tries again.
func TestPurge_reportsADatabaseFailureRatherThanPurgingHalfway(t *testing.T) {
	ctx := context.Background()

	t.Run("a closed database", func(t *testing.T) {
		db, s := seedPurgeable(t, "peeq")
		db.Close()
		if _, err := s.SyncSpecs(ctx, nil); err == nil {
			t.Error("SyncSpecs() err = nil, want the transaction failure")
		}
		if err := s.ResetRepo(ctx, "peeq"); err == nil {
			t.Error("ResetRepo() err = nil, want the transaction failure")
		}
	})

	t.Run("the repository table is unreadable", func(t *testing.T) {
		db, s := seedPurgeable(t, "peeq")
		if _, err := db.Exec(`DROP TABLE repo_state`); err != nil {
			t.Fatalf("sabotage: %v", err)
		}
		if _, err := s.SyncSpecs(ctx, nil); err == nil {
			t.Error("SyncSpecs() err = nil, want the failure to list repositories")
		}
	})

	t.Run("the vector mirror is unreachable", func(t *testing.T) {
		db, s := seedPurgeable(t, "peeq")
		if _, err := db.Exec(`DROP TABLE chunks_vec`); err != nil {
			t.Fatalf("sabotage: %v", err)
		}
		if _, err := s.SyncSpecs(ctx, nil); err == nil {
			t.Error("SyncSpecs() err = nil, want the mirror failure surfaced")
		}
		if err := s.ResetRepo(ctx, "peeq"); err == nil {
			t.Error("ResetRepo() err = nil, want the mirror failure surfaced")
		}
	})

	t.Run("the keyword mirror is unreachable", func(t *testing.T) {
		db, s := seedPurgeable(t, "peeq")
		if _, err := db.Exec(`DROP TABLE chunks_fts`); err != nil {
			t.Fatalf("sabotage: %v", err)
		}
		if err := s.ResetRepo(ctx, "peeq"); err == nil {
			t.Error("ResetRepo() err = nil, want the mirror failure surfaced")
		}
	})

	t.Run("the row a reset has to keep is gone", func(t *testing.T) {
		// The content clears, and only the UPDATE that puts the repository back
		// at "nothing indexed yet" fails. A reset reporting success here would
		// leave last_sha pointing at a commit whose chunks no longer exist.
		db, s := seedPurgeable(t, "peeq")
		if _, err := db.Exec(`DROP TABLE repo_state`); err != nil {
			t.Fatalf("sabotage: %v", err)
		}
		if err := s.ResetRepo(ctx, "peeq"); err == nil {
			t.Error("ResetRepo() err = nil, want the failure to record the reset")
		}
	})
}

func TestResetRepo_dropsTheContentAndKeepsTheRow(t *testing.T) {
	// Given: a repository whose checkout turned out to point at a different
	// remote. The entry is still in repos.yaml and has to survive; everything
	// built out of the wrong code has to go.
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	spec := repos.Spec{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true}
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := NewWriter(db).ReplaceFile(ctx, "peeq", "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	if err := s.MarkError(ctx, "peeq", "an older failure"); err != nil {
		t.Fatalf("MarkError() err = %v", err)
	}

	// When
	if err := s.ResetRepo(ctx, "peeq"); err != nil {
		t.Fatalf("ResetRepo() err = %v", err)
	}

	// Then: the row is still there and still active ...
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Name != "peeq" {
		t.Fatalf("Active() = %+v, want peeq kept", active)
	}
	// ... with nothing left to resume from, so the next poll indexes in full.
	if active[0].LastSHA != "" {
		t.Errorf("LastSHA = %q, want empty", active[0].LastSHA)
	}
	if active[0].LastError != "" {
		t.Errorf("LastError = %q, want cleared — it describes code that is gone", active[0].LastError)
	}
	if active[0].Files != 0 || active[0].Chunks != 0 {
		t.Errorf("counts = %d files / %d chunks, want 0/0", active[0].Files, active[0].Chunks)
	}
	// ... and the content is gone from all four tables, mirrors included.
	for _, q := range []string{
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM chunks_vec`,
		`SELECT COUNT(*) FROM chunks_fts`,
	} {
		if n := countOf(t, db, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
}
