package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/threads"
)

func TestAsk_aQuestionOverTheByteCapIsRejectedBeforeAnythingIsRecorded(t *testing.T) {
	// Counted in bytes, the way the pipeline pays for it: 16385 umlauts are
	// 16385 characters and 32770 bytes.
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})
	q := strings.Repeat("ä", maxQuestionBytes/2+1)
	body, _ := json.Marshal(map[string]string{"question": q, "audience": "ba"})

	rec := postAsk(t, deps, string(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too long") {
		t.Errorf("body = %q, want the reason", rec.Body.String())
	}
	list, _ := st.List(context.Background(), "dev-user")
	if len(list) != 0 {
		t.Errorf("threads = %+v, want none created for an oversize question", list)
	}
}

func TestAsk_aQuestionAtTheByteCapIsAccepted(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})
	q := strings.Repeat("a", maxQuestionBytes)
	body, _ := json.Marshal(map[string]string{"question": q, "audience": "ba"})

	rec := postAsk(t, deps, string(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	list, _ := st.List(context.Background(), "dev-user")
	if len(list) != 1 {
		t.Errorf("threads = %+v, want one", list)
	}
}

func TestAsk_anEmptyPastedBlockIsRejected(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})

	rec := postAsk(t, deps, `{"question":"How?","audience":"ba","pasted_texts":[{"text":"  \n","lines":1}]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	list, _ := st.List(context.Background(), "dev-user")
	if len(list) != 0 {
		t.Errorf("threads = %+v, want none", list)
	}
}

func TestAsk_pastedTextsOverTheByteCapAreRejected(t *testing.T) {
	// The metadata rides beside the question, inside the same 1 MiB body, and
	// is stored on every row of the turn: it gets the question's own cap.
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})
	body := fmt.Sprintf(`{"question":"How?","audience":"ba","pasted_texts":[{"text":%q,"lines":1}]}`, strings.Repeat("a", maxQuestionBytes+1))

	rec := postAsk(t, deps, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	list, _ := st.List(context.Background(), "dev-user")
	if len(list) != 0 {
		t.Errorf("threads = %+v, want none", list)
	}
}

func TestAsk_storesThePastedTextsOnTheTurn(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})

	rec := postAsk(t, deps, `{"question":"Why?\n\npanic: boom\nmain.go:12","audience":"ba","pasted_texts":[{"text":"panic: boom\nmain.go:12","lines":2}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	list, _ := st.List(context.Background(), "dev-user")
	msgs, err := st.Messages(context.Background(), "dev-user", list[0].ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	want := threads.PastedText{Text: "panic: boom\nmain.go:12", Lines: 2}
	if got := msgs[0].PastedTexts; len(got) != 1 || got[0] != want {
		t.Errorf("PastedTexts = %+v, want %+v", got, want)
	}
	if msgs[0].Question != "Why?\n\npanic: boom\nmain.go:12" {
		t.Errorf("Question = %q, want the paste kept inline for the pipeline", msgs[0].Question)
	}
}

func TestReexplain_copiesThePastedTextsOntoTheNewRow(t *testing.T) {
	// The re-explained row copies the question, so it copies the fold too:
	// whichever row heads the turn on screen draws the chip.
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)
	ctx := context.Background()
	want := threads.PastedText{Text: "panic: boom", Lines: 1}
	if err := store.SavePastedTexts(ctx, msgID, []threads.PastedText{want}); err != nil {
		t.Fatalf("SavePastedTexts: %v", err)
	}
	orig, _, _ := store.Message(ctx, testSubject, msgID)

	doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)

	msgs, err := store.Messages(ctx, testSubject, orig.ThreadID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.ID == orig.ID {
		t.Fatal("re-explain must create a new row")
	}
	if got := last.PastedTexts; len(got) != 1 || got[0] != want {
		t.Errorf("new row PastedTexts = %+v, want %+v", got, want)
	}
}

func TestPublicShare_keepsThePastedTexts(t *testing.T) {
	// A share strips the machinery, not the question. The paste is public
	// text either way; without the fold the link would set it as prose.
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th := sharedTurn(t, st, testSubject)
	msgs, _ := st.Messages(ctx, testSubject, th.ID)
	if err := st.SavePastedTexts(ctx, msgs[0].ID, []threads.PastedText{{Text: "panic: boom", Lines: 1}}); err != nil {
		t.Fatalf("SavePastedTexts: %v", err)
	}
	sh := share(t, srv, th.PublicID)

	rec := getPublic(srv, "/api/shares/"+sh.Token)

	if !strings.Contains(rec.Body.String(), `"pasted_texts":[{"text":"panic: boom","lines":1}]`) {
		t.Errorf("the public payload lost the fold:\n%s", rec.Body.String())
	}
}
