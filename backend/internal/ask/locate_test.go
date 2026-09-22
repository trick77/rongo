package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// The fixtures here are the case the loop was built for, spelled the way the
// estate spells it: a German question about "Anzahl Haustiere" whose answer is a
// converter calling setAnzahlhaustiere, a name FTS5 indexes inside one token and
// a keyword lane therefore cannot reach. Placeholder identifiers would test
// that a loop loops; these test that it reaches the place, and a cap once
// shipped green with stems called alpha, bravo and charlie while the feature
// did not work on its own case.

// locateRound is one scripted upstream turn: the tool calls to answer with,
// or content when the model is done.
type locateRound struct {
	content string
	calls   []llm.ToolCall
	// finish overrides the finish reason, so a round can be cut at the cap.
	finish string
}

// locateLLM answers each round in order and records how many it was asked
// for. A round past the end answers "done", so a loop that runs longer than
// the script says is visible as a round count, not a hang.
func locateLLM(t *testing.T, rounds []locateRound, asked *int) *llm.Client {
	t.Helper()
	return locateLLMSeeing(t, rounds, asked, nil)
}

// locateLLMSeeing is locateLLM that also records the tool results each
// request carried, so a test can check what the model was TOLD.
func locateLLMSeeing(t *testing.T, rounds []locateRound, asked *int, told *[]string) *llm.Client {
	t.Helper()
	// The fake advances its own round counter. It must not depend on the
	// caller passing one: with a nil counter an earlier version replayed
	// round one forever, and the test asserting that the SECOND query differs
	// from the first failed against a loop that was working correctly.
	seen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if told != nil {
			*told = (*told)[:0]
			for _, m := range req.Messages {
				if m.Role == "tool" {
					*told = append(*told, m.Content)
				}
			}
		}
		i := seen
		seen++
		if asked != nil {
			*asked = seen
		}
		msg := map[string]any{"content": "done"}
		finish := "stop"
		if i < len(rounds) {
			msg["content"] = rounds[i].content
			if calls := rounds[i].calls; len(calls) > 0 {
				wire := make([]any, 0, len(calls))
				for n, c := range calls {
					wire = append(wire, map[string]any{
						"id": c.ID, "type": "function", "index": n,
						"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
					})
				}
				msg["tool_calls"] = wire
				finish = "tool_calls"
			}
			if rounds[i].finish != "" {
				finish = rounds[i].finish
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": finish}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

// locateSearcher is a Searcher whose grep lane answers from a fixed table of
// pattern to hits, so a test can say what the substring scan sees.
type locateSearcher struct {
	seeds  map[string][]retrieve.Hit
	search map[string][]retrieve.Hit
	// asked records every pattern the scan was given, in order: the loop's
	// whole point is that the SECOND query differs from the first.
	asked *[]string
	// repos records the repository restriction each call ran under, so a
	// test can check the ceiling reached the lookup.
	repos *[]string
}

func (s locateSearcher) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	return s.search[q.Text], nil
}

// SeedHits is NOT what the grep tool calls, and answering from the same table
// is what hid a real defect: the tool used to call SeedHits, whose accessor
// derivation, length floor and file ceiling would have returned nothing for a
// literal the model had spelled correctly. A fake keyed on the query text
// made that invisible, because it is the one thing the real SeedHits does not
// do. Left returning nothing, so a tool wired to it fails its test.
func (s locateSearcher) SeedHits(_ context.Context, _ retrieve.Query) ([]retrieve.Hit, error) {
	return nil, nil
}

// Substring is the raw scan the grep tool calls: the literal term, nothing
// derived from it.
func (s locateSearcher) Substring(_ context.Context, term string, _ int, repos []string, _ string, _ retrieve.StagePrefixes) ([]retrieve.Hit, error) {
	if s.asked != nil {
		*s.asked = append(*s.asked, term)
	}
	if s.repos != nil {
		*s.repos = append(*s.repos, repos...)
	}
	return s.seeds[term], nil
}

func (s locateSearcher) ResolveRepos(_ context.Context, want []string, _ string) ([]string, []string, error) {
	return want, nil, nil
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: args}
}

func TestLocate_isOffWithoutAClient(t *testing.T) {
	db := gatherDB(t)
	id := seedChunk(t, db, "Converter.java", 0, 1, 10, "toHouseholdType", "class X {}")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})
	sources := []Source{sourceOf(t, db, id)}

	got, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Skipped != "off" {
		t.Errorf("Skipped = %q, want off", report.Skipped)
	}
	if len(got) != 1 {
		t.Errorf("sources = %d, want the ones it was given", len(got))
	}
}

