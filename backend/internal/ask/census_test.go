package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// The link census: "what does this user interface link to" is a listing,
// and a listing is read from the index, not searched for. Every navigation
// site of the named repository lands as a source before the symbol walk, so
// the walk resolves `environment.portalUrl` to the file that sets it; the
// listing block names every site whether or not it landed.
func TestCensus_landsEveryLinkSiteOfTheRepositoryAndWalksFromThem(t *testing.T) {
	// Given: a UI with two link sites, one on a variable the environment
	// file defines, and a second UI whose links must stay out.
	db := gatherDB(t)
	seedRepo(t, db, "claims-ui")
	seedRepo(t, db, "policy-ui")
	seedChunkIn(t, db, "claims-ui", "src/nav.html", 0, 1, 20, "",
		`<a href="https://portal.example.ch/claims">Portal</a>`)
	seedTokenIn(t, db, "claims-ui", "src/nav.html", "link", "https://portal.example.ch/claims", 5)
	seedChunkIn(t, db, "claims-ui", "src/open.ts", 0, 1, 20, "openPortal",
		"function openPortal() { window.open(`${environment.portalUrl}/claims`); }")
	seedTokenIn(t, db, "claims-ui", "src/open.ts", "link", "${environment.portalUrl}/claims", 3)
	seedChunkIn(t, db, "claims-ui", "src/environment.ts", 0, 1, 10, "environment",
		`export const environment = { portalUrl: "https://portal.example.ch" };`)
	seedSymbolIn(t, db, "claims-ui", "src/environment.ts", "environment", 1)
	seedChunkIn(t, db, "policy-ui", "src/nav.html", 0, 1, 20, "",
		`<a href="https://elsewhere.example.ch">Elsewhere</a>`)
	seedTokenIn(t, db, "policy-ui", "src/nav.html", "link", "https://elsewhere.example.ch", 1)
	g := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 24000})

	// When: the census runs with NO search hits at all.
	census, err := g.LinkCensus(context.Background(), []string{"claims-ui"})
	if err != nil {
		t.Fatalf("LinkCensus: %v", err)
	}
	sources, err := g.GatherSeeded(context.Background(), nil, census.Landings, nil)
	if err != nil {
		t.Fatalf("GatherSeeded: %v", err)
	}

	// Then: both sites landed, with the index's spelling as the reason, the
	// environment file arrived by the walk, and the other UI is absent.
	nav, ok := sourceIn(sources, "claims-ui", "src/nav.html")
	if !ok || nav.Reason != "link:https://portal.example.ch/claims" {
		t.Errorf("nav.html: %+v (present %v)", nav, ok)
	}
	open, ok := sourceIn(sources, "claims-ui", "src/open.ts")
	if !ok || open.Reason != "link:${environment.portalUrl}/claims" {
		t.Errorf("open.ts: %+v (present %v)", open, ok)
	}
	if !hasIn(sources, "claims-ui", "src/environment.ts") {
		t.Errorf("the walk did not resolve environment from the landing: %v", repoPaths(sources))
	}
	if hasIn(sources, "policy-ui", "src/nav.html") {
		t.Errorf("another repository's links landed: %v", repoPaths(sources))
	}
	if census.Sites != 2 {
		t.Errorf("Sites = %d, want 2", census.Sites)
	}
	// The listing block: every site with its place, in the prompt's words.
	for _, want := range []string{
		"https://portal.example.ch/claims", "claims-ui/src/nav.html:5",
		"${environment.portalUrl}/claims", "claims-ui/src/open.ts:3",
	} {
		if !strings.Contains(census.Listing, want) {
			t.Errorf("listing lacks %q:\n%s", want, census.Listing)
		}
	}
	if strings.Contains(census.Listing, "elsewhere") {
		t.Errorf("listing carries another repository:\n%s", census.Listing)
	}
}

func TestCensus_capsTheLandingsAndListsTheRest(t *testing.T) {
	// Given: more sites than censusMaxLandings, one per file.
	db := gatherDB(t)
	seedRepo(t, db, "big-ui")
	for i := 0; i < censusMaxLandings+5; i++ {
		path := "src/p" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".html"
		seedChunkIn(t, db, "big-ui", path, 0, 1, 5, "", `<a href="https://x.example.ch/`+path+`">x</a>`)
		seedTokenIn(t, db, "big-ui", path, "link", "https://x.example.ch/"+path, 1)
	}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 24000})

	// When
	census, err := g.LinkCensus(context.Background(), []string{"big-ui"})
	if err != nil {
		t.Fatalf("LinkCensus: %v", err)
	}

	// Then: the landings stop at the cap, the listing does not.
	if len(census.Landings) != censusMaxLandings {
		t.Errorf("landed %d, want %d", len(census.Landings), censusMaxLandings)
	}
	if census.Sites != censusMaxLandings+5 {
		t.Errorf("Sites = %d, want %d", census.Sites, censusMaxLandings+5)
	}
	if n := strings.Count(census.Listing, "https://x.example.ch/"); n != censusMaxLandings+5 {
		t.Errorf("listing names %d sites, want every one", n)
	}
}

