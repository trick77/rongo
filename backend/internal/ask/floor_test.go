package ask

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// TestRankDropsCandidatesFarBehindTheLeader is the card the reader was shown:
// five candidates, the last of them a straggler that shared nothing with the
// question but a language. Grouping had no relevance floor at all — only the
// leader-versus-runner-up margin — so whatever the top five modules of the
// fused twenty were became the question put back to the reader.
func TestRankDropsCandidatesFarBehindTheLeader(t *testing.T) {
	// Given: two serious candidates and a straggler.
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/llm/client.go", Score: 1.0},
		{Repo: "loom", Path: "internal/httpapi/sse.go", Score: 0.8},
		{Repo: "rongo", Path: "internal/ui/i18n_de.go", Score: 0.2},
	})

	// Then: the straggler is not a thing anyone is asked to choose.
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	for _, c := range got.All {
		if c.Repo == "rongo" {
			t.Errorf("a candidate at a fifth of the leader's score reached the card: %+v", c)
		}
	}
	if len(got.All) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got.All), got.All)
	}
}

func TestRankKeepsTheLeaderHoweverWeakItIs(t *testing.T) {
	// Given: everything scored badly, which is not the same as nothing being
	// the best. A floor that can empty the list would turn a weak answer into
	// no answer.
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/llm/client.go", Score: 0.01},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 1 {
		t.Fatalf("got %d candidates, want the leader kept: %+v", len(got.All), got.All)
	}
}

// TestRankDropsAModuleThatIsOnlyTestCode is the "LLM Session Header Test"
// candidate. An analyst asking how a mechanism works is never choosing between
// a test and the code it exercises, and a developer who wants the test can ask
// for it — the hits are still gathered from, this only decides what may be
// offered as an alternative.
func TestRankDropsAModuleThatIsOnlyTestCode(t *testing.T) {
	// Given: a production module and a module that is nothing but tests, at
	// comparable scores.
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/llm/client.go", Score: 1.0},
		{Repo: "loom", Path: "internal/session/header_test.go", Score: 0.95},
		{Repo: "loom", Path: "internal/session/session_test.go", Score: 0.9},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 1 {
		t.Fatalf("got %d candidates, want the test-only module dropped: %+v", len(got.All), got.All)
	}
}

// TestRankDropsATestOnlyLeaderWhenProductionCodeSurvives pins the deliberate
// case: the best-scoring module is nothing but tests, and a production module
// is close behind. The card is about which mechanism was meant, so the test
// goes and the mechanism stands alone — which then dominates, and the turn
// answers instead of asking. The test's hits are still gathered from.
func TestRankDropsATestOnlyLeaderWhenProductionCodeSurvives(t *testing.T) {
	// Given
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/session/header_test.go", Score: 1.0},
		{Repo: "loom", Path: "internal/llm/client.go", Score: 0.9},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 1 || got.All[0].ModuleKey != "internal/llm" {
		t.Fatalf("want the production module alone, got %+v", got.All)
	}
}

// TestRankKeepsATestOnlyLeaderWhenNothingElseIsLeft is the other side of it:
// everything retrieval found is test material. That is still the best the
// corpus has, and someone asking how a thing is tested is entitled to it.
func TestRankKeepsATestOnlyLeaderWhenNothingElseIsLeft(t *testing.T) {
	// Given
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/session/header_test.go", Score: 1.0},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 1 {
		t.Fatalf("want the test module kept, got %+v", got.All)
	}
}

// TestRankStillAsksWhenEveryCandidateIsATest is the question the drop must not
// swallow: "wie wird das getestet?", answered by two repositories that test
// the same thing in two places. Falling back to the leader alone would make
// dominates() true by arithmetic — a list of one cannot be asked about — and
// the reader would silently get one of the two.
func TestRankStillAsksWhenEveryCandidateIsATest(t *testing.T) {
	// Given
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/httpapi/header_test.go", Score: 0.9},
		{Repo: "peeq", Path: "internal/httpapi/header_test.go", Score: 0.85},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 2 {
		t.Fatalf("want both test modules, got %+v", got.All)
	}
}

func TestRankKeepsAModuleThatMixesTestsAndCode(t *testing.T) {
	// Given: a module whose hits include a test alongside the code. Dropping
	// it would lose the mechanism to keep out its harness.
	r := newTestRouter(t, testLLM(t, func(prompt string) string { return `{"decision":"compose"}` }), testDBWithDeps(t, nil))

	// When
	got, err := r.Rank(context.Background(), []retrieve.Hit{
		{Repo: "loom", Path: "internal/llm/client.go", Score: 1.0},
		{Repo: "loom", Path: "internal/session/header.go", Score: 0.95},
		{Repo: "loom", Path: "internal/session/header_test.go", Score: 0.9},
	})

	// Then
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got.All) != 2 {
		t.Fatalf("got %d candidates, want both modules: %+v", len(got.All), got.All)
	}
}