func TestLocate_nothingGatheredIsLeftAlone(t *testing.T) {
	// "Nothing found" is a true answer and the loop is not the place to
	// overturn it.
	db := gatherDB(t)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, nil, nil), locateSearcher{})

	got, report, err := g.Locate(context.Background(), "wo?", nil, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Skipped != "no sources" {
		t.Errorf("Skipped = %q, want 'no sources'", report.Skipped)
	}
	if len(got) != 0 {
		t.Errorf("sources = %d, want none", len(got))
	}
}

func TestLocate_grepLandsTheConverterTheRankedLanesMissed(t *testing.T) {
	// The case in one test: the gathered sources NAME the field, the place
	// that sets it is reachable only by substring, and the loop asks for it.
	db := gatherDB(t)
	namingID := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"@Column(name = \"anzahl_haustiere\") private Integer anzahlHaustiere;")
	converterID := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")

	var asked []string
	searcher := locateSearcher{
		seeds: map[string][]retrieve.Hit{"setAnzahlhaustiere": {hitInFor(t, db, converterID)}},
		asked: &asked,
	}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "Gefunden im Converter."},
		}, nil), searcher)
	sources := []Source{sourceOf(t, db, namingID)}

	got, report, err := g.Locate(context.Background(),
		"Wie wird die Anzahl Haustiere an Ledger uebermittelt?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}

	s, ok := sourceIn(got, "peeq", "ConverterPetRegistry.java")
	if !ok {
		t.Fatalf("sources = %v, want the converter the loop looked for", repoPaths(got))
	}
	// The reason says how it arrived and carries the index's spelling, never
	// the model's prose: reachedVia renders it for the answer prompt.
	if s.Reason != "locate:grep setAnzahlhaustiere" {
		t.Errorf("reason = %q, want the tool and the pattern", s.Reason)
	}
	// Never hop 0: hitRepos reads hop 0 to say which repositories the
	// question's own search reached, and a loop landing is not that.
	if s.Hop != 3 {
		t.Errorf("hop = %d, want MaxHops+2", s.Hop)
	}
	if len(report.Landed) != 1 || !strings.Contains(report.Landed[0], "setAnzahlhaustiere") {
		t.Errorf("Landed = %v, want the grep that found it", report.Landed)
	}
}

func TestLocate_anEmptyCallIsReportedSoTheNextQueryCanDiffer(t *testing.T) {
	// The move a ranked pass cannot make: a scan returning NOTHING is a fact,
	// and the next query is spelled differently because of it. This is the
	// behaviour the whole loop exists for, so it is asserted on the calls the
	// scan actually received, not on the model's prose.
	db := gatherDB(t)
	namingID := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	converterID := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")

	var asked []string
	searcher := locateSearcher{
		// The first guess is the German business spelling and finds nothing;
		// the accessor spelling finds the converter.
		seeds: map[string][]retrieve.Hit{"setAnzahlHaustiere": {}, "setAnzahlhaustiere": {hitInFor(t, db, converterID)}},
		asked: &asked,
	}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlHaustiere"}`)}},
			{calls: []llm.ToolCall{call("c2", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "Gefunden."},
		}, nil), searcher)
	sources := []Source{sourceOf(t, db, namingID)}

	got, report, err := g.Locate(context.Background(),
		"Wie wird die Anzahl Haustiere an Ledger uebermittelt?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}

	if len(asked) != 2 || asked[0] != "setAnzahlHaustiere" || asked[1] != "setAnzahlhaustiere" {
		t.Fatalf("the scan was asked %v, want the failed spelling then the corrected one", asked)
	}
	if len(report.Empty) != 1 || !strings.Contains(report.Empty[0], "setAnzahlHaustiere") {
		t.Errorf("Empty = %v, want the call that found nothing", report.Empty)
	}
	if _, ok := sourceIn(got, "peeq", "ConverterPetRegistry.java"); !ok {
		t.Errorf("sources = %v, want the converter the second spelling found", repoPaths(got))
	}
}

