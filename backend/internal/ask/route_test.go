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
	"unicode/utf8"

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
	got, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
		{Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.1},
	}, nil, false)

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

	got, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
		{Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.1},
	}, nil, false)
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

	got, err := r.Route(context.Background(), "how does peeq open the database?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/store/store.go", Score: 0.50},
		{Repo: "go-sqlite3", Path: "driver/driver.go", Score: 0.48},
	}, nil, false)

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
	// Given two close candidates in ONE repository with no dependency between
	// them: peeq authenticates in two places and neither calls the other.
	// Inside a repository the judge is still what decides — the repository
	// rung above it only fires when the candidates span several.
	var prompts []string
	llmFake := testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"peeq HTTP layer","summary":"Takes requests and answers them."}`
	})
	r := newTestRouter(t, llmFake, testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false)

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

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false)
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
	// question a person answers. Eleven repositories is the repository card,
	// so the cap it lands on is four repositories plus the "all repositories"
	// entry — five buttons, the same as a module card's five.
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

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, hits, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(got.Candidates) != maxCandidates {
		t.Errorf("card offers %d candidates, want %d", len(got.Candidates), maxCandidates)
	}
}

// TestRouteCapsAModuleCardAtFiveCandidates is the same cap on the other card
// shape: eleven modules inside ONE repository never reach the repository rung,
// so the judge decides and maxCandidates applies to the modules themselves.
func TestRouteCapsAModuleCardAtFiveCandidates(t *testing.T) {
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"T","summary":"S"}`
	}), testDBWithDeps(t, nil))

	var hits []retrieve.Hit
	for i := 0; i < 11; i++ {
		hits = append(hits, retrieve.Hit{
			Repo:  "peeq",
			Path:  fmt.Sprintf("m%d/f.go", i),
			Score: 0.50 - float64(i)/1000,
		})
	}

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, hits, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(got.Candidates) != maxCandidates {
		t.Errorf("card offers %d candidates, want %d", len(got.Candidates), maxCandidates)
	}
	for i, c := range got.Candidates {
		if c.ModuleKey == "" {
			t.Errorf("candidate %d has no module key; a single repository must still produce a module card", i)
		}
	}
}

