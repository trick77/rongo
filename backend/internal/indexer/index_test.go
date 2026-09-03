package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

// --- fixture repository -------------------------------------------------

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const cartJava = `package shop.cart;

/** Sends the teaser mail for an abandoned cart. */
public class AbandonedCartJob {

    /** Runs one pass over the abandoned carts. */
    public void run() {
        sender.send();
    }
}
`

// fixtureCorpus builds a local repository holding one indexable file, one
// vendored file and one plain text file, and returns its path.
func fixtureCorpus(t *testing.T) string {
	t.Helper()
	return fixtureCorpusFiles(t, map[string]string{
		"src/shop/cart/AbandonedCartJob.java": cartJava,
		"node_modules/left-pad/index.js":      "module.exports = function(){}\n",
		"README.md":                           "# shop backend\n\nThe cart is abandoned after 24 hours.\n",
	})
}

// fixtureCorpusFiles builds a local repository from an arbitrary path->body
// map and returns its path.
func fixtureCorpusFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for path, body := range files {
		write(t, dir, path, body)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "initial")
	return dir
}

// --- fakes --------------------------------------------------------------

// countingEmbedder returns deterministic vectors and records how many texts it
// was asked to embed, which is how the cache-hit assertions are made.
type countingEmbedder struct {
	dim   int
	texts int
	calls int
}

func (e *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.texts += len(texts)
	out := make([][]float32, len(texts))
	for i, tx := range texts {
		v := make([]float32, e.dim)
		var sum float32
		for _, r := range tx {
			sum += float32(r % 17)
		}
		for j := range v {
			v[j] = sum / float32(j+1)
		}
		out[i] = v
	}
	return out, nil
}

// failingSymbols is a ctags that breaks on one path, so the line-window
// fallback can be driven without a broken binary.
type failingSymbols struct {
	inner  SymbolExtractor
	failOn string
}

func (f failingSymbols) Extract(ctx context.Context, path string, body []byte) ([]symbols.Symbol, error) {
	if path == f.failOn {
		return nil, fmt.Errorf("ctags: unparseable output")
	}
	return f.inner.Extract(ctx, path, body)
}

// --- harness ------------------------------------------------------------

type harness struct {
	db       *sql.DB
	ix       *Indexer
	state    *StateStore
	embedder *countingEmbedder
	spec     repos.Spec
	src      string
	gitc     *gitrepo.Client
}

func newHarness(t *testing.T, symbolExtractor func(SymbolExtractor) SymbolExtractor) *harness {
	t.Helper()
	return newHarnessFiles(t, nil, symbolExtractor)
}

