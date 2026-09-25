package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// The trace tells a reader what the loop looked up and what came of it, from
// facts the loop held. The model's conclusion is a pointer for the answer
// prompt, never trace text: it is prose in the question's language making
// claims about code nothing cites.

// locateConverterFixture is the grep-then-conclude case, with the conclusion
// the second round returns.
func locateConverterFixture(t *testing.T, conclusion string) (*Gatherer, []Source) {
	t.Helper()
	db := gatherDB(t)
	namingID := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	converterID := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, converterID)},
	}}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: conclusion},
		}, nil), searcher)
	return g, []Source{sourceOf(t, db, namingID)}
}

func TestLocate_reportsEachLookupAsAStep(t *testing.T) {
	g, sources := locateConverterFixture(t, "FOUND: peeq ConverterPetRegistry.java:152 setzt den Wert.")
	_, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	want := []LocateStep{{Tool: "grep", Arg: "setAnzahlhaustiere", Matches: 1}}
	if len(report.Steps) != 1 || report.Steps[0].Tool != want[0].Tool ||
		report.Steps[0].Arg != want[0].Arg || report.Steps[0].Matches != 1 || report.Steps[0].NotRun != "" {
		t.Errorf("Steps = %+v, want %+v", report.Steps, want)
	}
}

func TestLocate_aResolvedConclusionIsThePlaceTheAnswerStartsFrom(t *testing.T) {
	g, sources := locateConverterFixture(t, "FOUND: peeq ConverterPetRegistry.java:152 setzt den Wert.")
	got, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Outcome != LocatePointed {
		t.Errorf("Outcome = %q, want %q", report.Outcome, LocatePointed)
	}
	if report.Place == nil || *report.Place != (LocatePlace{Repo: "peeq", Path: "ConverterPetRegistry.java", Line: 152}) {
		t.Errorf("Place = %+v, want peeq ConverterPetRegistry.java:152", report.Place)
	}
	if got[0].Path != "ConverterPetRegistry.java" {
		t.Errorf("first source = %s, want the place the trace names", got[0].Path)
	}
}

func TestLocate_notFoundIsAnOutcomeNotAPlace(t *testing.T) {
	g, sources := locateConverterFixture(t, "NOT FOUND: die Stelle ist nicht im Index.")
	_, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Outcome != LocateNotFound || report.Place != nil {
		t.Errorf("Outcome = %q, Place = %+v, want not found and no place", report.Outcome, report.Place)
	}
}

func TestLocate_aPlaceNoSourceHoldsLeavesTheOrderAndSaysSo(t *testing.T) {
	g, sources := locateConverterFixture(t, "FOUND: peeq Elsewhere.java:9 does it.")
	_, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Outcome != LocateUnpinned || report.Place != nil {
		t.Errorf("Outcome = %q, Place = %+v, want unpinned and no place", report.Outcome, report.Place)
	}
}

func TestLocate_aCallNotRunSaysWhyInPlainWords(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	id := seedChunkIn(t, db, "peeq", "HouseholdEntity.java", 0, 1, 10, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{
				call("c1", "grep", `{"pattern":"setAnzahlhaustiere","repo":"loom"}`),
				call("c2", "grep", `{"pattern":`),
			}},
			{content: "NOT FOUND: nichts."},
		}, nil), locateSearcher{seeds: map[string][]retrieve.Hit{}})

	_, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, []string{"peeq"}, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(report.Steps) != 2 {
		t.Fatalf("Steps = %+v, want both calls", report.Steps)
	}
	if s := report.Steps[0]; s.Repo != "loom" || !strings.Contains(s.NotRun, "outside") {
		t.Errorf("step 1 = %+v, want loom refused as outside this turn", s)
	}
	if s := report.Steps[1]; !strings.Contains(s.NotRun, "malformed") {
		t.Errorf("step 2 = %+v, want a malformed call", s)
	}
	// Nothing ran, so no conclusion is kept and the trace claims none.
	if report.Outcome != "" {
		t.Errorf("Outcome = %q, want none: no lookup ran", report.Outcome)
	}
}