// TestRouteAsksWhichRepositoryWhenTheQuestionNamedNone is the rung end to end:
// a generic question — "how are token costs calculated in $?" names no
// system — whose hits land in two repositories ends with a card offering the
// repositories, not their modules, and never pays the judge to get there.
func TestRouteAsksWhichRepositoryWhenTheQuestionNamedNone(t *testing.T) {
	var prompts []string
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, judgeMarker) {
			t.Errorf("the repository rung decides on its own; the judge must not be paid for:\n%s", prompt)
			return `{"decision":"compose"}`
		}
		return `{"title":"Token cost per turn","summary":"Prices a turn from the model's own rates."}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how are token costs calculated in $?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/pricing/price.go", Score: 0.60},
		{Repo: "peeq", Path: "backend/internal/usage/meter.go", Score: 0.58},
		{Repo: "loom", Path: "backend/internal/cost/cost.go", Score: 0.30},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("a question naming no repository whose hits span two must ask which one")
	}
	if len(got.Candidates) != 3 {
		t.Fatalf("card offers %d entries, want 2 repositories plus \"all repositories\"", len(got.Candidates))
	}
	for i, c := range got.Candidates[:2] {
		if c.ModuleKey != "" {
			t.Errorf("entry %d has module key %q; a repository card offers repositories", i, c.ModuleKey)
		}
		if c.Title == "" || c.Summary == "" {
			t.Errorf("entry %d has no title or summary", i)
		}
	}
	if got.Candidates[0].Repo != "peeq" || got.Candidates[1].Repo != "loom" {
		t.Errorf("entries are %q then %q, want the repositories best first",
			got.Candidates[0].Repo, got.Candidates[1].Repo)
	}
	// peeq's two modules are one entry now, and it carries both their hits.
	if n := len(got.Candidates[0].Hits); n != 2 {
		t.Errorf("peeq's entry carries %d hits, want both of its modules'", n)
	}
	last := got.Candidates[len(got.Candidates)-1]
	wantTitle, wantSummary := AllReposChoice(LanguageEN)
	if last.Repo != "" || last.Title != wantTitle || last.Summary != wantSummary {
		t.Errorf("last entry = %+v, want the templated \"all repositories\" choice", last)
	}
	// two naming calls, one per repository — the "all" entry is templated and
	// the judge was never asked
	if len(prompts) != 2 {
		t.Errorf("made %d model calls, want 2 (one name per repository)", len(prompts))
	}
}

// TestRouteAsksWhichRepositoryEvenWhenOneLeadsClearly is the reported defect
// itself: "How are token costs calculated in $?" was answered across every
// repository because one module led by more than the margin. A leader says
// which module scored best, never which product the reader meant, so the
// repository rung sits ABOVE the margin and the card is asked anyway.
func TestRouteAsksWhichRepositoryEvenWhenOneLeadsClearly(t *testing.T) {
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			t.Errorf("the repository rung decides on its own; the judge must not be paid for:\n%s", prompt)
		}
		return `{"title":"Token cost per turn","summary":"Prices a turn."}`
	}), testDBWithDeps(t, nil))

	// (0.60-0.30)/0.60 = 0.5, twice the 0.25 margin: the old ladder answered
	// here without asking anything.
	hits := []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/pricing/price.go", Score: 0.60},
		{Repo: "loom", Path: "backend/internal/cost/cost.go", Score: 0.30},
	}
	ranked, err := r.Rank(context.Background(), hits)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if !Dominates(ranked.All, 0.25) {
		t.Fatal("the fixture must be one the margin dominates, or this test proves nothing")
	}

	got, err := r.Route(context.Background(), "how are token costs calculated in $?", AudienceDev, LanguageEN, hits, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Error("a dominant margin must not decide which repository the reader meant")
	}
}

// TestRouteDoesNotAskWhichRepositoryWhenTheReaderAskedForAllOfThem is the
// other half of the rung: "in all repos, how are token costs calculated?" is
// the reader answering the card in advance.
func TestRouteDoesNotAskWhichRepositoryWhenTheReaderAskedForAllOfThem(t *testing.T) {
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("asking for every repository settles it; no model may be asked. prompt: %q", prompt)
		return ""
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "in all repos, how are token costs calculated?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/pricing/price.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/cost/cost.go", Score: 0.49},
	}, nil, true)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a reader who asked for every repository must not be asked which one")
	}
	if len(got.Candidates) != 2 {
		t.Errorf("both repositories must stay in the turn, got %d", len(got.Candidates))
	}
}

// TestRouteDoesNotAskWhichRepositoryWhenTheyDependOnEachOther is the
// exception the repo_deps invariant buys: loom requiring what peeq publishes
// is one mechanism, and the rung sits below that check on purpose.
func TestRouteDoesNotAskWhichRepositoryWhenTheyDependOnEachOther(t *testing.T) {
	db := testDBWithDeps(t, map[string]string{
		"loom": "module github.com/trick77/loom\n\nrequire github.com/trick77/peeq v1.0.0\n",
		"peeq": "module github.com/trick77/peeq\n",
	})
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("a manifest dependency is a hard signal; no model may be asked. prompt: %q", prompt)
		return ""
	}), db)

	got, err := r.Route(context.Background(), "how are token costs calculated in $?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "loom", Path: "backend/internal/cost/cost.go", Score: 0.60},
		{Repo: "peeq", Path: "backend/internal/pricing/price.go", Score: 0.20},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("repositories joined by a manifest are composition, not a choice between products")
	}
}

// TestRepoCandidatesUnionTheHitsBestFirst pins what an entry on a repository
// card carries: every hit its modules put on the list, ordered best first so
// the naming call reads the repository's strongest code rather than whichever
// module happened to come first.
func TestRepoCandidatesUnionTheHitsBestFirst(t *testing.T) {
	got := repoCandidates([]Candidate{
		{Repo: "peeq", ModuleKey: "a", Score: 0.60, Hits: []retrieve.Hit{{ChunkID: 1, Score: 0.60}, {ChunkID: 2, Score: 0.30}}},
		{Repo: "loom", ModuleKey: "c", Score: 0.50, Hits: []retrieve.Hit{{ChunkID: 4, Score: 0.50}}},
		{Repo: "peeq", ModuleKey: "b", Score: 0.55, Hits: []retrieve.Hit{{ChunkID: 3, Score: 0.55}}},
	})

	if len(got) != 2 {
		t.Fatalf("got %d entries, want one per repository", len(got))
	}
	if got[0].Repo != "peeq" || got[0].Score != 0.60 {
		t.Errorf("leader = %q at %v, want peeq at its best module's score", got[0].Repo, got[0].Score)
	}
	if got[0].ModuleKey != "" {
		t.Errorf("module key = %q, want empty — that is what marks a repository entry", got[0].ModuleKey)
	}
	var ids []int64
	for _, h := range got[0].Hits {
		ids = append(ids, h.ChunkID)
	}
	if fmt.Sprint(ids) != "[1 3 2]" {
		t.Errorf("peeq's hits are %v, want both modules' hits best first", ids)
	}
}

func TestRepoCandidatesCapAtFourSoTheAllEntryFits(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 9; i++ {
		cs = append(cs, Candidate{Repo: fmt.Sprintf("r%d", i), Score: 0.50 - float64(i)/1000})
	}
	got := repoCandidates(cs)
	if len(got) != maxRepoCandidates {
		t.Errorf("offered %d repositories, want %d so the \"all repositories\" entry keeps the card at %d buttons",
			len(got), maxRepoCandidates, maxCandidates)
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
			return "I think these belong together."
		}
		return `{"title":"T","summary":"S"}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false)

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

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false)

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

