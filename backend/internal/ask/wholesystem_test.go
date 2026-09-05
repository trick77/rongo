package ask

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// A question about a system as a whole has no module for an answer: every
// candidate is a part of it. The judge cannot see that — it is asked whether
// the CODE is ambiguous, and five parts of one product are five independent
// mechanisms by any reading. Nor can the role gate, which reads titles the
// naming call wrote from the question itself. This file pins the rung that
// settles it before either is paid for, from the reader's own words.

// wholeSystem is the understanding of "how does rongo differ from an app that
// only does RAG over the source": one repository named, and the question is
// about that system entire.
var wholeSystem = Asked{NamedRepos: 1, WholeSystem: true}

func TestDecideAnswersAQuestionAboutTheSystemAsAWhole(t *testing.T) {
	// Given candidates too close for the margin, a judge that said ask, and a
	// role gate that said the reader can choose — every later rung pointing
	// at a card.
	tight := []Candidate{{Repo: "rongo", Score: 1.0}, {Repo: "rongo", Score: 0.99}}

	// When the reader said they meant the system as a whole
	// Then the turn answers: the card would offer parts of the thing they
	// already named.
	if Decide(tight, 0.25, false, true, wholeSystem, true) {
		t.Error("a question about a system as a whole must be answered, not carded")
	}

	// Without that word from the reader, nothing changes.
	if !Decide(tight, 0.25, false, true, Asked{NamedRepos: 1}, true) {
		t.Error("the rung must fire on the reader's words alone, not on the shape of the candidates")
	}
}

func TestDecideKeepsTheRepositoryCardAboveTheWholeSystemRung(t *testing.T) {
	// Given candidates in two repositories and a question that named none.
	spread := []Candidate{{Repo: "peeq", Score: 1.0}, {Repo: "rongo", Score: 0.99}}

	// When the question is about a system as a whole — but never says which
	// system.
	// Then the repository card still stands: "as a whole" says the reader
	// meant no part, never which product they meant.
	if !Decide(spread, 0.25, false, false, Asked{WholeSystem: true}, true) {
		t.Error("a whole-system question naming no repository must still be asked which one")
	}
}

func TestRouteSkipsEveryModelRungForAWholeSystemQuestion(t *testing.T) {
	// Given a router whose model must not be called at all: not the judge,
	// not the naming, not the role gate.
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("the reader's own words settle this; no model may be asked. prompt: %q", prompt)
		return ""
	}), testDBWithDeps(t, nil))

	// When two candidates inside one repository sit too close for the margin,
	// and the reader asked about that repository as a whole.
	got, err := r.Route(context.Background(), "how does rongo differ from a RAG-only app?",
		AudienceBA, LanguageEN, []retrieve.Hit{
			{Repo: "rongo", Path: "backend/internal/retrieve/fuse.go", Score: 0.50},
			{Repo: "rongo", Path: "backend/internal/indexer/index.go", Score: 0.49},
		}, wholeSystem)

	// Then
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a question about the system as a whole must not produce a card")
	}
	if len(got.Candidates) != 2 {
		t.Errorf("got %d candidates, want 2 — the answer is composed from them", len(got.Candidates))
	}
}
