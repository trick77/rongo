package indexer

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/repos"
)

func TestCompareVersionNames(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2027_V9005", "2026_V9004", 1},
		{"2023_V9001", "2024_V9002", -1},
		{"V10", "V9", 1},
		{"V9", "V10", -1},
		{"release-2", "release-2", 0},
		{"v1.10.0", "v1.9.3", 1},
		{"V01", "V1", -1}, // numerically equal: bytes decide, so the order is total
		{"a", "b", -1},
		{"abc", "ab", 1},
		{"99999999999999999999999", "100000000000000000000000", -1}, // no overflow
	}
	for _, tc := range cases {
		if got := compareVersionNames(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersionNames(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSelector_latestKeepsOnlyTheHighestSibling(t *testing.T) {
	// Given: versions side by side under two parents, a sibling the glob does
	// not match, and a file directly in the parent.
	tree := []string{
		"assets/seed/2023_V9001/a.txt",
		"assets/seed/2027_V9005/a.txt",
		"assets/seed/2026_V9004/deep/b.txt",
		"assets/seed/inyear_V1234/a.txt",
		"assets/seed/README.md",
		"other/seed/2023_V1/a.txt",
		"v/V9/x.txt",
		"v/V10/x.txt",
	}
	sel, _ := NewSelector(DefaultSelectOptions()).withRules(repos.Index{
		Latest: []string{"*/seed/[0-9][0-9][0-9][0-9]_*", "v/V*"},
	}, tree)

	want := map[string]bool{
		"assets/seed/2023_V9001/a.txt":      true,
		"assets/seed/2026_V9004/deep/b.txt": true,
		"assets/seed/2027_V9005/a.txt":      false,
		"assets/seed/inyear_V1234/a.txt":    false, // not a version: the glob does not match it
		"assets/seed/README.md":             false,
		"other/seed/2023_V1/a.txt":          false, // newest of its own parent
		"v/V9/x.txt":                        true,
		"v/V10/x.txt":                       false,
	}
	for p, excluded := range want {
		if _, got := sel.Excluded(p); got != excluded {
			t.Errorf("Excluded(%q) = %v, want %v", p, got, excluded)
		}
	}
}

func TestSelector_aRetiredVersionSaysWhatRetiredIt(t *testing.T) {
	sel, _ := NewSelector(SelectOptions{Exclude: []string{"docs/plans/**"}}).withRules(repos.Index{
		Latest: []string{seedGlob},
	}, []string{"assets/seed/2023_V9001/a.md", "assets/seed/2024_V9002/a.md"})

	_, detail, _ := sel.SelectPath("assets/seed/2023_V9001/a.md")
	if want := "superseded by assets/seed/2024_V9002 (index.latest " + seedGlob + ")"; detail != want {
		t.Errorf("detail = %q, want %q", detail, want)
	}
	if _, detail, _ := sel.SelectPath("docs/plans/a.md"); detail != "matches exclusion pattern docs/plans/**" {
		t.Errorf("detail = %q, want the pattern named", detail)
	}
}

func TestSelector_repoExcludeAddsToTheGlobalList(t *testing.T) {
	global := NewSelector(SelectOptions{Exclude: []string{"docs/plans/**"}})
	sel, _ := global.withRules(repos.Index{Exclude: []string{"docs/drafts/**"}}, nil)

	for _, p := range []string{"docs/plans/a.md", "docs/drafts/b.md"} {
		if _, ok := sel.Excluded(p); !ok {
			t.Errorf("Excluded(%q) = false, want true", p)
		}
	}
	// The global selector is untouched: another repository shares it.
	if _, ok := global.Excluded("docs/drafts/b.md"); ok {
		t.Error("the repository's pattern leaked into the global selector")
	}
}

// --- through the pipeline ------------------------------------------------

var versionedTree = map[string]string{
	"src/app.go":                     "package app\n\nfunc Run() {}\n",
	"docs/plans/phase-2.md":          "# plan\n\nNot how it works.\n",
	"docs/drafts/idea.md":            "# draft\n\nAn idea nobody built.\n",
	"assets/seed/2023_V9001/rate.md": "# rates 2023\n\nbase 1.0\n",
	"assets/seed/2024_V9002/rate.md": "# rates 2024\n\nbase 1.1\n",
	"assets/seed/inyear_V1234/x.md":  "# in-year\n\nadjustment\n",
}

const seedGlob = "assets/seed/[0-9][0-9][0-9][0-9]_*"

func reasonOf(t testing.TB, db *sql.DB, path string) string {
	t.Helper()
	var reason string
	if err := db.QueryRow(`SELECT skip_reason FROM files WHERE path = ?`, path).Scan(&reason); err != nil {
		t.Fatalf("no file row for %s: %v", path, err)
	}
	return reason
}

func TestIndexRepo_repoRulesApplyToThatRepositoryOnly(t *testing.T) {
	// Given: a global pattern, and index rules for this repository and for
	// another one.
	h := newHarnessFiles(t, versionedTree, nil)
	h.ix.selector = NewSelector(SelectOptions{Exclude: []string{"docs/plans/**"}})
	h.ix.rules = map[string]repos.Index{
		"shop":    {Exclude: []string{"docs/drafts/**"}, Latest: []string{seedGlob}},
		"billing": {Exclude: []string{"src/**"}},
	}

	// When
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}

	// Then
	want := map[string]string{
		"src/app.go":                     "", // billing's rule is billing's
		"docs/plans/phase-2.md":          string(SkipExcluded),
		"docs/drafts/idea.md":            string(SkipExcluded),
		"assets/seed/2023_V9001/rate.md": string(SkipExcluded),
		"assets/seed/2024_V9002/rate.md": "",
		"assets/seed/inyear_V1234/x.md":  "",
	}
	for p, reason := range want {
		if got := reasonOf(t, h.db, p); got != reason {
			t.Errorf("%s: skip_reason = %q, want %q", p, got, reason)
		}
	}
}

func TestIndexRepo_warnsOfALatestGlobThatMatchesNoDirectory(t *testing.T) {
	// Given: a glob that only file names match, and one that matches.
	h := newHarnessFiles(t, versionedTree, nil)
	var logged strings.Builder
	h.ix.log = slog.New(slog.NewTextHandler(&logged, nil))
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{"assets/seed/*/*.md", seedGlob}}}

	// When
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}

	// Then: the idle one is named, the working one is not.
	out := logged.String()
	if !strings.Contains(out, "matches no directory") || !strings.Contains(out, "assets/seed/*/*.md") {
		t.Errorf("log = %q, want a warning naming the idle glob", out)
	}
	if strings.Contains(out, "glob="+seedGlob) || strings.Contains(out, `glob="`+seedGlob) {
		t.Errorf("log = %q, the working glob was warned about too", out)
	}
}