func TestRouteJudgeDefaultsToTheProDeployment(t *testing.T) {
	// Matches what is deployed. The judge is a one-word decision, which is the
	// bar for the cheap lane — but phase 4c measured the two, pinned and twice,
	// and Pro decides six to seven of the 61 catalogue questions better. That
	// word is the difference between an answer and a question back, so it is
	// bought on the expensive queue; see Router.judgeDeployment.
	c, models := testLLMWithModel(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"compose"}`
		}
		return `{"title":"T","summary":"S"}`
	})
	r := newTestRouter(t, c, testDBWithDeps(t, nil))

	if _, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}

	if len(*models) != 1 || (*models)[0] != llm.ProDeployment {
		t.Errorf("judge ran on %v, want a single call on %q", *models, llm.ProDeployment)
	}
}

func TestRouteWithJudgeDeploymentOverridesTheJudgeOnly(t *testing.T) {
	// The eval harness needs the judge on the cheap lane to keep measuring it
	// against the deployed one; WithJudgeDeployment(llm.ShortGate()) is how it
	// asks for that. The override reaches the judge and nothing else.
	c, models := testLLMWithModel(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"T","summary":"S"}`
	})
	r := newTestRouter(t, c, testDBWithDeps(t, nil)).WithJudgeDeployment(llm.ShortGate())

	got, err := r.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("the judge said ask")
	}
	if len(*models) == 0 || (*models)[0] != llm.ShortGateDeployment {
		t.Errorf("judge ran on %v, want the first call on %q", *models, llm.ShortGateDeployment)
	}
}

// TestRouteWithJudgeDeploymentDoesNotMutateTheReceiver guards against a
// shared, production Router silently changing deployment: WithJudgeDeployment
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
	_ = base.WithJudgeDeployment(llm.ShortGate()) // a second Router, deliberately discarded here

	if _, err := base.Route(context.Background(), "frage", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "a/x.go", Score: 0.50},
		{Repo: "peeq", Path: "b/y.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}

	if len(*models) != 1 || (*models)[0] != llm.ProDeployment {
		t.Errorf("the original Router ran the judge on %v, want it still on %q — WithJudgeDeployment must not mutate it",
			*models, llm.ProDeployment)
	}
}

