package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

// TestAsk_aFollowUpCarriesThePreviousAnswersSources: a rework answers from
// the previous turn's own basis, so the handler reads it with the previous
// question and answer, whole or not — the count says which.
func TestAsk_aFollowUpCarriesThePreviousAnswersSources(t *testing.T) {
	db := askDB(t)
	chunkID := seedChunk(t, db)
	// The first turn claims two sources; only one of them is a chunk the
	// index holds, the other stands for one a re-index has since removed.
	a := &fakeAsker{tokens: []string{"x"}, sources: []ask.Source{{ChunkID: chunkID, Reason: "hit"}, {ChunkID: chunkID + 1000, Reason: "hit"}}}
	deps := Deps{Auth: auth.NewService(db, "dev", ""), Ask: a, Threads: threads.NewStore(db)}
	postAsk(t, deps, `{"question":"How does peeq issue grants?","audience":"ba"}`)

	list, err := deps.Threads.List(context.Background(), testSubject)
	if err != nil || len(list) != 1 {
		t.Fatalf("list threads: %v (%d)", err, len(list))
	}
	postAsk(t, deps, fmt.Sprintf(`{"question":"summarize","audience":"ba","thread_id":%q}`, list[0].PublicID))

	if len(a.gotThread.Sources) != 1 || a.gotThread.Sources[0].ChunkID != chunkID {
		t.Errorf("sources = %+v, want the one chunk the index still holds", a.gotThread.Sources)
	}
	if a.gotThread.SourcesTotal != 2 {
		t.Errorf("sources total = %d, want the two the record holds", a.gotThread.SourcesTotal)
	}
}

// TestAsk_aReworkWhoseBasisIsGoneSaysSo: its own line, not the generic one,
// and never the pipeline's text.
func TestAsk_aReworkWhoseBasisIsGoneSaysSo(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{err: fmt.Errorf("%w (1 of 2 left)", ask.ErrBasisGone)})

	body := postAsk(t, deps, `{"question":"summarize","audience":"ba"}`).Body.String()

	if !strings.Contains(body, "event: error") || !strings.Contains(body, reworkBasisGone) {
		t.Fatalf("stream did not say the basis is gone: %s", body)
	}
	if strings.Contains(body, turnFailed) || strings.Contains(body, "1 of 2 left") {
		t.Errorf("stream carried the generic line or the raw error: %s", body)
	}
	list, _ := st.List(context.Background(), testSubject)
	msgs, _ := st.Messages(context.Background(), testSubject, list[0].ID)
	if len(msgs) != 1 || msgs[0].Error != reworkBasisGone {
		t.Fatalf("messages = %+v, want the turn recorded with the basis line", msgs)
	}
}

// TestReexplainOfAReworkRowWithNoAntecedentSaysTheBasisIsGone: a rework
// row whose turn below no longer answers anything (never happens through
// the product; a record edited by hand) has nothing to rework again.
func TestReexplainOfAReworkRowWithNoAntecedentSaysTheBasisIsGone(t *testing.T) {
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	ctx := context.Background()
	chunkID := seedChunk(t, db)
	th, err := store.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	rework, err := store.AddQuestion(ctx, th.ID, "ba", "en", "summarize", 0)
	if err != nil {
		t.Fatalf("add rework: %v", err)
	}
	if err := store.Finish(ctx, rework.ID, "Short.", nil); err != nil {
		t.Fatalf("finish rework: %v", err)
	}
	if err := store.SetScope(ctx, rework.ID, ask.Scope{Intent: ask.IntentRework}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	if err := store.SaveSources(ctx, rework.ID, []ask.Source{{ChunkID: chunkID, Reason: "hit"}}); err != nil {
		t.Fatalf("save rework sources: %v", err)
	}

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", rework.ID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: error") || !strings.Contains(body, reworkBasisGone) {
		t.Fatalf("want the basis line, got:\n%s", body)
	}
}

// TestReexplainOfAReworkRowReworksAgain: the audience toggle on a
// "summarize" row must not answer "summarize" afresh over the sources. It
// reworks the same antecedent again, for the other audience.
func TestReexplainOfAReworkRowReworksAgain(t *testing.T) {
	var asker *fakeAsker
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining(), func(f *fakeAsker) { asker = f })
	ctx := context.Background()
	chunkID := seedChunk(t, db)
	th, err := store.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	first, err := store.AddQuestion(ctx, th.ID, "ba", "en", "How are grants issued?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := store.Finish(ctx, first.ID, "By NewGrant [1].", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := store.SaveSources(ctx, first.ID, []ask.Source{{ChunkID: chunkID, Reason: "hit"}}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	rework, err := store.AddQuestion(ctx, th.ID, "ba", "en", "summarize", 0)
	if err != nil {
		t.Fatalf("add rework: %v", err)
	}
	if err := store.Finish(ctx, rework.ID, "Short: NewGrant [1].", nil); err != nil {
		t.Fatalf("finish rework: %v", err)
	}
	if err := store.SetScope(ctx, rework.ID, ask.Scope{Intent: ask.IntentRework}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	if err := store.SaveSources(ctx, rework.ID, []ask.Source{{ChunkID: chunkID, Reason: "hit"}}); err != nil {
		t.Fatalf("save rework sources: %v", err)
	}

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", rework.ID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: token") {
		t.Fatalf("want a streamed answer:\n%s", body)
	}
	if !asker.reworked || asker.reworkInstruction != "summarize" {
		t.Fatalf("reworked=%v instruction=%q, want the row reworked again with its own instruction", asker.reworked, asker.reworkInstruction)
	}
	if asker.gotThread.Question != "How are grants issued?" || asker.gotThread.Answer != "By NewGrant [1]." {
		t.Errorf("antecedent = %+v, want the turn below the rework", asker.gotThread)
	}
	if len(asker.gotThread.Sources) != 1 || asker.gotThread.SourcesTotal != 1 {
		t.Errorf("sources = %+v (of %d), want the row's own", asker.gotThread.Sources, asker.gotThread.SourcesTotal)
	}
}
