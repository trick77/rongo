package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/retrieve"
)

type fakeHistory struct {
	got     history.Query
	commits []history.Commit
}

func (f *fakeHistory) Search(_ context.Context, q history.Query) ([]history.Commit, error) {
	f.got = q
	return f.commits, nil
}

var fixedNow = time.Date(2026, 9, 17, 18, 0, 0, 0, time.UTC)

func changesUpstream(t *testing.T, understanding string, answerTokens ...string) (*fakeHistory, *Pipeline, *[]string) {
	t.Helper()
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": understanding}}},
			})
			return
		}
		for _, m := range req.Messages {
			prompts = append(prompts, m.Content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, answerTokens, "")
	}))
	t.Cleanup(srv.Close)
	h := &fakeHistory{commits: []history.Commit{
		{ID: 7, Repo: "rongo", Branch: "master", SHA: "7f2a492abcdef0123456789", CommittedAt: fixedNow.Add(-2 * time.Hour),
			Subject: "Test sources are labelled", Body: "So the model can tell a fake from the client.", Paths: []string{"backend/internal/ask/answer.go"}},
		{ID: 5, Repo: "rongo", Branch: "master", SHA: "9fe6584abcdef0123456789", CommittedAt: fixedNow.Add(-30 * time.Hour),
			Subject: "token_auth: bearer for Bitbucket", Paths: []string{"backend/internal/gitrepo/gitrepo.go"}},
	}}
	search := &fakeSearch{hits: []retrieve.Hit{{ChunkID: 1, Repo: "rongo", Path: "a.go"}}}
	p := NewPipeline(fakeLLM(t, srv), search, NewGatherer(gatherDB(t), GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{}).
		WithHistory(h, func() time.Time { return fixedNow })
	return h, p, &prompts
}