func TestIndexRepo_aNewVersionRetiresThePreviousOne(t *testing.T) {
	// Given: an index holding 2024 as the newest version.
	h := newHarnessFiles(t, versionedTree, nil)
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{seedGlob}}}
	st := h.stateOf(t)
	first := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, first, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}

	// When: 2025 lands, and the incremental run is handed only its file.
	write(t, h.src, "assets/seed/2025_V9003/rate.md", "# rates 2025\n\nbase 1.2\n")
	gittest.Run(t, h.src, "add", "-A")
	gittest.Run(t, h.src, "commit", "-qm", "2025")
	st.LastSHA = first
	second := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, second, []string{"assets/seed/2025_V9003/rate.md"}); err != nil {
		t.Fatalf("incremental IndexRepo() err = %v", err)
	}

	// Then: 2024 is retired with no edit to the rules.
	if got := reasonOf(t, h.db, "assets/seed/2024_V9002/rate.md"); got != string(SkipExcluded) {
		t.Errorf("2024 skip_reason = %q, want excluded", got)
	}
	if n := chunkCountOf(t, h.db, "assets/seed/2024_V9002/rate.md"); n != 0 {
		t.Errorf("2024 still has %d chunks", n)
	}
	if got := reasonOf(t, h.db, "assets/seed/2025_V9003/rate.md"); got != "" {
		t.Errorf("2025 skip_reason = %q, want indexed", got)
	}

	// When: 2025 is withdrawn again.
	gittest.Run(t, h.src, "rm", "-rq", "assets/seed/2025_V9003")
	gittest.Run(t, h.src, "commit", "-qm", "withdraw 2025")
	st.LastSHA = second
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), []string{"assets/seed/2025_V9003/rate.md"}); err != nil {
		t.Fatalf("second incremental IndexRepo() err = %v", err)
	}

	// Then: 2024 is the newest again, and indexed again.
	if got := reasonOf(t, h.db, "assets/seed/2024_V9002/rate.md"); got != "" {
		t.Errorf("2024 skip_reason = %q after 2025 left, want indexed", got)
	}
	if n := chunkCountOf(t, h.db, "assets/seed/2024_V9002/rate.md"); n == 0 {
		t.Error("2024 has no chunks after 2025 left")
	}
}