func TestLocate_stopsAtTheRoundCeiling(t *testing.T) {
	// A model that keeps asking is bounded by rounds, not by calls.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere", "private Integer anzahlHaustiere;")
	asked := 0
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{}}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"a"}`)}},
			{calls: []llm.ToolCall{call("c2", "grep", `{"pattern":"b"}`)}},
			{calls: []llm.ToolCall{call("c3", "grep", `{"pattern":"c"}`)}},
			{calls: []llm.ToolCall{call("c4", "grep", `{"pattern":"d"}`)}},
		}, &asked), searcher)

	_, report, err := g.Locate(context.Background(), "wo?", []Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	// The looking rounds, plus the one closing call that asks what was found.
	// The conclusion is real spend and is counted: a trace reporting fewer
	// calls than the meter was billed for is a record disagreeing with the
	// bill.
	if want := locateMaxRounds + 1; asked != want {
		t.Errorf("asked the model %d times, want %d looking rounds plus the conclusion", asked, want)
	}
	if report.Rounds != locateMaxRounds {
		t.Errorf("Rounds = %d, want %d", report.Rounds, locateMaxRounds)
	}
}

func TestLocate_aFailedRoundKeepsWhatLandedAndNeverFailsTheTurn(t *testing.T) {
	// Same contract as the gap pass: every question was answered before this
	// loop existed, so it may never do worse than nothing.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere", "private Integer anzahlHaustiere;")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(gapLLMFailing(t), locateSearcher{})
	g.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	sources := []Source{sourceOf(t, db, id)}

	got, report, err := g.Locate(context.Background(), "wo?", sources, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate must not fail the turn: %v", err)
	}
	if report.Skipped != "call failed" {
		t.Errorf("Skipped = %q, want 'call failed'", report.Skipped)
	}
	if len(got) != 1 {
		t.Errorf("sources = %v, want the ones it was given", repoPaths(got))
	}
}

func TestLocate_aToolItCannotParseIsNothingFoundNotAFailure(t *testing.T) {
	// The model writes the arguments; a malformed one is a call that found
	// nothing, which is exactly what it is told to act on.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere", "private Integer anzahlHaustiere;")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":`)}},
			{content: "Nichts gefunden."},
		}, nil), locateSearcher{})

	_, report, err := g.Locate(context.Background(), "wo?", []Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(report.Empty) != 1 || !strings.Contains(report.Empty[0], "unparseable") {
		t.Errorf("Empty = %v, want the unparseable call reported", report.Empty)
	}
}