// newHarnessFiles is newHarness with the fixture's file set overridable, for
// tests that need a tree shape fixtureCorpus does not provide (e.g. a
// go.mod). A nil files map falls back to fixtureCorpus's default tree.
func newHarnessFiles(t *testing.T, files map[string]string, symbolExtractor func(SymbolExtractor) SymbolExtractor) *harness {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	ctagsBin, err := exec.LookPath("ctags")
	if err != nil {
		t.Skip("ctags not available")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, writeDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var src string
	if files != nil {
		src = fixtureCorpusFiles(t, files)
	} else {
		src = fixtureCorpus(t)
	}
	spec := repos.Spec{Name: "shop", CloneURL: src, Branch: "main", Enabled: true}
	state := NewStateStore(db)
	if err := state.SyncSpecs(context.Background(), []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	gitc := gitrepo.New(gitBin, t.TempDir())
	if err := gitc.EnsureCloned(context.Background(), spec, ""); err != nil {
		t.Fatalf("EnsureCloned: %v", err)
	}

	var sym SymbolExtractor = symbols.NewExtractor(ctagsBin)
	if symbolExtractor != nil {
		sym = symbolExtractor(sym)
	}
	emb := &countingEmbedder{dim: writeDim}
	ix := New(Deps{
		DB:       db,
		Git:      gitc,
		Symbols:  sym,
		Embedder: emb,
		Cache:    newTestCache(db),
		Writer:   NewWriter(db),
		Selector: NewSelector(DefaultSelectOptions()),
	})
	return &harness{db: db, ix: ix, state: state, embedder: emb, spec: spec, src: src, gitc: gitc}
}

// newTestCache is the real cache under a fixed model name, so the cache-hit
// assertions exercise the code that actually runs in production.
func newTestCache(db *sql.DB) VectorCache {
	return embed.NewCache(db, "test-model", writeDim)
}

func (h *harness) stateOf(t *testing.T) RepoState {
	t.Helper()
	all, err := h.state.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, st := range all {
		if st.Name == "shop" {
			return st
		}
	}
	t.Fatal("no state for shop")
	return RepoState{}
}

func (h *harness) head(t *testing.T) string {
	t.Helper()
	if err := h.gitc.Fetch(context.Background(), h.spec, ""); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sha, err := h.gitc.HeadSHA(context.Background(), h.spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	return sha
}

// --- tests --------------------------------------------------------------

func TestIndexRepo_fullIndexRecordsIndexedAndSkippedFilesAlike(t *testing.T) {
	// Given
	h := newHarness(t, nil)
	st := h.stateOf(t)
	sha := h.head(t)

	// When
	counts, err := h.ix.IndexRepo(context.Background(), st, sha, nil)

	// Then
	if err != nil {
		t.Fatalf("IndexRepo() err = %v, want nil", err)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE repo = 'shop'`); n != 3 {
		t.Errorf("files = %d, want 3 — a skipped file is recorded, not omitted", n)
	}
	// The vendored file exists in the index as a skipped row: the answer layer
	// must be able to say "that file exists but was not indexed" rather than
	// pretending it is absent.
	var reason string
	if err := h.db.QueryRow(`SELECT skip_reason FROM files WHERE path = 'node_modules/left-pad/index.js'`).Scan(&reason); err != nil {
		t.Fatalf("the vendored file was not recorded: %v", err)
	}
	if reason != string(SkipVendored) {
		t.Errorf("skip_reason = %q, want %q", reason, SkipVendored)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'node_modules/left-pad/index.js'`); n != 0 {
		t.Errorf("the vendored file produced %d chunks, want 0", n)
	}
	javaChunks := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'src/shop/cart/AbandonedCartJob.java'`)
	if javaChunks == 0 {
		t.Error("the java file produced no chunks")
	}
	if counts.Files != 3 || counts.Chunks == 0 {
		t.Errorf("Counts = %+v, want 3 files and some chunks", counts)
	}
	// Every chunk has its vector and its keyword row.
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c
		JOIN chunks_vec v ON v.rowid = c.id
		JOIN chunks_fts f ON f.rowid = c.id`); n != counts.Chunks {
		t.Errorf("%d of %d chunks are bridged to both lanes", n, counts.Chunks)
	}
}

const stalePlan = "# Phase 2\n\nThe cart is abandoned after 48 hours, says this stale plan.\n"

func TestIndexRepo_excludedPathIsRecordedNotEmbedded(t *testing.T) {
	// Given: a repository carrying a design document under docs/plans.
	h := newHarnessFiles(t, map[string]string{
		"src/shop/cart/AbandonedCartJob.java": cartJava,
		"docs/plans/phase-2.md":               stalePlan,
	}, nil)
	h.ix.selector = NewSelector(SelectOptions{Exclude: []string{"docs/plans/**"}})
	st := h.stateOf(t)
	sha := h.head(t)

	// When
	counts, err := h.ix.IndexRepo(context.Background(), st, sha, nil)

	// Then
	if err != nil {
		t.Fatalf("IndexRepo() err = %v, want nil", err)
	}
	var reason string
	if err := h.db.QueryRow(`SELECT skip_reason FROM files WHERE path = 'docs/plans/phase-2.md'`).Scan(&reason); err != nil {
		t.Fatalf("the excluded file was not recorded: %v", err)
	}
	if reason != string(SkipExcluded) {
		t.Errorf("skip_reason = %q, want %q", reason, SkipExcluded)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'docs/plans/phase-2.md'`); n != 0 {
		t.Errorf("the excluded file produced %d chunks, want 0", n)
	}
	if counts.Files != 2 {
		t.Errorf("Counts.Files = %d, want 2 — the excluded file is recorded, not omitted", counts.Files)
	}
}

func TestSweepExcluded_removesWhatAnEarlierRunIndexed(t *testing.T) {
	// Given: a repository indexed BEFORE the exclusion existed, so the plan is
	// embedded and searchable. Incremental runs only visit changed paths and
	// the poller does nothing while HEAD is unchanged, so without a sweep the
	// plan would stay in the index for as long as nobody edits it.
	h := newHarnessFiles(t, map[string]string{
		"src/shop/cart/AbandonedCartJob.java": cartJava,
		"docs/plans/phase-2.md":               stalePlan,
	}, nil)
	st := h.stateOf(t)
	sha := h.head(t)
	before, err := h.ix.IndexRepo(context.Background(), st, sha, nil)
	if err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	planChunks := func() int {
		return countOf(t, h.db, `
			SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
			WHERE f.path = 'docs/plans/phase-2.md'`)
	}
	if planChunks() == 0 {
		t.Fatal("the plan was not indexed by the first run; the sweep would have nothing to prove")
	}
	h.ix.selector = NewSelector(SelectOptions{Exclude: []string{"docs/plans/**"}})

	// When
	changed, counts, err := h.ix.SweepExcluded(context.Background(), "shop")

	// Then
	if err != nil {
		t.Fatalf("SweepExcluded() err = %v, want nil", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	if n := planChunks(); n != 0 {
		t.Errorf("the plan still has %d chunks after the sweep, want 0", n)
	}
	var reason string
	if err := h.db.QueryRow(`SELECT skip_reason FROM files WHERE path = 'docs/plans/phase-2.md'`).Scan(&reason); err != nil {
		t.Fatalf("the swept file row is gone: %v", err)
	}
	if reason != string(SkipExcluded) {
		t.Errorf("skip_reason = %q, want %q", reason, SkipExcluded)
	}
	// The mirrors go with the chunks: an orphaned vector keeps answering.
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM chunks_vec`); n != counts.Chunks {
		t.Errorf("chunks_vec holds %d rows, want %d", n, counts.Chunks)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM chunks_fts`); n != counts.Chunks {
		t.Errorf("chunks_fts holds %d rows, want %d", n, counts.Chunks)
	}
	if counts.Files != before.Files || counts.Chunks >= before.Chunks {
		t.Errorf("Counts = %+v after %+v, want the same files and fewer chunks", counts, before)
	}

	// And a second sweep finds nothing left to do.
	if changed, _, err := h.ix.SweepExcluded(context.Background(), "shop"); err != nil || changed != 0 {
		t.Errorf("second sweep: changed = %d, err = %v, want 0 and nil", changed, err)
	}
}

func TestIndexRepo_secondRunEmbedsNothing(t *testing.T) {
	// Given: an indexed repository whose content has not changed. Re-embedding
	// it would make every dev re-index cost minutes and money for nothing.
	h := newHarness(t, nil)
	st := h.stateOf(t)
	sha := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, sha, nil); err != nil {
		t.Fatalf("first IndexRepo() err = %v", err)
	}
	first := h.embedder.texts
	if first == 0 {
		t.Fatal("the first run embedded nothing at all")
	}

	// When
	if _, err := h.ix.IndexRepo(context.Background(), st, sha, nil); err != nil {
		t.Fatalf("second IndexRepo() err = %v", err)
	}

	// Then
	if h.embedder.texts != first {
		t.Errorf("the second run embedded %d more texts, want 0 — the content-hash cache did not hit",
			h.embedder.texts-first)
	}
}

func TestIndexRepo_incrementalTouchesOnlyTheNamedPaths(t *testing.T) {
	// Given: a fully indexed repository.
	h := newHarness(t, nil)
	st := h.stateOf(t)
	firstSHA := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, firstSHA, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	javaBefore := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'src/shop/cart/AbandonedCartJob.java'`)

	// When: one file changes and another is deleted upstream.
	write(t, h.src, "README.md", "# shop backend\n\nNew text about the cart.\n")
	git(t, h.src, "rm", "-q", "node_modules/left-pad/index.js")
	git(t, h.src, "add", "-A")
	git(t, h.src, "commit", "-qm", "change and delete")
	st.LastSHA = firstSHA
	nextSHA := h.head(t)
	counts, err := h.ix.IndexRepo(context.Background(), st, nextSHA,
		[]string{"README.md", "node_modules/left-pad/index.js"})

	// Then
	if err != nil {
		t.Fatalf("incremental IndexRepo() err = %v", err)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE path = 'node_modules/left-pad/index.js'`); n != 0 {
		t.Error("the deleted file is still in the index; its rows would keep answering queries")
	}
	javaAfter := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'src/shop/cart/AbandonedCartJob.java'`)
	if javaAfter != javaBefore {
		t.Errorf("the untouched java file went from %d chunks to %d — an incremental run re-indexed it",
			javaBefore, javaAfter)
	}
	// Counts are the repository's TOTALS, not this run's delta: they are written
	// straight onto repo_state, where a delta would read as the whole corpus.
	if counts.Files != 2 {
		t.Errorf("Counts.Files = %d, want the repository total of 2", counts.Files)
	}
}

func TestIndexRepo_aFileCtagsCannotParseStillGetsChunks(t *testing.T) {
	// Given: ctags fails on one file. That must cost line-window chunking for
	// that file, never the repository's index.
	h := newHarness(t, func(inner SymbolExtractor) SymbolExtractor {
		return failingSymbols{inner: inner, failOn: "src/shop/cart/AbandonedCartJob.java"}
	})
	st := h.stateOf(t)

	// When
	_, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil)

	// Then
	if err != nil {
		t.Fatalf("IndexRepo() err = %v, want the failure confined to one file", err)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'src/shop/cart/AbandonedCartJob.java'`); n == 0 {
		t.Error("the file ctags failed on has no chunks; the line-window fallback did not run")
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE f.path = 'src/shop/cart/AbandonedCartJob.java'`); n != 0 {
		t.Errorf("got %d symbols for a file ctags could not parse, want 0", n)
	}
}

func TestIndexRepo_anEmbeddingFailureFailsTheRun(t *testing.T) {
	// Given: an endpoint that refuses. Unlike a ctags failure this is NOT
	// per-file — a partially embedded repository would be recorded as indexed
	// and its missing half would silently never be searched.
	h := newHarness(t, nil)
	st := h.stateOf(t)

	// When: the embedder fails for every text.
	h.ix.embedder = failingEmbedder{}
	_, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil)

	// Then
	if err == nil {
		t.Fatal("IndexRepo() err = nil, want the embedding failure surfaced")
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("embedding endpoint unreachable")
}

func TestIndexRepoRecordsTheDependenciesFromGoMod(t *testing.T) {
	// Given a fixture repository whose tree holds a go.mod
	h := newHarnessFiles(t, map[string]string{
		"backend/go.mod":  "module github.com/trick77/peeq\n\nrequire github.com/ncruces/go-sqlite3 v0.23.3\n",
		"backend/main.go": "package main\n\nfunc main() {}\n",
	}, nil)
	st := h.stateOf(t)

	// When
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Then
	var publishes, requires int
	if err := h.db.QueryRow(
		`SELECT
		   sum(direction = 'publishes'), sum(direction = 'requires')
		 FROM repo_deps WHERE repo = ?`, h.spec.Name).Scan(&publishes, &requires); err != nil {
		t.Fatalf("read repo_deps: %v", err)
	}
	if publishes != 1 || requires != 1 {
		t.Errorf("repo_deps has %d published / %d required, want 1 / 1", publishes, requires)
	}
}

func TestIndexRepoSurvivesAnUnparsableGoMod(t *testing.T) {
	// A broken manifest is a missing edge, never a failed index run: the
	// corpus is other people's code and one bad file must not stop indexing.
	h := newHarnessFiles(t, map[string]string{
		"go.mod":  "this is not a go.mod\n",
		"main.go": "package main\n",
	}, nil)
	st := h.stateOf(t)

	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("index must not fail on a broken manifest: %v", err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM repo_deps WHERE repo = ?`, h.spec.Name).Scan(&n); err != nil {
		t.Fatalf("read repo_deps: %v", err)
	}
	if n != 0 {
		t.Errorf("repo_deps has %d rows for a broken manifest, want 0", n)
	}
}
