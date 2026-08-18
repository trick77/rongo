package ask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repodeps"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
)

// moduleByDir is the mapping the real router gets from internal/modules: a
// path's directory is its module.
func moduleByDir(_ string, p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[:i]
	}
	return "."
}

func TestCandidatesGroupByRepoAndScoreAsTheirBestHit(t *testing.T) {
	// Given a big module with many mediocre hits and a small one with a single
	// very good hit. Phase 3 measured what summing does here: peeq's httpapi
	// has 1135 chunks and buries one excellent hit under twenty average ones.
	hits := []retrieve.Hit{
		{ChunkID: 1, Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.30},
		{ChunkID: 2, Repo: "peeq", Path: "backend/internal/httpapi/b.go", Score: 0.28},
		{ChunkID: 3, Repo: "peeq", Path: "backend/internal/httpapi/c.go", Score: 0.27},
		{ChunkID: 4, Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.60},
	}

	// When
	got := candidates(hits, moduleByDir)

	// Then
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].ModuleKey != "backend/internal/download" {
		t.Errorf("leading candidate is %q, want the module with the best single hit", got[0].ModuleKey)
	}
	if got[0].Score != 0.60 {
		t.Errorf("score = %v, want the best hit's score", got[0].Score)
	}
	if len(got[1].Hits) != 3 {
		t.Errorf("second candidate carries %d hits, want all 3 of its own", len(got[1].Hits))
	}
}

func TestDominatesComparesTheTopTwo(t *testing.T) {
	cs := []Candidate{{Score: 0.60}, {Score: 0.40}}

	// (0.60-0.40)/0.60 = 0.33
	if !dominates(cs, 0.25) {
		t.Error("a third clear of the runner-up must run on silently")
	}
	if dominates(cs, 0.50) {
		t.Error("under a stricter margin the same pair must be asked about")
	}
	if !dominates([]Candidate{{Score: 0.6}}, 0.25) {
		t.Error("a single candidate has nothing to be ambiguous with")
	}
	if !dominates(nil, 0.25) {
		t.Error("no candidates is the nothing-found path, not a clarification")
	}
}

// testLLM fakes the completions endpoint: every request's user-message
// content is handed to fn, and fn's return value becomes the reply. Copied
// from understand_test.go's modelUpstream pattern, adapted to let a test tell
// the judge call apart from the naming calls by inspecting the prompt.
//
// The naming step in route.go fires its Complete calls concurrently, so this
// fake's own handler runs on multiple goroutines at once — a mutex serialises
// calls into fn so tests that record prompts into a plain slice do not race.
func testLLM(t *testing.T, fn func(prompt string) string) *llm.Client {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		var prompt string
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		mu.Lock()
		content := fn(prompt)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
}

// testDBWithDeps opens a fresh migrated database, seeds a repo_state row for
// every repository named in deps (plus peeq/loom/go-sqlite3, which the tests
// below use in hits without necessarily depending on anything), and syncs
// each deps entry's go.mod into repo_deps.
func testDBWithDeps(t *testing.T, deps map[string]string) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}
	for _, repo := range []string{"peeq", "loom", "go-sqlite3"} {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO repo_state (name, clone_url) VALUES (?, ?)`,
			repo, "https://example.invalid/"+repo+".git"); err != nil {
			t.Fatalf("seed repo_state(%s): %v", repo, err)
		}
	}
	for repo, goMod := range deps {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO repo_state (name, clone_url) VALUES (?, ?)`,
			repo, "https://example.invalid/"+repo+".git"); err != nil {
			t.Fatalf("seed repo_state(%s): %v", repo, err)
		}
		if err := repodeps.Sync(context.Background(), db, repo, map[string][]byte{"go.mod": []byte(goMod)}); err != nil {
			t.Fatalf("sync %s: %v", repo, err)
		}
	}
	return db
}