func TestLocate_refusesTheCallsAfterTheOneTheReserveRanOutOn(t *testing.T) {
	// Three calls in ONE turn, the first finding nothing: that is what makes
	// a refusal count drift. Calls that find nothing go to Empty, not Landed,
	// so indexing the labels by len(Landed) reports whichever ones the offset
	// happens to reach — and with enough empty calls, slices out of range.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	big := seedChunk(t, db, "ConverterPetRegistry.java", 0, 1, 400, "toHouseholdType",
		strings.Repeat("wsHousehold.setAnzahlhaustiere(household.getAnzahlHaustiere()); ", 400))

	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"nichtVorhanden":     {},
		"setAnzahlhaustiere": {hitInFor(t, db, big)},
		"getAnzahlHaustiere": {hitInFor(t, db, big)},
	}}
	// A budget with almost no room past the source it is given, so the second
	// call's landing cannot be admitted.
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 120}).
		WithLocateLoop(locateLLM(t, []locateRound{{calls: []llm.ToolCall{
			call("c1", "grep", `{"pattern":"nichtVorhanden"}`),
			call("c2", "grep", `{"pattern":"setAnzahlhaustiere"}`),
			call("c3", "grep", `{"pattern":"getAnzahlHaustiere"}`),
		}}}, nil), searcher)

	_, report, err := g.Locate(context.Background(), "wo?", []Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(report.Empty) != 1 || !strings.Contains(report.Empty[0], "nichtVorhanden") {
		t.Fatalf("Empty = %v, want the first call", report.Empty)
	}
	// The reserve ran out on c2, so only c3 is refused — never c1, which was
	// tried and found nothing.
	if len(report.Refused) != 1 {
		t.Fatalf("Refused = %v, want exactly the one call after the refusal", report.Refused)
	}
	for _, r := range report.Refused {
		if strings.Contains(r, "nichtVorhanden") {
			t.Errorf("Refused = %v, must not name a call that was tried", report.Refused)
		}
	}
}

func TestLocate_staysOutOfAResumedTurn(t *testing.T) {
	// A module card replays exactly the hits its candidate was built from and
	// searches for nothing more, because the answer has to be built from what
	// the reader was offered. A loop looking past that would cite code the
	// card never showed.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	var asked []string
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
		}, nil), locateSearcher{seeds: map[string][]retrieve.Hit{}, asked: &asked})

	got, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, true, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Skipped != "resumed" {
		t.Errorf("Skipped = %q, want resumed", report.Skipped)
	}
	if len(asked) != 0 {
		t.Errorf("the scan ran %v on a resumed turn, want nothing", asked)
	}
	if len(got) != 1 {
		t.Errorf("sources = %d, want exactly what the card replayed", len(got))
	}
}

func TestLocate_concludesEvenWhenEveryRoundWentOnTools(t *testing.T) {
	// The pointer the answer prompt carries is set when the model stops
	// asking for tools. At rounds=1 — the setting the config recommends — a
	// round that calls anything exits through the loop condition instead, so
	// without a closing call Found is empty exactly when the looking worked,
	// and the whole answerLocated block is dead code.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	converter := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")

	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, converter)},
	}}
	// Round one calls a tool; the script's second entry is the conclusion the
	// closing call collects.
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "ConverterPetRegistry.java:162 setzt den Wert via setAnzahlhaustiere."},
		}, nil), searcher).WithLocateRounds(1)

	_, report, err := g.Locate(context.Background(),
		"Wie wird die Anzahl Haustiere an Ledger uebermittelt?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if report.Found == "" {
		t.Fatal("Found is empty after a round spent on tools; the answer prompt gets no pointer")
	}
	if !strings.Contains(report.Found, "ConverterPetRegistry") {
		t.Errorf("Found = %q, want the place it read", report.Found)
	}
}

func TestLocate_honoursTheTurnsRepositoriesAsACeiling(t *testing.T) {
	// A thread is a funnel: it narrows and never widens. The loop is the one
	// place where the MODEL picks what to look in, so a repository the turn
	// did not reach must be refused rather than searched — and refused OUT
	// LOUD, because silently searching everything is the quiet widening the
	// rule exists to stop.
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	id := seedChunkIn(t, db, "peeq", "HouseholdEntity.java", 0, 1, 10, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")

	var asked []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{}, asked: &asked}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere","repo":"loom"}`)}},
			{content: "fertig"},
		}, nil), searcher)

	_, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, []string{"peeq"}, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	// The scan must not have run at all: a repo outside the ceiling is
	// refused before the lookup, never widened into.
	if len(asked) != 0 {
		t.Errorf("the scan was asked %v, want nothing outside the turn's repositories", asked)
	}
	if len(report.Empty) != 1 || !strings.Contains(report.Empty[0], "outside") {
		t.Errorf("Empty = %v, want the call reported as outside the turn", report.Empty)
	}
}