// TestDecideIsTheLadderRouteItselfRuns pins Decide as the ONE place the
// ladder's decision lives. The eval harness used to carry its own copy of it
// (askAt in the routing arms), which meant a change to Route's rung order
// left the harness compiling while it silently measured a policy the product
// no longer ran. Route calls Decide too, so there is nothing left to drift
// apart — this test fixes what Decide must say at each rung.
func TestDecideIsTheLadderRouteItselfRuns(t *testing.T) {
	dominant := []Candidate{{Score: 0.60}, {Score: 0.20}} // ratio 0.667
	tight := []Candidate{{Score: 0.51}, {Score: 0.49}}    // ratio 0.039

	// The margin dominates: no rung below it is consulted at all, so whatever
	// related and judged would have said must not be read.
	if Decide(dominant, 0.25, true, true, 0, false, true) {
		t.Error("a dominant pair answers without asking, whatever the later rungs would have said")
	}
	// A manifest dependency short-circuits the judge.
	if Decide(tight, 0.25, true, true, 0, false, true) {
		t.Error("a manifest dependency is composition; the judge must not override it")
	}
	// Past both, the judge decides — and is never defaulted.
	if !Decide(tight, 0.25, false, true, 0, false, true) {
		t.Error("the judge said ask")
	}
	if Decide(tight, 0.25, false, false, 0, false, true) {
		t.Error("the judge said compose")
	}
	// Last rung: the judge found the code ambiguous, but the reader's role
	// cannot resolve it, so the turn answers instead of asking a question
	// nobody can answer.
	if Decide(tight, 0.25, false, true, 0, false, false) {
		t.Error("a card the role cannot answer is not asked")
	}
}

// TestDecideAsksWhichRepositoryWhenTheQuestionNamedNone pins the repository
// rung: it is deterministic, it sits above the margin and above the judge,
// and the only things that outrank it are the reader's own words.
func TestDecideAsksWhichRepositoryWhenTheQuestionNamedNone(t *testing.T) {
	// A question naming no repository whose best hits are a clear leader in
	// one repository and a runner-up in another. The margin dominates
	// outright — 0.667 against 0.25 — and that is exactly the case this rung
	// exists for: a leader says which MODULE scored best, never which product
	// the reader meant.
	spread := []Candidate{{Repo: "peeq", Score: 0.60}, {Repo: "loom", Score: 0.20}}
	if !Decide(spread, 0.25, false, false, 0, false, true) {
		t.Error("candidates in two repositories with none named are a card, whatever the margin says")
	}
	if Decide(spread, 0.25, false, false, 0, true, true) {
		t.Error("a reader who asked for all repositories has already answered the card")
	}
	if Decide(spread, 0.25, true, false, 0, false, true) {
		t.Error("repositories joined by a manifest dependency are composition, not a choice")
	}
	if Decide(spread, 0.25, false, true, 1, false, true) {
		t.Error("a question that named the repository must not be asked which repository it meant")
	}

	// One repository, two modules: the rung does not fire, and the ladder
	// below it decides exactly as before.
	oneRepo := []Candidate{{Repo: "peeq", Score: 0.51}, {Repo: "peeq", Score: 0.49}}
	if Decide(oneRepo, 0.25, false, false, 0, false, true) {
		t.Error("a single repository is not a choice between repositories; the judge decides")
	}
	if !Decide(oneRepo, 0.25, false, true, 0, false, true) {
		t.Error("within one repository the judge still decides")
	}
}