// listingGit counts the tree listings and the blob reads a run asks for. It
// hides the batch reader, so every read goes through ReadFile.
type listingGit struct {
	GitClient
	lists int
	reads map[string]int
}

func (g *listingGit) ListEntries(ctx context.Context, spec repos.Spec, sha string) ([]gitrepo.Change, error) {
	g.lists++
	return g.GitClient.ListEntries(ctx, spec, sha)
}

func (g *listingGit) ReadFile(ctx context.Context, spec repos.Spec, sha, p string) ([]byte, error) {
	g.reads[p]++
	return g.GitClient.ReadFile(ctx, spec, sha, p)
}

// commitAndIndex commits what the working tree of the fixture holds and runs
// the incremental index over changed, from st.LastSHA.
func commitAndIndex(t *testing.T, h *harness, st *RepoState, msg string, changed ...string) {
	t.Helper()
	gittest.Run(t, h.src, "add", "-A")
	gittest.Run(t, h.src, "commit", "-qm", msg)
	sha := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), *st, sha, changed); err != nil {
		t.Fatalf("incremental IndexRepo(%s) err = %v", msg, err)
	}
	st.LastSHA = sha
}

func TestIndexRepo_aCommitOutsideTheVersionsCostsNoListing(t *testing.T) {
	// Given: an indexed repository with a latest rule, and an exclude added
	// afterwards that only a reconcile pass or the boot sweep would apply.
	h := newHarnessFiles(t, versionedTree, nil)
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{seedGlob}}}
	st := h.stateOf(t)
	st.LastSHA = h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, st.LastSHA, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	h.ix.rules = map[string]repos.Index{"shop": {Exclude: []string{"docs/drafts/**"}, Latest: []string{seedGlob}}}
	git := &listingGit{GitClient: h.gitc, reads: map[string]int{}}
	h.ix.git = git

	// When: a commit touches code only.
	write(t, h.src, "src/app.go", "package app\n\nfunc Run() { _ = 1 }\n")
	commitAndIndex(t, h, &st, "code", "src/app.go")

	// Then: no tree listing, no pass over the rows.
	if git.lists != 0 {
		t.Errorf("ListEntries ran %d times for a commit outside every version directory", git.lists)
	}
	if got := reasonOf(t, h.db, "docs/drafts/idea.md"); got != "" {
		t.Errorf("draft skip_reason = %q: a pass over every row ran", got)
	}

	// When: a commit edits a file inside the retired 2023 version.
	write(t, h.src, "assets/seed/2023_V9001/rate.md", "# rates 2023\n\nbase 1.05\n")
	commitAndIndex(t, h, &st, "fix 2023", "assets/seed/2023_V9001/rate.md")

	// Then: the edit is recorded excluded, decided from the diff and the
	// rows, with no listing and no pass over the rows: no version came or
	// went.
	if got := reasonOf(t, h.db, "assets/seed/2023_V9001/rate.md"); got != string(SkipExcluded) {
		t.Errorf("edited 2023 skip_reason = %q, want excluded", got)
	}
	if git.lists != 0 {
		t.Errorf("ListEntries ran %d times for an edit inside an existing version, want 0", git.lists)
	}
	if got := reasonOf(t, h.db, "docs/drafts/idea.md"); got != "" {
		t.Errorf("draft skip_reason = %q: a pass over every row ran", got)
	}

	// When: a new version lands.
	write(t, h.src, "assets/seed/2025_V9003/rate.md", "# rates 2025\n\nbase 1.2\n")
	commitAndIndex(t, h, &st, "2025", "assets/seed/2025_V9003/rate.md")

	// Then: one listing, and the pass over the rows ran.
	if git.lists != 1 {
		t.Errorf("ListEntries ran %d times once a version came, want 1", git.lists)
	}
	if got := reasonOf(t, h.db, "docs/drafts/idea.md"); got != string(SkipExcluded) {
		t.Errorf("draft skip_reason = %q, want the pass over the rows to have run", got)
	}
}