func TestLocate_withNoRepoNamedTheToolSearchesTheTurnsOwn(t *testing.T) {
	// The other half: the model naming no repository must not mean "the whole
	// corpus". A pinned thread whose loop searched everything would admit and
	// cite a repository the reader excluded.
	db := gatherDB(t)
	id := seedChunkIn(t, db, "peeq", "HouseholdEntity.java", 0, 1, 10, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")

	var gotRepos []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{}, repos: &gotRepos}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "fertig"},
		}, nil), searcher)

	if _, _, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, []string{"peeq"}, false, nil); err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(gotRepos) != 1 || gotRepos[0] != "peeq" {
		t.Errorf("the scan ran over %v, want the turn's own repositories", gotRepos)
	}
}

func TestLocate_reservesItsShareOfTheBudgetFromTheWalk(t *testing.T) {
	// A fourth phase under one budget needs its own reserve, or it resolves
	// material it can never admit — the failure crossingReserve and
	// gapReserve were each written to prevent.
	db := gatherDB(t)
	off := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 24000})
	on := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 24000}).
		WithLocateLoop(locateLLM(t, nil, nil), locateSearcher{})

	if off.locate != nil {
		t.Fatal("a gatherer with no loop must have none")
	}
	if on.locate == nil || on.locateSearch == nil {
		t.Fatal("WithLocateLoop must set both the client and the searcher")
	}
	// The reserve is one part in eight of the default budget.
	if want := 24000 / locateReserve; want != 3000 {
		t.Errorf("locateReserve carves %d tokens, want 3000", want)
	}
}

func TestLocate_tellsTheModelWhyACallFoundNothing(t *testing.T) {
	// A call refused for its repository name, its arguments or its tool
	// found nothing for a reason that is not the spelling. "Try a different
	// spelling" would send the model away from a pattern that was right.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	var told []string
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLMSeeing(t, []locateRound{
			{calls: []llm.ToolCall{
				call("c1", "grep", `{"pattern":"setAnzahlhaustiere","repo":"nope"}`),
				call("c2", "grep", `{"pattern":`),
				call("c3", "fetch", `{}`),
				call("c4", "grep", `{"pattern":"setAnzahlHaustiere"}`),
			}},
			{content: "Nichts gefunden."},
		}, nil, &told), locateSearcher{seeds: map[string][]retrieve.Hit{}})

	if _, _, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil); err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(told) != 4 {
		t.Fatalf("tool results = %q, want one per call", told)
	}
	for i, want := range []string{"not in the index", "not valid JSON", "no tool", "Nothing found"} {
		if !strings.Contains(told[i], want) {
			t.Errorf("result %d = %q, want it to say %q", i+1, told[i], want)
		}
	}
	for i := range told[:3] {
		if strings.Contains(told[i], "spelling") {
			t.Errorf("result %d = %q, tells the model to respell a call that was refused", i+1, told[i])
		}
	}
}

func TestLocate_aRoundCutAtTheCapKeepsTheLandingsAndThePointer(t *testing.T) {
	// A round cut at the cap arrives with a FinishError. The rounds before it
	// landed code and read it, so the conclusion is still worth asking for:
	// dropping it loses the pointer on the turns that looked the longest.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	converter := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")
	var asked []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, converter)},
	}, asked: &asked}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{calls: []llm.ToolCall{call("c2", "grep", `{"pattern":"half`)}, finish: "length"},
			{content: "ConverterPetRegistry.java:162 setzt den Wert."},
		}, nil), searcher)
	g.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	got, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(asked) != 1 {
		t.Errorf("the scan was asked %v, want the cut round's half call left alone", asked)
	}
	if _, ok := sourceIn(got, "peeq", "ConverterPetRegistry.java"); !ok {
		t.Errorf("sources = %v, want round one's landing kept", repoPaths(got))
	}
	if !strings.Contains(report.Found, "ConverterPetRegistry") {
		t.Errorf("Found = %q, want the conclusion asked after the cut round", report.Found)
	}
	if report.Skipped != "call failed" {
		t.Errorf("Skipped = %q, want 'call failed'", report.Skipped)
	}
}