func TestWithLocateDetail_sendsStepsAndPlaceNeverTheModelsSentence(t *testing.T) {
	d := withLocateDetail(map[string]any{}, LocateReport{
		Rounds: 1, Calls: []string{"grep(setAnzahlhaustiere) 1 lines"},
		Steps:   []LocateStep{{Tool: "grep", Arg: "setAnzahlhaustiere", Matches: 1}},
		Found:   "FOUND: peeq ConverterPetRegistry.java:152 setzt den Wert.",
		Outcome: LocatePointed,
		Place:   &LocatePlace{Repo: "peeq", Path: "ConverterPetRegistry.java", Line: 152},
	})
	for _, k := range []string{"locate_found", "locate_calls", "locate_landed", "locate_empty", "locate_refused"} {
		if _, ok := d[k]; ok {
			t.Errorf("detail carries %s; the trace reads steps and the outcome", k)
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "setzt den Wert") {
		t.Errorf("detail %s carries the model's prose", raw)
	}
	for _, want := range []string{
		`"locate_steps":[{"tool":"grep","arg":"setAnzahlhaustiere","matches":1}]`,
		`"locate_outcome":"pointed"`,
		`"locate_place":{"repo":"peeq","path":"ConverterPetRegistry.java","line":152}`,
		`"locate_rounds":1`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("detail %s lacks %s", raw, want)
		}
	}
}

func TestLocate_aLookupLandingOnlyOutsideTheTurnSaysSo(t *testing.T) {
	// take skips a landing outside the ceiling without counting it, so a
	// search whose every hit lay outside would read "nothing" in the trace
	// while the model was shown those excerpts.
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	id := seedChunkIn(t, db, "peeq", "HouseholdEntity.java", 0, 1, 10, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	outside := seedChunkIn(t, db, "loom", "Pets.java", 0, 1, 10, "pets", "class Pets {}")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "search", `{"query":"pets"}`)}},
			{content: "NOT FOUND: nichts."},
		}, nil), locateSearcher{search: map[string][]retrieve.Hit{"pets": {hitInFor(t, db, outside)}}}).
		within([]string{"peeq"})

	_, report, err := g.Locate(context.Background(), "wo?", []Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(report.Steps) != 1 || report.Steps[0].Outside != 1 || report.Steps[0].Added != 0 {
		t.Errorf("Steps = %+v, want one search with its one hit counted as outside", report.Steps)
	}
}

func TestLocate_callsOverTheLimitFollowTheCallsNotTried(t *testing.T) {
	// The reserve runs out on the first call of a round that also went over
	// the call limit: the round's own calls come first, the ones past the
	// limit last.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 1, 10, "anzahlHaustiere", "private Integer anzahlHaustiere;")
	big := seedChunk(t, db, "Big.java", 0, 1, 400, "big", strings.Repeat("token ", 4000))
	calls := make([]llm.ToolCall, 0, locateMaxCalls+1)
	for i := 0; i <= locateMaxCalls; i++ {
		calls = append(calls, call(fmt.Sprintf("c%d", i), "search", fmt.Sprintf(`{"query":"q%d"}`, i)))
	}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 200}).
		WithLocateLoop(locateLLM(t, []locateRound{{calls: calls}, {content: "NOT FOUND: nichts."}}, nil),
			locateSearcher{search: map[string][]retrieve.Hit{"q0": {hitInFor(t, db, big)}}})

	_, report, err := g.Locate(context.Background(), "wo?", []Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	var reasons []string
	for _, s := range report.Steps {
		reasons = append(reasons, s.Arg+":"+s.NotRun)
	}
	last := report.Steps[len(report.Steps)-1]
	if last.Arg != fmt.Sprintf("q%d", locateMaxCalls) || !strings.Contains(last.NotRun, "limit") {
		t.Errorf("steps = %v, want the call past the limit last", reasons)
	}
	if !report.Steps[0].Cut || !strings.Contains(report.Steps[1].NotRun, "budget") {
		t.Errorf("steps = %v, want the cut call first and the ones after it not tried", reasons)
	}
}