func TestIndexRepo_anEditInsideATopLevelVersionCostsNoListing(t *testing.T) {
	// Given: versions at the root of the repository.
	h := newHarnessFiles(t, map[string]string{
		"2023_A/x.md": "# a 2023\n\none\n",
		"2024_A/x.md": "# a 2024\n\ntwo\n",
		"src/app.go":  "package app\n",
	}, nil)
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{"[0-9][0-9][0-9][0-9]_*"}}}
	st := h.stateOf(t)
	st.LastSHA = h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, st.LastSHA, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	git := &listingGit{GitClient: h.gitc, reads: map[string]int{}}
	h.ix.git = git

	// When: the retired version is edited.
	write(t, h.src, "2023_A/x.md", "# a 2023\n\none, fixed\n")
	commitAndIndex(t, h, &st, "fix 2023", "2023_A/x.md")

	// Then
	if got := reasonOf(t, h.db, "2023_A/x.md"); got != string(SkipExcluded) {
		t.Errorf("2023 skip_reason = %q, want excluded", got)
	}
	if git.lists != 0 {
		t.Errorf("ListEntries ran %d times, want 0", git.lists)
	}
}

func TestVersionsMoved(t *testing.T) {
	dirs := map[string]bool{"seed/2025_X": true}
	before := []string{"seed/2024_X/a.md"}
	if !versionsMoved(dirs, before, applyDiff(before, []target{{path: "seed/2025_X/a.md"}})) {
		t.Error("a version that came did not move the versions")
	}
	both := []string{"seed/2024_X/a.md", "seed/2025_X/a.md"}
	if versionsMoved(dirs, both, applyDiff(both, []target{{path: "seed/2025_X/a.md"}})) {
		t.Error("an edit inside an existing version moved the versions")
	}
	if !versionsMoved(dirs, both, applyDiff(both, []target{{path: "seed/2025_X/a.md", deleted: true}})) {
		t.Error("a version that went did not move the versions")
	}
	if !versionsMoved(dirs, both, applyDiff(both, []target{{path: "seed/2025_X/a.md", mode: "120000"}})) {
		t.Error("a version whose one file became a symlink did not move the versions")
	}
}

func TestIndexRepo_aVersionBroughtBackIsSizedBeforeItIsRead(t *testing.T) {
	// Given: 2024 retired by 2025, holding a file above the file ceiling.
	files := map[string]string{
		"assets/seed/2024_V9002/table.md": "# table 2024\n\n| a | b |\n| - | - |\n| 1 | 2 |\n| 3 | 4 |\n",
		"assets/seed/2025_V9003/rate.md":  "# rates 2025\n\nbase 1.2\n",
	}
	h := newHarnessFiles(t, files, nil)
	h.ix.selector = NewSelector(SelectOptions{MaxBytes: 32})
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{seedGlob}}}
	st := h.stateOf(t)
	st.LastSHA = h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, st.LastSHA, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	if got := reasonOf(t, h.db, "assets/seed/2024_V9002/table.md"); got != string(SkipExcluded) {
		t.Fatalf("2024 table skip_reason = %q, want excluded before 2025 leaves", got)
	}
	git := &listingGit{GitClient: h.gitc, reads: map[string]int{}}
	h.ix.git = git

	// When: 2025 is withdrawn.
	gittest.Run(t, h.src, "rm", "-rq", "assets/seed/2025_V9003")
	commitAndIndex(t, h, &st, "withdraw 2025", "assets/seed/2025_V9003/rate.md")

	// Then: the table is judged on its listed size, never read.
	if got := reasonOf(t, h.db, "assets/seed/2024_V9002/table.md"); got != string(SkipTooLarge) {
		t.Errorf("2024 table skip_reason = %q, want too_large", got)
	}
	if n := git.reads["assets/seed/2024_V9002/table.md"]; n != 0 {
		t.Errorf("the table was read %d times; its listed size decides it unread", n)
	}
}

func TestSweep_appliesTheRepoRules(t *testing.T) {
	// Given: an index built before the repository had rules.
	h := newHarnessFiles(t, versionedTree, nil)
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	h.ix.rules = map[string]repos.Index{"shop": {Exclude: []string{"docs/drafts/**"}, Latest: []string{seedGlob}}}

	// When
	swept := h.ix.SweepAll(context.Background(), h.state)

	// Then
	if len(swept) != 1 || swept[0] != (Swept{Repo: "shop", Retired: 2}) {
		t.Errorf("SweepAll() = %+v, want 2 retired (the draft and 2023)", swept)
	}
	for _, p := range []string{"docs/drafts/idea.md", "assets/seed/2023_V9001/rate.md"} {
		if got := reasonOf(t, h.db, p); got != string(SkipExcluded) {
			t.Errorf("%s: skip_reason = %q, want excluded", p, got)
		}
	}
}