func TestLocate_searchAndReadLandAndEmptyArgumentsAreToldApart(t *testing.T) {
	// The other tools land like grep does, and a call with its one argument
	// left out is told that, not "nothing found": it looked up nothing.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")
	mapper := seedChunk(t, db, "HouseholdMapper.java", 0, 10, 30, "map",
		"target.setAnzahlHaustiere(source.getAnzahlHaustiere());")
	var told []string
	searcher := locateSearcher{search: map[string][]retrieve.Hit{
		"Anzahl Haustiere uebermitteln": {hitInFor(t, db, mapper)},
	}}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLMSeeing(t, []locateRound{
			{calls: []llm.ToolCall{
				call("c1", "search", `{"query":"Anzahl Haustiere uebermitteln"}`),
				call("c2", "read", `{"repo":"peeq","path":"ConverterPetRegistry.java","line":162}`),
				call("c3", "search", `{}`),
				call("c4", "grep", `{}`),
				call("c5", "symbol", `{}`),
				call("c6", "read", `{"repo":"peeq"}`),
			}},
			{content: "ConverterPetRegistry.java:162"},
		}, nil, &told), searcher)

	got, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	for _, path := range []string{"HouseholdMapper.java", "ConverterPetRegistry.java"} {
		s, ok := sourceIn(got, "peeq", path)
		if !ok {
			t.Errorf("sources = %v, want %s landed", repoPaths(got), path)
			continue
		}
		if !strings.HasPrefix(s.Reason, "locate:") {
			t.Errorf("%s arrived as %q, want a locate reason", path, s.Reason)
		}
	}
	if len(report.Landed) != 2 || len(report.Empty) != 4 {
		t.Errorf("Landed = %v, Empty = %v, want two landings and four empty calls", report.Landed, report.Empty)
	}
	if len(told) != 6 {
		t.Fatalf("tool results = %q, want one per call", told)
	}
	for i, r := range told[2:] {
		if !strings.Contains(r, "nothing was looked up") {
			t.Errorf("result %d = %q, want it to say nothing was looked up", i+3, r)
		}
	}
}

func TestLocate_symbolFiltersStageBeforeChoosingTheHomeRepository(t *testing.T) {
	// atHome keeps the first gathered repository that defines the name. If
	// that repository defines it only outside the asked stage, filtering
	// afterwards drops everything and the model is told "nothing found" for a
	// name the in-stage repository defines.
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	home := seedChunkIn(t, db, "peeq", "intg/Mapper.java", 0, 1, 10, "setAnzahlhaustiere",
		"void setAnzahlhaustiere(int n) {}")
	seedSymbolIn(t, db, "peeq", "intg/Mapper.java", "setAnzahlhaustiere", 1)
	seedChunkIn(t, db, "loom", "src/Converter.java", 0, 1, 10, "setAnzahlhaustiere",
		"void setAnzahlhaustiere(int n) {}")
	seedSymbolIn(t, db, "loom", "src/Converter.java", "setAnzahlhaustiere", 1)

	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "symbol", `{"name":"setAnzahlhaustiere"}`)}},
			{content: "fertig"},
		}, nil), locateSearcher{})

	got, _, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, home)}, []string{"peeq", "loom"}, false,
		retrieve.StagePrefixes{"peeq": "prod/"})
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if _, ok := sourceIn(got, "loom", "src/Converter.java"); !ok {
		t.Errorf("sources = %v, want the in-stage definer in loom", repoPaths(got))
	}
}