// newTestRouter builds a Router over the fake client and database, using the
// same defaults production wires: a quarter-margin and the default module
// cut. No repository in these tests has enough indexed files to make the
// clustering itself interesting, so every candidate falls back to its
// directory as its module key — the same fallback moduleByDir exercises above.
func newTestRouter(t *testing.T, c *llm.Client, db *sql.DB) *Router {
	t.Helper()
	return NewRouter(c, db, 0.25, modules.Opts{MinChunks: 1, MaxChunks: 1 << 20})
}

func TestRouteAnswersWithoutAskingWhenOneCandidateDominates(t *testing.T) {
	// Given hits where one module is far ahead
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("no model call may happen when the margin is clear; got %q", prompt)
		return ""
	}), testDBWithDeps(t, nil))

	// When
	got, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
		{Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.1},
	})

	// Then
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a dominant candidate must not produce a question")
	}
}

// TestRouteFastPathMakesNoDependencyQuery pins the ladder's ordering: the
// manifest-dependency check is an O(n^2) set of database round trips, and
// Route must not pay for it when the margin already dominates. repo_deps is
// dropped from the database, so ANY call into Related/anyDependency would
// fail with a "no such table" error — a passing Route() here is the proof
// that the fast path never reaches it, not just an assumption about the code
// that happens to hold today.
func TestRouteFastPathMakesNoDependencyQuery(t *testing.T) {
	db := testDBWithDeps(t, nil)
	if _, err := db.Exec(`DROP TABLE repo_deps`); err != nil {
		t.Fatalf("drop repo_deps: %v", err)
	}
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("no model call may happen when the margin is clear; got %q", prompt)
		return ""
	}), db)

	got, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
		{Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.1},
	})
	if err != nil {
		t.Fatalf("route: %v — the dominant path must never reach the dependency query, which cannot run without repo_deps", err)
	}
	if got.Ask {
		t.Error("a dominant candidate must not produce a question")
	}
}

func TestRouteDoesNotAskWhenTheRepositoriesDependOnEachOther(t *testing.T) {
	// Given two close candidates in repositories joined by a manifest. This is
	// composition: asking would force the reader to pick half a mechanism.
	db := testDBWithDeps(t, map[string]string{
		"peeq":       "module github.com/trick77/peeq\n\nrequire github.com/ncruces/go-sqlite3 v0.23.3\n",
		"go-sqlite3": "module github.com/ncruces/go-sqlite3\n",
	})
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("the dependency is a hard signal; no model may be asked. prompt: %q", prompt)
		return ""
	}), db)

	got, err := r.Route(context.Background(), "wie oeffnet peeq die Datenbank?", []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/store/store.go", Score: 0.50},
		{Repo: "go-sqlite3", Path: "driver/driver.go", Score: 0.48},
	})

	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("composition must be answered, not asked about")
	}
	if len(got.Candidates) != 2 {
		t.Errorf("both parts must stay in the turn, got %d", len(got.Candidates))
	}
}

func TestRouteAsksAndNamesTheCandidates(t *testing.T) {
	// Given two close candidates with no dependency between them — peeq and
	// loom both have an httpapi package and neither pulls the other.
	var prompts []string
	llmFake := testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"HTTP-Schicht von peeq","summary":"Nimmt Anfragen entgegen und beantwortet sie."}`
	})
	r := newTestRouter(t, llmFake, testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "wie ist die Authentisierung geloest?", []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
	})

	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("two unrelated candidates at the same score are a question")
	}
	for i, c := range got.Candidates {
		if c.Title == "" || c.Summary == "" {
			t.Errorf("candidate %d has no title or summary; a card showing a bare module key asks nothing", i)
		}
	}
	// one judgement plus one naming call per candidate
	if len(prompts) != 3 {
		t.Errorf("made %d model calls, want 3 (1 judge + 2 names)", len(prompts))
	}
}

func TestRouteNamesNobodyWhenTheJudgeSaysCompose(t *testing.T) {
	// Naming is deferred behind the judgement on purpose: a composed turn must
	// not pay for titles nobody will read.
	var calls int
	r := newTestRouter(t, testLLM(t, func(string) string {
		calls++
		return `{"decision":"compose"}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("the judge said compose")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 — nothing is named when nothing is shown", calls)
	}
}