func TestSweepAll_requestsAFullRunWhenARuleNoLongerExcludes(t *testing.T) {
	// Given: an index built under rules that excluded the draft and 2023.
	h := newHarnessFiles(t, versionedTree, nil)
	h.ix.rules = map[string]repos.Index{"shop": {Exclude: []string{"docs/drafts/**"}, Latest: []string{seedGlob}}}
	sha := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), sha, nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}

	// When: the start-up pass runs with the rules unchanged.
	if swept := h.ix.SweepAll(context.Background(), h.state); len(swept) != 0 {
		t.Errorf("SweepAll() = %+v with unchanged rules, want nothing", swept)
	}

	// Then: nothing to re-include, nothing requested.
	if st := h.stateOf(t); st.ReindexRequested != 0 {
		t.Fatalf("ReindexRequested = %d with unchanged rules, want 0", st.ReindexRequested)
	}

	// When: the exclude is dropped and the next start sweeps.
	h.ix.rules = map[string]repos.Index{"shop": {Latest: []string{seedGlob}}}
	swept := h.ix.SweepAll(context.Background(), h.state)

	// Then: a full run is requested, said so, and that run indexes the draft.
	if len(swept) != 1 || swept[0] != (Swept{Repo: "shop", Reincluded: 1}) {
		t.Errorf("SweepAll() = %+v, want shop with one file re-included", swept)
	}
	st := h.stateOf(t)
	if st.ReindexRequested == 0 {
		t.Fatal("ReindexRequested = 0, want a full run: the draft is excluded in the index and included by the rules")
	}
	if _, err := h.ix.IndexRepo(context.Background(), st, sha, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	if got := reasonOf(t, h.db, "docs/drafts/idea.md"); got != "" {
		t.Errorf("draft skip_reason = %q after the full run, want indexed", got)
	}
	if got := reasonOf(t, h.db, "assets/seed/2023_V9001/rate.md"); got != string(SkipExcluded) {
		t.Errorf("2023 skip_reason = %q, want still excluded", got)
	}
}

func TestSweepAll_retiresWhatTheRulesNowExclude(t *testing.T) {
	// Given: an index built with no rules.
	h := newHarnessFiles(t, versionedTree, nil)
	sha := h.head(t)
	counts, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), sha, nil)
	if err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	if err := h.state.MarkIndexed(context.Background(), "shop", sha, counts); err != nil {
		t.Fatal(err)
	}
	before := h.stateOf(t)
	h.ix.rules = map[string]repos.Index{"shop": {Exclude: []string{"docs/drafts/**"}}}

	// When
	swept := h.ix.SweepAll(context.Background(), h.state)

	// Then: retired, reported, and the page's totals follow.
	if len(swept) != 1 || swept[0] != (Swept{Repo: "shop", Retired: 1}) {
		t.Errorf("SweepAll() = %+v, want shop with one file retired", swept)
	}
	if got := reasonOf(t, h.db, "docs/drafts/idea.md"); got != string(SkipExcluded) {
		t.Errorf("draft skip_reason = %q, want excluded", got)
	}
	if after := h.stateOf(t); after.Chunks >= before.Chunks {
		t.Errorf("Chunks = %d after the sweep, want fewer than %d", after.Chunks, before.Chunks)
	}
}

func TestRulesOf(t *testing.T) {
	got := RulesOf([]repos.Spec{
		{Name: "a", Index: repos.Index{Exclude: []string{"x/**"}}},
		{Name: "b"},
	})
	if len(got) != 1 || len(got["a"].Exclude) != 1 {
		t.Errorf("RulesOf() = %+v, want only a's rules", got)
	}
}

func TestSelectorOf_isTheConfiguredOne(t *testing.T) {
	sel, err := SelectorOf(10, 20, 30, []string{"docs/plans/**"})
	if err != nil {
		t.Fatalf("SelectorOf() err = %v", err)
	}
	if sel.opts.MaxBytes != 10 || sel.opts.MaxDataBytes != 20 || sel.opts.MaxSchemaBytes != 30 {
		t.Errorf("opts = %+v, want the configured ceilings", sel.opts)
	}
	if _, ok := sel.Excluded("docs/plans/a.md"); !ok {
		t.Error("the configured exclusion does not apply")
	}
	if _, err := SelectorOf(0, 0, 0, []string{"docs/"}); err == nil || !strings.Contains(err.Error(), "BACKEND_INDEX_EXCLUDE") {
		t.Errorf("SelectorOf(malformed) err = %v, want a refusal naming BACKEND_INDEX_EXCLUDE", err)
	}
}

func chunkCountOf(t testing.TB, db *sql.DB, path string) int {
	t.Helper()
	return countOf(t, db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = ?`, path)
}