func TestCensus_seedsSitAfterTheHitsAndAreNeverEvicted(t *testing.T) {
	// Given: one search hit and one landing, under a budget the walk cannot
	// spend at all.
	db := gatherDB(t)
	seedRepo(t, db, "claims-ui")
	hitID := seedChunkIn(t, db, "claims-ui", "src/a.ts", 0, 1, 20, "a", "const a = 1;")
	seedChunkIn(t, db, "claims-ui", "src/nav.html", 0, 1, 20, "", `<a href="https://p.example.ch">P</a>`)
	seedTokenIn(t, db, "claims-ui", "src/nav.html", "link", "https://p.example.ch", 1)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 1})

	// When
	census, err := g.LinkCensus(context.Background(), []string{"claims-ui"})
	if err != nil {
		t.Fatalf("LinkCensus: %v", err)
	}
	sources, err := g.GatherSeeded(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)}, census.Landings, nil)
	if err != nil {
		t.Fatalf("GatherSeeded: %v", err)
	}

	// Then
	if len(sources) != 2 || sources[0].Reason != "hit" || sources[1].Reason != "link:https://p.example.ch" {
		t.Errorf("sources: %+v", sources)
	}
}

func TestCensus_nothingForNoRepository(t *testing.T) {
	db := gatherDB(t)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 100})

	census, err := g.LinkCensus(context.Background(), nil)
	if err != nil {
		t.Fatalf("LinkCensus: %v", err)
	}
	if census.Sites != 0 || len(census.Landings) != 0 || census.Listing != "" {
		t.Errorf("a census over nothing found something: %+v", census)
	}
}

// twoStepUpstreamPrompt is twoStepUpstream with the ANSWER call's prompt
// captured, for a test that reads what the census put in front of the model.
func twoStepUpstreamPrompt(t *testing.T, understanding string, answerTokens ...string) (*llm.Client, *string) {
	t.Helper()
	var prompt string
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
			prompt += m.Content + "\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, answerTokens, "")
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &prompt
}

// TestRunReadsTheLinkCensusWhenTheQuestionAsksForOne: no search hit names
// the portal link, the understanding says census, the repository is named,
// and the answer is written from the census landings with the listing in
// front of the model.
func TestRunReadsTheLinkCensusWhenTheQuestionAsksForOne(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "claims-ui")
	hitID := seedChunkIn(t, db, "claims-ui", "src/a.ts", 0, 1, 20, "a", "const a = 1;")
	seedChunkIn(t, db, "claims-ui", "src/nav.html", 0, 1, 20, "", `<a href="https://portal.example.ch">Portal</a>`)
	seedTokenIn(t, db, "claims-ui", "src/nav.html", "link", "https://portal.example.ch", 7)
	search := &fakeSearch{hits: []retrieve.Hit{hitInFor(t, db, hitID)}, indexed: []string{"claims-ui"}}
	c, prompt := twoStepUpstreamPrompt(t, `{"intent":"where","terms":["links"],"code_terms":["href"],"repos":["claims-ui"],"census":"link"}`,
		"It links to the portal [2].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var gathering map[string]any
	ev := Events{OnDetail: func(step string, d map[string]any) {
		if step == "gathering" {
			gathering = d
		}
	}}

	got, _, err := p.Run(context.Background(), "in claims-ui, what links lead to apps outside it?", AudienceBA, LanguageEN, Thread{}, ev)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Scope.Census != CensusLink {
		t.Errorf("scope.Census = %q, want the record to carry it", got.Scope.Census)
	}
	if !strings.Contains(*prompt, "https://portal.example.ch  claims-ui/src/nav.html:7") {
		t.Errorf("the listing never reached the model:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "read from the index") {
		t.Errorf("the block must say what it is:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "[link site https://portal.example.ch]") {
		t.Errorf("the landing must say how it arrived:\n%s", *prompt)
	}
	if gathering["link_sites"] != 1 || gathering["links"] != 1 {
		t.Errorf("the trace does not report the census: %v", gathering)
	}
}

// TestRunWithoutANamedRepositoryRunsNoCensus: a census over the corpus is
// the spread the repository rung refuses.
func TestRunWithoutANamedRepositoryRunsNoCensus(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "claims-ui")
	hitID := seedChunkIn(t, db, "claims-ui", "src/a.ts", 0, 1, 20, "a", "const a = 1;")
	seedChunkIn(t, db, "claims-ui", "src/nav.html", 0, 1, 20, "", `<a href="https://portal.example.ch">Portal</a>`)
	seedTokenIn(t, db, "claims-ui", "src/nav.html", "link", "https://portal.example.ch", 7)
	search := &fakeSearch{hits: []retrieve.Hit{hitInFor(t, db, hitID)}}
	c, prompt := twoStepUpstreamPrompt(t, `{"intent":"where","terms":["links"],"code_terms":["href"],"repos":[],"census":"link"}`, "Nothing [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	if _, _, err := p.Run(context.Background(), "what links lead outside?", AudienceBA, LanguageEN, Thread{}, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(*prompt, "portal.example.ch") {
		t.Errorf("a census ran over the corpus:\n%s", *prompt)
	}
}