// TestTheJudgeRunsOnProAndNamingDoesNot pins phase 4c's one exception to
// "Pro only where a human reads". The judge's output is a single word, but it
// decides whether the reader gets an answer or a question back, and pinned
// measurements put Pro six to seven questions ahead of the cheap lane over
// the 61-question catalogue against a residual spread of one to two. The
// naming calls stay on ShortGate: a wrong title costs a worse card, not a
// wrong turn.
func TestTheJudgeRunsOnProAndNamingDoesNot(t *testing.T) {
	var mu sync.Mutex
	models := map[string]string{}
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
		reply := `{"title":"HTTP-Schicht von peeq","summary":"Nimmt Anfragen entgegen."}`
		kind := "name"
		if strings.Contains(prompt, judgeMarker) {
			reply = `{"decision":"ask"}`
			kind = "judge"
		}
		mu.Lock()
		models[kind] = req.Model
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)

	r := newTestRouter(t, llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), testDBWithDeps(t, nil))
	if _, err := r.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}

	if models["judge"] != llm.ProDeployment {
		t.Errorf("judge ran on %q, want the Pro deployment", models["judge"])
	}
	if models["name"] != llm.ShortGateDeployment {
		t.Errorf("naming ran on %q, want the short-gate deployment — a title is not worth the expensive queue", models["name"])
	}

	// And the cheap lane is still reachable, because the harness has to keep
	// measuring the comparison the spec asks for.
	cheap := r.WithJudgeDeployment(llm.ShortGate())
	if _, err := cheap.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route on the cheap lane: %v", err)
	}
	if models["judge"] != llm.ShortGateDeployment {
		t.Errorf("overridden judge ran on %q, want the short-gate deployment", models["judge"])
	}
}

// TestEveryGateCallPinsItsTemperature guards the reproducibility of the two
// paid calls routing makes. Both hand back a label — one word from a set of
// two for the judge, a title and a sentence for the naming — and neither is
// read as prose, so a re-roll is a defect. Phase 4c measured the cost of not
// pinning them: two runs of the routing arm over frozen expansions and an
// unchanged corpus decided three of sixty-one questions differently.
func TestEveryGateCallPinsItsTemperature(t *testing.T) {
	var mu sync.Mutex
	var temps []*float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Temperature *float64 `json:"temperature"`
			Messages    []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		var prompt string
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		mu.Lock()
		temps = append(temps, req.Temperature)
		mu.Unlock()
		reply := `{"title":"HTTP-Schicht von peeq","summary":"Nimmt Anfragen entgegen."}`
		if strings.Contains(prompt, judgeMarker) {
			reply = `{"decision":"ask"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)

	r := newTestRouter(t, llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), testDBWithDeps(t, nil))
	if _, err := r.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}

	if len(temps) != 3 {
		t.Fatalf("made %d model calls, want 3 (1 judge + 2 names)", len(temps))
	}
	for i, temp := range temps {
		if temp == nil {
			t.Errorf("call %d sent no temperature; the endpoint's default would re-roll a one-word decision", i)
			continue
		}
		if *temp != gateTemperature {
			t.Errorf("call %d sent temperature %v, want %v", i, *temp, gateTemperature)
		}
	}
}

func TestExcerptOfCutsOnARuneBoundary(t *testing.T) {
	// Given code whose n-th BYTE falls inside a multi-byte rune. German
	// comments are the normal case in this corpus, so this is not exotic:
	// cutting at the byte offset leaves half a rune, and JSON encoding
	// replaces it with U+FFFD on the way to the model.
	s := strings.Repeat("a", 9) + "ä" + strings.Repeat("b", 20)

	got := excerptOf(s, 10)

	if !utf8.ValidString(got) {
		t.Errorf("excerpt is not valid UTF-8: %q", got)
	}
	if got != strings.Repeat("a", 9) {
		t.Errorf("excerpt = %q, want the 9 whole runes that fit in 10 bytes", got)
	}
	// The bound is still a bound: nothing longer may come back.
	if len(got) > 10 {
		t.Errorf("excerpt is %d bytes, want at most 10", len(got))
	}
}
