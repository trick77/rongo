package ask

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
)

// A turn naming no repository still has a project once its search landed:
// the loop may look anywhere in that product and in the libraries it declares
// it uses, and nowhere else. An empty restriction reads as the whole corpus,
// so without this a grep matching in a second product admits and cites it.
func TestLocateCeiling_isTheProjectTheSearchLandedInPlusItsUsedLibraries(t *testing.T) {
	pm := libraryMap(t)
	cases := []struct {
		name    string
		known   []string
		sources []Source
		want    []string
	}{
		{"a product hit opens the product and the library it uses",
			nil, []Source{{Repo: "shop-backend", Hop: 0}},
			[]string{"acme-commons", "shop-backend", "shop-ui"}},
		{"a product declaring no uses gets no library",
			nil, []Source{{Repo: "legacy-crm", Hop: 0}},
			[]string{"legacy-crm"}},
		{"a walk hop into another product does not open it",
			nil, []Source{{Repo: "shop-ui", Hop: 0}, {Repo: "billing-api", Hop: 1}},
			[]string{"acme-commons", "shop-backend", "shop-ui"}},
		{"a library hit alone opens only the library",
			nil, []Source{{Repo: "acme-commons", Hop: 0}},
			[]string{"acme-commons"}},
		{"a restriction already set is the ceiling, never widened to its project",
			[]string{"shop-ui"}, []Source{{Repo: "shop-ui", Hop: 0}},
			[]string{"shop-ui"}},
		{"no hop-0 source falls back to the repositories gathered",
			nil, []Source{{Repo: "billing-api", Hop: 2}},
			[]string{"acme-commons", "billing-api"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := locateCeiling(c.known, c.sources, pm)
			if !slices.Equal(got, c.want) {
				t.Errorf("ceiling = %v, want %v", got, c.want)
			}
		})
	}
}

// The ceiling has to reach the lookups: a turn naming nothing runs the loop's
// grep inside the project its search landed in, never across the corpus.
func TestPipeline_locateOnATurnNamingNothingStaysInTheProject(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	if _, err := db.Exec(`UPDATE repo_state SET project = name`); err != nil {
		t.Fatalf("set projects: %v", err)
	}
	pm, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load: %v", err)
	}
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")

	var repos []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{}, repos: &repos}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setFoo"}`)}},
			{content: "fertig"},
		}, nil), searcher)
	// The fixture names peeq; this turn names nothing.
	namesNothing := strings.Replace(appleTVReply, `"repos": ["peeq"]`, `"repos": []`, 1)
	if namesNothing == appleTVReply {
		t.Fatal("the understanding fixture no longer names peeq; update the replacement")
	}
	c := twoStepUpstream(t, namesNothing, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}}, g,
		&fakeRouter{projects: pm})

	if _, _, err := p.Run(context.Background(), "How?", AudienceBA, LanguageEN, Thread{}, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(repos, []string{"peeq"}) {
		t.Errorf("grep ran under %v, want the landed project peeq alone", repos)
	}
}