func TestLocate_aResumedTurnGathersWithoutTheLoopsReserve(t *testing.T) {
	// A module-card resume never runs the loop, so holding its share back
	// from the walk gathers less on exactly those turns and spends it on
	// nothing.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "hit.go", 0, 1, 10, "target", "func target() { helper() }")
	body := "func helper() { " + strings.Repeat("x ", 1380) + " }"
	seedChunk(t, db, "helper.go", 0, 1, 10, "helper", body)
	seedSymbol(t, db, "helper.go", "helper", 1)

	const budget = 800
	hit := estimateTokens("func target() { helper() }")
	if need := hit + estimateTokens(body); need <= budget-budget/locateReserve || need > budget {
		t.Fatalf("fixture needs %d tokens, want it between %d and %d", need, budget-budget/locateReserve, budget)
	}
	on := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: budget, NoCrossings: true}).
		WithLocateLoop(locateLLM(t, nil, nil), locateSearcher{})
	hits := []retrieve.Hit{hitFor(t, db, hitID)}

	fresh, err := on.forTurn(false).GatherSeeded(context.Background(), hits, nil, nil)
	if err != nil {
		t.Fatalf("GatherSeeded: %v", err)
	}
	if has(fresh, "helper.go") {
		t.Fatalf("sources = %v, want the reserve held back on a turn the loop runs on", paths(fresh))
	}
	resumed, err := on.forTurn(true).GatherSeeded(context.Background(), hits, nil, nil)
	if err != nil {
		t.Fatalf("GatherSeeded: %v", err)
	}
	if !has(resumed, "helper.go") {
		t.Errorf("sources = %v, want the whole budget on a resumed turn", paths(resumed))
	}
	if on.locate == nil {
		t.Error("forTurn changed the shared gatherer; the next fresh turn would run without the loop")
	}
}

func TestLocate_runsAtMostTheCallLimitPerTurn(t *testing.T) {
	// mimo-v2.6-flash answered one round with 38 calls. Running them all
	// would spend the reserve on one guess-storm; the rest are refused and
	// said so in the report.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	var calls []llm.ToolCall
	for i := 0; i < locateMaxCalls+4; i++ {
		calls = append(calls, call(fmt.Sprintf("c%d", i), "grep", fmt.Sprintf(`{"pattern":"p%d"}`, i)))
	}
	var asked, told []string
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLMSeeing(t, []locateRound{
			{calls: calls},
			{content: "fertig"},
		}, nil, &told), locateSearcher{seeds: map[string][]retrieve.Hit{}, asked: &asked})

	_, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(asked) != locateMaxCalls {
		t.Errorf("the scan ran %d times, want %d", len(asked), locateMaxCalls)
	}
	if want := fmt.Sprintf("at most %d tools per turn", locateMaxCalls); !strings.Contains(locateSystem, want) {
		t.Errorf("the prompt never tells the model the limit %q", want)
	}
	if len(report.Refused) != 4 {
		t.Errorf("Refused = %v, want the 4 calls over the limit", report.Refused)
	}
	// Every call the next request advertises has its result: only the calls
	// that ran are advertised.
	if len(told) != locateMaxCalls {
		t.Errorf("tool results = %d, want one per call that ran", len(told))
	}
}

func TestLocate_aTurnCutMidCallKeepsTheCompleteCalls(t *testing.T) {
	// A fan-out that runs into the output cap arrives with every call but
	// the last one whole. Throwing the turn away is the loop landing nothing
	// on the setting the config recommends.
	db := gatherDB(t)
	id := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere",
		"private Integer anzahlHaustiere;")
	converter := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);")
	var asked []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, converter)},
	}, asked: &asked}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{
				call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`),
				call("c2", "grep", `{"pattern":"anzahl`),
			}, finish: "length"},
			{content: "ConverterPetRegistry.java:162"},
		}, nil), searcher).WithLocateRounds(1)
	g.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	got, report, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, id)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(asked) != 1 || asked[0] != "setAnzahlhaustiere" {
		t.Errorf("the scan was asked %v, want the one complete call", asked)
	}
	if _, ok := sourceIn(got, "peeq", "ConverterPetRegistry.java"); !ok {
		t.Errorf("sources = %v, want the converter the complete call found", repoPaths(got))
	}
	if report.Skipped != "" {
		t.Errorf("Skipped = %q, want the round treated as a round", report.Skipped)
	}
	if !strings.Contains(report.Found, "ConverterPetRegistry") {
		t.Errorf("Found = %q, want the conclusion", report.Found)
	}
}

