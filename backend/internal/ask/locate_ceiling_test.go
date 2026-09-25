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

// The turn's ceiling: the projects of what the question named or its search
// hit, with the libraries those projects declare in uses. Nothing else: the
// walk, the crossings, the gap pass and the loop all run under it.
func TestTurnCeiling_isTheProjectPlusItsUsedLibraries(t *testing.T) {
	pm := libraryMap(t)
	cases := []struct {
		name       string
		known, hit []string
		want       []string
	}{
		{"a product hit opens the product and the library it uses",
			nil, []string{"shop-backend"}, []string{"acme-commons", "shop-backend", "shop-ui"}},
		{"a product declaring no uses gets no library",
			nil, []string{"legacy-crm"}, []string{"legacy-crm"}},
		{"a library hit alone opens only the library",
			nil, []string{"acme-commons"}, []string{"acme-commons"}},
		{"a named member opens its project and the libraries it uses",
			[]string{"shop-ui"}, []string{"shop-ui"}, []string{"acme-commons", "shop-backend", "shop-ui"}},
		{"a named product without uses stays alone",
			[]string{"legacy-crm"}, nil, []string{"legacy-crm"}},
		{"nothing named and nothing hit is no ceiling",
			nil, nil, nil},
		{"no project data confines to what was hit, never to nothing",
			nil, []string{"unknown-repo"}, []string{"unknown-repo"}},
		{"no project data confines to what was named",
			[]string{"unknown-a", "unknown-b"}, nil, []string{"unknown-a", "unknown-b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := turnCeiling(c.known, c.hit, pm)
			if !slices.Equal(got, c.want) {
				t.Errorf("ceiling = %v, want %v", got, c.want)
			}
		})
	}
}

func TestGather_withinACeilingNeverWalksOutOfIt(t *testing.T) {
	// A symbol the hit references is defined only in another project. Under
	// the turn's ceiling the walk does not follow it there.
	db := gatherDB(t)
	seedRepo(t, db, "go-sqlite3")
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/store/store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return ZeroBlob(p) }")
	seedChunkIn(t, db, "go-sqlite3", "blob.go", 0, 40, 60, "ZeroBlob",
		"func ZeroBlob(p string) error { return nil }")
	seedSymbolIn(t, db, "go-sqlite3", "blob.go", "ZeroBlob", 40)
	hits := []retrieve.Hit{hitInFor(t, db, hitID)}

	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})
	confined, err := g.within([]string{"peeq"}).Gather(context.Background(), hits)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(confined, "go-sqlite3", "blob.go") {
		t.Errorf("sources = %v, want nothing outside the ceiling", repoPaths(confined))
	}
	open, err := g.within([]string{"peeq", "go-sqlite3"}).Gather(context.Background(), hits)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(open, "go-sqlite3", "blob.go") {
		t.Errorf("sources = %v, want the definition when its repository is inside", repoPaths(open))
	}
}