func TestRun_changesAnswersFromTheCommitLaneNotTheFiles(t *testing.T) {
	h, p, prompts := changesUpstream(t,
		`{"intent":"changes","since_days":2,"topic":"","terms":["recent changes"],"code_terms":[]}`,
		"Two changes landed [1][2].")
	var steps []string

	got, clar, err := p.Run(context.Background(), "what changed in the last 2 days?", AudienceDev, LanguageEN, Thread{},
		Events{OnStatus: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if clar != nil {
		t.Fatal("a changes turn asked instead of answering")
	}

	// The lane was asked for the window against the injected clock, across
	// every repository, with no topic.
	if !h.got.Since.Equal(fixedNow.Add(-48 * time.Hour)) {
		t.Errorf("since = %v, want two days before the fixed clock", h.got.Since)
	}
	if len(h.got.Repos) != 0 || h.got.Topic != "" {
		t.Errorf("query = %+v, want every repository and no topic", h.got)
	}
	// No routing, no gathering: the steps are understanding, searching,
	// answering, writing.
	if strings.Join(steps, ",") != "understanding,searching,answering,writing" {
		t.Errorf("steps = %v", steps)
	}
	// The citations are commits, carrying what the chip shows.
	if len(got.Citations) != 2 {
		t.Fatalf("citations = %+v", got.Citations)
	}
	c := got.Citations[0]
	if c.Kind != SourceCommit || c.SHA != "7f2a492abcdef0123456789" || c.Subject != "Test sources are labelled" ||
		c.CommittedAt != "2026-09-17T16:00:00Z" || c.Path != "" {
		t.Errorf("citation = %+v", c)
	}
	// The record carries the window and the intent.
	if got.Scope.Intent != IntentChanges || got.Scope.SinceDays != 2 {
		t.Errorf("scope = %+v", got.Scope)
	}
	if len(got.Sources) != 2 || !got.Sources[0].IsCommit() || got.Sources[0].CommitID != 7 {
		t.Errorf("sources = %+v", got.Sources)
	}
	// The prompt shows commits, dated and with their paths, and the rule.
	all := strings.Join(*prompts, "\n")
	for _, want := range []string{
		"[1] rongo commit 7f2a492 2026-09-17: Test sources are labelled",
		"So the model can tell a fake from the client.",
		"paths: backend/internal/ask/answer.go",
		"[2] rongo commit 9fe6584 2026-09-16: token_auth: bearer for Bitbucket",
		"commits of the last 2 days",
		"the code itself is not among the sources",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("prompt lacks %q\n%s", want, all)
		}
	}
}

func TestRun_changesDefaultsTheWindowAndNarrowsToTopicAndRepo(t *testing.T) {
	h, p, prompts := changesUpstream(t,
		`{"intent":"changes","since_days":0,"topic":"snapshot handling","terms":[],"code_terms":[],"repos":["rongo"]}`,
		"One [1].")

	got, _, err := p.Run(context.Background(), "what changed in the snapshot handling in rongo lately?", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.got.Topic != "snapshot handling" || len(h.got.Repos) != 1 || h.got.Repos[0] != "rongo" {
		t.Errorf("query = %+v", h.got)
	}
	if got.Scope.SinceDays != DefaultSinceDays || got.Scope.Topic != "snapshot handling" {
		t.Errorf("scope = %+v, want the default window and the topic recorded", got.Scope)
	}
	all := strings.Join(*prompts, "\n")
	if !strings.Contains(all, `filtered to those mentioning "snapshot handling"`) {
		t.Errorf("prompt does not name the topic:\n%s", all)
	}
	if !strings.Contains(all, "One line per change") {
		t.Errorf("an Analyst digest is one line per change:\n%s", all)
	}
}

func TestRun_changesWithNoCommitsIsATemplatedAnswer(t *testing.T) {
	h, p, prompts := changesUpstream(t,
		`{"intent":"changes","since_days":1,"topic":"","terms":[],"code_terms":[],"repos":["rongo"]}`,
		"never streamed")
	h.commits = nil

	got, _, err := p.Run(context.Background(), "what changed yesterday?", AudienceBA, LanguageDE, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Text != "Keine Commits in den letzten 1 Tagen in rongo." {
		t.Errorf("text = %q", got.Text)
	}
	if len(*prompts) != 0 {
		t.Error("an answer with no sources came from a model")
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v", got.Citations)
	}
}

func TestRun_withoutTheLaneAChangesQuestionRunsTheOrdinaryPipeline(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")
	c := twoStepUpstream(t, `{"intent":"changes","since_days":2,"terms":["x"],"code_terms":[]}`, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var steps []string
	if _, _, err := p.Run(context.Background(), "what changed?", AudienceBA, LanguageEN, Thread{},
		Events{OnStatus: func(s string) { steps = append(steps, s) }}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(strings.Join(steps, ","), "routing") {
		t.Errorf("steps = %v, want the ordinary pipeline", steps)
	}
}

func TestNoChanges_everyLanguageAndTheTopicForm(t *testing.T) {
	sc := Scope{SinceDays: 3}
	if got := NoChanges(LanguageEN, sc); got != "No commits in the last 3 days in the indexed repositories." {
		t.Errorf("EN = %q", got)
	}
	sc.Topic = "login"
	sc.Known = []string{"shop", "loom"}
	if got := NoChanges(LanguageFR, sc); got != `Aucun commit concernant "login" dans les 3 derniers jours dans shop, loom.` {
		t.Errorf("FR = %q", got)
	}
	if got := NoChanges(LanguageIT, sc); !strings.Contains(got, "Nessun commit") {
		t.Errorf("IT = %q", got)
	}
}

func TestClampSinceDays(t *testing.T) {
	for in, want := range map[int]int{0: DefaultSinceDays, -1: DefaultSinceDays, 2: 2, 1000: maxSinceDays} {
		if got := clampSinceDays(in); got != want {
			t.Errorf("clampSinceDays(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestDocsOnly_aCommitIsNeverDocumentation(t *testing.T) {
	if DocsOnly([]Source{{Kind: SourceCommit, Path: ""}}) {
		t.Error("a commit source read as documentation")
	}
}