func TestLocate_grepShowsTheMatchingLineEvenWhenAlreadyGathered(t *testing.T) {
	// The chunk that answers is usually gathered already. Answering "nothing
	// new" hid the one line the model needed to read; it gets the line with
	// its number, the way a grep in a terminal would show it.
	db := gatherDB(t)
	converter := seedChunk(t, db, "ConverterPetRegistry.java", 0, 150, 170, "toHouseholdType",
		"void toHouseholdType() {\n  mapName();\n  Optional.ofNullable(household.getAnzahlHaustiere()).ifPresent(wsHousehold::setAnzahlhaustiere);\n}")
	var told []string
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, converter)},
	}}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLMSeeing(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "ConverterPetRegistry.java:152"},
		}, nil, &told), searcher).WithLocateRounds(1)

	if _, _, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, converter)}, nil, false, nil); err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(told) == 0 {
		t.Fatal("no tool result reached the model")
	}
	got := told[0]
	for _, want := range []string{"ConverterPetRegistry.java", "152:", "wsHousehold::setAnzahlhaustiere", "already among the sources"} {
		if !strings.Contains(got, want) {
			t.Errorf("tool result = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "mapName") {
		t.Errorf("tool result = %q, want only the matching line, not the whole chunk", got)
	}
}

func TestLocate_putsTheSourceThePointerNamesFirst(t *testing.T) {
	// The answer writes from sources in order. The place the loop read and
	// named goes to [1], so the answer's first source is the one that answers.
	db := gatherDB(t)
	entity := seedChunk(t, db, "HouseholdEntity.java", 0, 100, 120, "anzahlHaustiere", "private Integer anzahlHaustiere;")
	ui := seedChunk(t, db, "household.component.ts", 0, 1, 20, "form", "anzahlHaustiere: new FormControl()")
	first := seedChunk(t, db, "ConverterPetRegistry.java", 0, 100, 140, "fromHouseholdType", "void fromHouseholdType() {}")
	second := seedChunk(t, db, "ConverterPetRegistry.java", 1, 150, 170, "toHouseholdType",
		"ifPresent(wsHousehold::setAnzahlhaustiere);")
	searcher := locateSearcher{seeds: map[string][]retrieve.Hit{
		"setAnzahlhaustiere": {hitInFor(t, db, second)},
	}}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"setAnzahlhaustiere"}`)}},
			{content: "ConverterPetRegistry.java:150 calls wsHousehold::setAnzahlhaustiere."},
		}, nil), searcher).WithLocateRounds(1)

	got, _, err := g.Locate(context.Background(), "wo?", []Source{
		sourceOf(t, db, entity), sourceOf(t, db, ui), sourceOf(t, db, first), sourceOf(t, db, second),
	}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got[0].ChunkID != second {
		t.Errorf("first source = %s:%d, want the converter chunk holding line 150", got[0].Path, got[0].StartLine)
	}
	if len(got) != 4 {
		t.Errorf("sources = %d, want the same four, reordered", len(got))
	}
}

func TestLocate_aPointerNamingNoSourceLeavesTheOrderAlone(t *testing.T) {
	db := gatherDB(t)
	a := seedChunk(t, db, "A.java", 0, 1, 10, "a", "class A {}")
	b := seedChunk(t, db, "B.java", 0, 1, 10, "b", "class B {}")
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithLocateLoop(locateLLM(t, []locateRound{
			{calls: []llm.ToolCall{call("c1", "grep", `{"pattern":"x"}`)}},
			{content: "Not in the index."},
		}, nil), locateSearcher{seeds: map[string][]retrieve.Hit{}}).WithLocateRounds(1)

	got, _, err := g.Locate(context.Background(), "wo?",
		[]Source{sourceOf(t, db, a), sourceOf(t, db, b)}, nil, false, nil)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got[0].ChunkID != a || got[1].ChunkID != b {
		t.Errorf("order = %s, %s, want A then B unchanged", got[0].Path, got[1].Path)
	}
}