// Run applies the ceiling to the walk, not only to the loop: a turn naming
// nothing whose search landed in one project never gathers from another.
func TestPipeline_theWalkStaysInTheProjectTheSearchLandedIn(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "go-sqlite3")
	if _, err := db.Exec(`UPDATE repo_state SET project = name`); err != nil {
		t.Fatalf("set projects: %v", err)
	}
	pm, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load: %v", err)
	}
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/store/store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return ZeroBlob(p) }")
	seedChunkIn(t, db, "go-sqlite3", "blob.go", 0, 40, 60, "ZeroBlob",
		"func ZeroBlob(p string) error { return nil }")
	seedSymbolIn(t, db, "go-sqlite3", "blob.go", "ZeroBlob", 40)

	namesNothing := strings.Replace(appleTVReply, `"repos": ["peeq"]`, `"repos": []`, 1)
	c := twoStepUpstream(t, namesNothing, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{projects: pm})

	answer, _, err := p.Run(context.Background(), "How?", AudienceDev, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, s := range answer.Sources {
		if s.Repo == "go-sqlite3" {
			t.Errorf("sources reached %s %s, outside the project the search landed in", s.Repo, s.Path)
		}
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

// The trace carries where the answer was pointed, so a turn read back shows
// it.
func TestWithLocateDetail_carriesThePointer(t *testing.T) {
	place := &LocatePlace{Repo: "peeq", Path: "ConverterPetRegistry.java", Line: 162}
	d := withLocateDetail(map[string]any{}, LocateReport{
		Rounds: 1, Calls: []string{"grep(setAnzahlhaustiere)"},
		Found: "FOUND: ConverterPetRegistry.java:162 sets it.", Concluded: true,
		Outcome: LocatePointed, Place: place,
	})
	if d["locate_place"] != place || d["locate_outcome"] != LocatePointed {
		t.Errorf("detail = %v, want the place it pointed at", d)
	}
	if d["locate_rounds"] != 1 {
		t.Errorf("locate_rounds = %v, want 1", d["locate_rounds"])
	}
}

// A turn the loop never looked at leaves no locate block: "Located in 0
// rounds, resumed" is the record of an absence.
func TestWithLocateDetail_isSilentWhenTheLoopNeverLooked(t *testing.T) {
	for _, skipped := range []string{"off", "resumed", "no sources"} {
		d := withLocateDetail(map[string]any{}, LocateReport{Skipped: skipped})
		if len(d) != 0 {
			t.Errorf("skipped %q: detail = %v, want nothing", skipped, d)
		}
	}
	d := withLocateDetail(map[string]any{}, LocateReport{Skipped: "call failed", Rounds: 1})
	if d["locate"] != "call failed" {
		t.Errorf("a failed round lost its reason: %v", d)
	}
}

func TestGather_neverFollowsAChunkItSkippedForTheCeiling(t *testing.T) {
	// The hit references a symbol defined only outside the ceiling. That
	// definition is skipped; what IT references must not be followed on the
	// next hop, or the turn gathers by a reason the answer never shows.
	db := gatherDB(t)
	seedRepo(t, db, "go-sqlite3")
	hitID := seedChunkIn(t, db, "peeq", "store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return ZeroBlob(p) }")
	seedChunkIn(t, db, "go-sqlite3", "blob.go", 0, 40, 60, "ZeroBlob",
		"func ZeroBlob(p string) error { return helperInPeeq(p) }")
	seedSymbolIn(t, db, "go-sqlite3", "blob.go", "ZeroBlob", 40)
	seedChunkIn(t, db, "peeq", "helper.go", 0, 1, 10, "helperInPeeq",
		"func helperInPeeq(p string) error { return nil }")
	seedSymbolIn(t, db, "peeq", "helper.go", "helperInPeeq", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 10000, NoCrossings: true}).
		within([]string{"peeq"}).Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "peeq", "helper.go") {
		t.Errorf("sources = %v, want nothing reached through a skipped chunk", repoPaths(got))
	}
}

func TestGather_neverFollowsACrossingItSkippedForTheCeiling(t *testing.T) {
	// A queue crossing lands outside the ceiling. It is skipped, and its
	// one far-side hop is not taken either.
	db := gatherDB(t)
	seedRepo(t, db, "queue-master")
	hitID := seedChunk(t, db, "send.go", 0, 1, 10, "send", `publish("shipping-task")`)
	seedTokenIn(t, db, "peeq", "send.go", "destination", "shipping-task", 1)
	seedChunkIn(t, db, "queue-master", "listen.go", 0, 1, 10, "listen",
		`func listen() { subscribe("shipping-task"); handleInPeeq() }`)
	seedTokenIn(t, db, "queue-master", "listen.go", "destination", "shipping-task", 1)
	seedChunkIn(t, db, "peeq", "handle.go", 0, 1, 10, "handleInPeeq", "func handleInPeeq() {}")
	seedSymbolIn(t, db, "peeq", "handle.go", "handleInPeeq", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		within([]string{"peeq"}).Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "queue-master", "listen.go") || hasIn(got, "peeq", "handle.go") {
		t.Errorf("sources = %v, want neither the skipped crossing nor its far-side hop", repoPaths(got))
	}
}
