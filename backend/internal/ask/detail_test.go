package ask

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// TestPipeline_reportsWhatEachStepFound: beside the one-word status, every
// step hands the trace what it found — the understanding's terms, the hits
// per repository, the rung that decided, the sources by reason, the answer's
// usage — so the trace can say why a card came or where an answer stopped.
func TestPipeline_reportsWhatEachStepFound(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "queue-master")
	hitID := seedChunk(t, db, "send.go", 0, 1, 10, "send", `publish("shipping-task")`)
	seedTokenIn(t, db, "peeq", "send.go", "destination", "shipping-task", 1)
	seedChunkIn(t, db, "queue-master", "listen.go", 0, 1, 10, "listen", `subscribe("shipping-task")`)
	seedTokenIn(t, db, "queue-master", "listen.go", "destination", "shipping-task", 1)
	c := twoStepUpstream(t, appleTVReply, "So [1] and [2].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	details := map[string]map[string]any{}

	if _, _, err := p.Run(context.Background(), "How?", AudienceBA, LanguageEN, Thread{},
		Events{OnDetail: func(step string, d map[string]any) { details[step] = d }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, step := range []string{"understanding", "searching", "routing", "gathering", "writing"} {
		if details[step] == nil {
			t.Errorf("no detail for %q; got %v", step, details)
		}
	}
	if u := details["understanding"]; u != nil && u["code_terms"] == nil {
		t.Errorf("understanding detail lacks the guessed identifiers: %v", u)
	}
	if s := details["searching"]; s != nil && s["hits"] != 1 {
		t.Errorf("searching detail = %v, want one hit", s)
	}
	if r := details["routing"]; r != nil && r["decision"] != "answer" {
		t.Errorf("routing detail = %v, want an answer", r)
	}
	g := details["gathering"]
	if g == nil || g["crossings"] != 1 || g["hits"] != 1 {
		t.Fatalf("gathering detail = %v, want one hit and one crossing", g)
	}
	crossed, _ := g["crossed"].([]map[string]string)
	if len(crossed) != 1 || crossed[0]["from"] != "peeq" || crossed[0]["to"] != "queue-master" || crossed[0]["via"] != "destination shipping-task" {
		t.Errorf("crossed = %v, want peeq -> queue-master via the queue", g["crossed"])
	}
	if w := details["writing"]; w != nil && (w["sources"] != 2 || w["cited"] != 2) {
		t.Errorf("writing detail = %v, want two sources, two cited", w)
	}
}

// TestPipeline_reportsTheGapPassUnderTheGatheringStep: the pass is part of
// reading the code, not a step of its own, so what it asked for and what it
// found is reported where the walk's own numbers are — and a chunk fetched
// by name is counted apart from the references, which is what it would
// otherwise silently become.
func TestPipeline_reportsTheGapPassUnderTheGatheringStep(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() int { return unitPrice() * n }")
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { return 3 }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	// No symbol hop: what reaches price.go can only be the gap pass.
	g := NewGatherer(db, GatherOptions{MaxHops: 0, TokenBudget: 5000}).
		WithGapPass(gapLLM(t, missing(name("unitPrice", "symbol")), nil))
	p := NewPipeline(twoStepUpstream(t, appleTVReply, "So [1] and [2]."),
		&fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}}, g, &fakeRouter{})
	details := map[string]map[string]any{}

	if _, _, err := p.Run(context.Background(), "How is the total computed?", AudienceBA, LanguageEN, Thread{},
		Events{OnDetail: func(step string, d map[string]any) { details[step] = d }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	d := details["gathering"]
	if d == nil || d["gaps"] != 1 || d["references"] != 0 {
		t.Fatalf("gathering detail = %v, want one gap and no reference", d)
	}
	asked, _ := d["gap_asked"].([]string)
	if len(asked) != 1 || asked[0] != "unitPrice (symbol)" {
		t.Errorf("gap_asked = %v", d["gap_asked"])
	}
	landed, _ := d["gap_landed"].([]string)
	if len(landed) != 1 || landed[0] != "unitPrice" {
		t.Errorf("gap_landed = %v", d["gap_landed"])
	}
	if _, ok := d["gap_skipped"]; ok {
		t.Errorf("gap_skipped = %v on a pass that ran", d["gap_skipped"])
	}
}

// TestPipeline_saysNothingAboutAGapPassThatIsOff: the trace is stored per
// message, and a deployment without the pass must not record its absence on
// every turn.
func TestPipeline_saysNothingAboutAGapPassThatIsOff(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() {}")
	p := NewPipeline(twoStepUpstream(t, appleTVReply, "So [1]."),
		&fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	details := map[string]map[string]any{}

	if _, _, err := p.Run(context.Background(), "How?", AudienceBA, LanguageEN, Thread{},
		Events{OnDetail: func(step string, d map[string]any) { details[step] = d }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, k := range []string{"gaps", "gap_asked", "gap_landed", "gap_unresolved", "gap_refused", "gap_skipped"} {
		if _, ok := details["gathering"][k]; ok {
			t.Errorf("%s reported for a pass that is off: %v", k, details["gathering"])
		}
	}
}

// TestRouteCarriesTheRungItDecidedOn: the trace names the rung, not only the
// decision, so "answered directly: the question named the product" and
// "asked: hits span two products" are tellable apart.
func TestRouteCarriesTheRungItDecidedOn(t *testing.T) {
	r := newTestRouter(t, testLLM(t, func(string) string { return "" }), testDBWithDeps(t, nil))
	got, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
	}, []string{"peeq"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rung != rungNamedRepos || got.Repos != 1 {
		t.Errorf("decision = %+v, want the named-repository rung over one repository", got)
	}
}