func TestRouteCapsTheCardAtFiveCandidates(t *testing.T) {
	// The spec says two to five named candidates. A card with eleven is not a
	// question a person answers.
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"T","summary":"S"}`
	}), testDBWithDeps(t, nil))

	var hits []retrieve.Hit
	for i := 0; i < 11; i++ {
		hits = append(hits, retrieve.Hit{
			Repo:  fmt.Sprintf("r%d", i),
			Path:  fmt.Sprintf("m%d/f.go", i),
			Score: 0.50 - float64(i)/1000,
		})
	}

	got, err := r.Route(context.Background(), "frage", hits)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(got.Candidates) != maxCandidates {
		t.Errorf("card offers %d candidates, want %d", len(got.Candidates), maxCandidates)
	}
}

func TestRouteJudgeDecodeFailureMeansAskNotCrashNotCompose(t *testing.T) {
	// A judge reply that is not the expected JSON shape must not crash the
	// turn and must not silently compose unrelated mechanisms either: the
	// safe reading is "ask", which costs the reader one click.
	var prompts []string
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, judgeMarker) {
			return "Ich denke, das gehoert zusammen."
		}
		return `{"title":"T","summary":"S"}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	})

	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Error("an undecodable judgement must fall back to ask, not compose")
	}
	// the ladder continued into naming rather than crashing: 1 judge + 2 names
	if len(prompts) != 3 {
		t.Errorf("made %d model calls, want 3 (1 judge + 2 names)", len(prompts))
	}
}

func TestRouteNamingFailureKeepsTheModuleKeyAsTitle(t *testing.T) {
	// The controller ruling: a naming call that fails must not fail the turn.
	// That candidate keeps its module key as the title and an empty summary.
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return "not json"
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	})

	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("the judge said ask")
	}
	for i, c := range got.Candidates {
		if c.Title != c.ModuleKey {
			t.Errorf("candidate %d title = %q, want the module key %q", i, c.Title, c.ModuleKey)
		}
		if c.Summary != "" {
			t.Errorf("candidate %d summary = %q, want empty", i, c.Summary)
		}
	}
}

// testLLMWithModel is testLLM plus the deployment name each request carried,
// so a test can tell ShortGate and Pro calls apart the same way
// understand_test.go's modelUpstream does.
func testLLMWithModel(t *testing.T, fn func(prompt string) string) (*llm.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		var prompt string
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		mu.Lock()
		models = append(models, req.Model)
		content := fn(prompt)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), &models
}

func TestRouteJudgeDefaultsToTheShortGateDeployment(t *testing.T) {
	// Matches what is deployed: the judge is a one-word decision, not worth
	// the Pro queue.
	c, models := testLLMWithModel(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"compose"}`
		}
		return `{"title":"T","summary":"S"}`
	})
	r := newTestRouter(t, c, testDBWithDeps(t, nil))

	if _, err := r.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	}); err != nil {
		t.Fatalf("route: %v", err)
	}

	if len(*models) != 1 || (*models)[0] != llm.ShortGateDeployment {
		t.Errorf("judge ran on %v, want a single call on %q", *models, llm.ShortGateDeployment)
	}
}

func TestRouteWithJudgeDeploymentOverridesTheJudgeOnly(t *testing.T) {
	// The phase 4b eval harness needs the judge on Pro to measure it against
	// ShortGate; WithJudgeDeployment(nil) is how it asks for that — nil means
	// "no override", which resolves to the client's default, Pro.
	c, models := testLLMWithModel(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"T","summary":"S"}`
	})
	r := newTestRouter(t, c, testDBWithDeps(t, nil)).WithJudgeDeployment(nil)

	got, err := r.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("the judge said ask")
	}
	if len(*models) == 0 || (*models)[0] != llm.ProDeployment {
		t.Errorf("judge ran on %v, want the first call on %q", *models, llm.ProDeployment)
	}
}

