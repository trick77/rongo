package threads

import (
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestFinishWithSources_anAnswerIsNeverStoredWithoutItsBasis(t *testing.T) {
	// Given a turn whose sources cannot be written: the same chunk twice
	s, ctx, threadID, _ := newThreadStore(t)
	m, err := s.AddQuestion(ctx, threadID, "ba", "en", "How?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	// When
	err = s.FinishWithSources(ctx, m.ID, "So [1].",
		[]ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 2, SHA: "0123abc"}},
		[]ask.Source{{ChunkID: 7, Reason: "hit"}, {ChunkID: 7, Reason: "hit"}}, nil)

	// Then nothing of it landed: an answer with no basis reads as one whose
	// basis is no longer indexed, which is a different fact
	if err == nil {
		t.Fatal("FinishWithSources succeeded, want the source write to fail it")
	}
	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Answer != "" || len(msgs[0].Citations) != 0 {
		t.Errorf("message = %+v, want neither answer nor citations", msgs)
	}
}

func TestFinishWithSources_storesAnswerCitationsAndBasisTogether(t *testing.T) {
	// Given
	s, ctx, threadID, db := newThreadStore(t)
	m, _ := s.AddQuestion(ctx, threadID, "ba", "en", "How?", 0)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")

	// When
	err := s.FinishWithSources(ctx, m.ID, "So [1].",
		[]ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 1, SHA: "0123abc"}},
		[]ask.Source{{ChunkID: 1, Reason: "hit"}}, nil)

	// Then
	if err != nil {
		t.Fatalf("FinishWithSources: %v", err)
	}
	msgs, _ := s.Messages(ctx, testSubject, threadID)
	if len(msgs) != 1 || msgs[0].Answer != "So [1]." || len(msgs[0].Citations) != 1 {
		t.Fatalf("message = %+v", msgs)
	}
	refs, err := s.SourceRefs(ctx, testSubject, m.ID)
	if err != nil || len(refs) != 1 {
		t.Errorf("refs = %+v, %v, want the one source", refs, err)
	}
}

// A resumed turn's answer and the link closing its card are one write. Apart,
// a link that did not land left an answered row the card knew nothing about,
// and the next click wrote a second answer under it.
func TestFinishWithSources_closesTheCardInTheSameTransaction(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	card, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	clarID, err := s.Clarify(ctx, card.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	m, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", card.ID)

	err = s.FinishWithSources(ctx, m.ID, "So.", nil, []ask.Source{{ChunkID: 1, Reason: "hit"}},
		&Choice{Subject: testSubject, ClarificationID: clarID, Idx: 1})

	if err != nil {
		t.Fatalf("FinishWithSources: %v", err)
	}
	clar, err := s.Clarification(ctx, testSubject, card.ID)
	if err != nil || clar == nil || !clar.Answered {
		t.Fatalf("clarification = %+v (%v), want it closed by the answer", clar, err)
	}
	got, _, _ := s.Message(ctx, testSubject, m.ID)
	if got.FromCandidateIdx != 1 || got.Answer != "So." {
		t.Errorf("message = %+v, want the answer and the choice it came from", got)
	}
}

func TestFinishWithSources_aLinkThatCannotLandStoresNoAnswer(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	card, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	clarID, err := s.Clarify(ctx, card.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	m, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", card.ID)

	// Someone else's subject: the link matches no row.
	err = s.FinishWithSources(ctx, m.ID, "So.", nil, []ask.Source{{ChunkID: 1, Reason: "hit"}},
		&Choice{Subject: "someone-else", ClarificationID: clarID, Idx: 1})

	if err == nil {
		t.Fatal("want the unlinkable finish to fail")
	}
	clar, _ := s.Clarification(ctx, testSubject, card.ID)
	if clar == nil || clar.Answered {
		t.Errorf("clarification = %+v, want it still open for a retry", clar)
	}
	got, _, _ := s.Message(ctx, testSubject, m.ID)
	if got.Answer != "" {
		t.Errorf("answer = %q, want nothing landed", got.Answer)
	}
	if refs, _ := s.SourceRefs(ctx, testSubject, m.ID); len(refs) != 0 {
		t.Errorf("refs = %+v, want no basis landed", refs)
	}
}