// TestRouteWithJudgeDeploymentDoesNotMutateTheReceiver guards against a
// shared, production Router being silently pointed at Pro: WithJudgeDeployment
// must hand back a new Router and leave the one it was called on running the
// deployment it already had.
func TestRouteWithJudgeDeploymentDoesNotMutateTheReceiver(t *testing.T) {
	c, models := testLLMWithModel(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"compose"}`
		}
		return `{"title":"T","summary":"S"}`
	})
	base := newTestRouter(t, c, testDBWithDeps(t, nil))
	_ = base.WithJudgeDeployment(nil) // a second Router, deliberately discarded here

	if _, err := base.Route(context.Background(), "frage", []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "loom", Path: "b/y.go", Score: 0.49},
	}); err != nil {
		t.Fatalf("route: %v", err)
	}

	if len(*models) != 1 || (*models)[0] != llm.ShortGateDeployment {
		t.Errorf("the original Router ran the judge on %v, want it still on %q — WithJudgeDeployment must not mutate it",
			*models, llm.ShortGateDeployment)
	}
}

// TestNameSystemForbidsSharpS pins the Swiss-orthography rule on the naming
// prompt: title and summary are both reader-visible, so nameSystem must
// forbid ß exactly like answerCommon does for the answer text. No LLM call —
// this only inspects the prompt string.
func TestNameSystemForbidsSharpS(t *testing.T) {
	if !strings.Contains(nameSystem, "ß") {
		t.Fatalf("nameSystem must name the forbidden character ß, got:\n%s", nameSystem)
	}
	if !strings.Contains(nameSystem, "ss") {
		t.Fatalf("nameSystem must tell the model to use ss instead, got:\n%s", nameSystem)
	}
	if strings.Count(nameSystem, "ß") != 1 {
		t.Fatalf("nameSystem should mention ß only where it forbids it, not use it itself, got:\n%s", nameSystem)
	}
}

// TestNameNormalisesSharpSInTitleAndSummary pins the guarantee, not just the
// request: even when the model ignores nameSystem's instruction and replies
// with ß anyway (as it did on the running card), the candidate that comes
// back from name() must carry ss in both Title and Summary — the same
// runtime normalisation answer.go applies to streamed answer tokens and
// title.go applies to thread titles. No LLM call: the fake server returns a
// canned reply containing ß.
func TestNameNormalisesSharpSInTitleAndSummary(t *testing.T) {
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		return `{"title":"Ausschließlich SQL-Migration","summary":"Enthält Migrationsskripte einschließlich Indizes."}`
	}), testDBWithDeps(t, nil))

	cs := []Candidate{
		{Repo: "peeq", ModuleKey: "backend/internal/store", Hits: []retrieve.Hit{
			{Repo: "peeq", Path: "backend/internal/store/migrate.go", Score: 0.5},
		}},
	}

	named, err := r.name(context.Background(), "wie migriert peeq das schema?", cs)
	if err != nil {
		t.Fatalf("name() err = %v", err)
	}
	if len(named) != 1 {
		t.Fatalf("name() returned %d candidates, want 1", len(named))
	}
	got := named[0]
	if strings.Contains(got.Title, "ß") {
		t.Errorf("Title still contains ß: %q", got.Title)
	}
	if strings.Contains(got.Summary, "ß") {
		t.Errorf("Summary still contains ß: %q", got.Summary)
	}
	if !strings.Contains(got.Title, "Ausschliesslich") {
		t.Errorf("Title = %q, want ß normalised to ss (Ausschliesslich)", got.Title)
	}
	if !strings.Contains(got.Summary, "einschliesslich") {
		t.Errorf("Summary = %q, want ß normalised to ss (einschliesslich)", got.Summary)
	}
}
