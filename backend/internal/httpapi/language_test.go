package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestAsk_theLanguageReachesThePipelineAndTheRecord(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, st := askDeps(t, a)

	postAsk(t, deps, `{"question":"How?","audience":"ba","language":"de"}`)

	if a.gotLang != ask.LanguageDE {
		t.Errorf("language = %q, want de", a.gotLang)
	}
	list, _ := st.List(context.Background(), testSubject)
	msgs, _ := st.Messages(context.Background(), testSubject, list[0].ID)
	if len(msgs) != 1 || msgs[0].Language != "de" {
		t.Errorf("messages = %+v, want the language stored on the turn", msgs)
	}
}

func TestAsk_anUnknownLanguageFallsBackToEnglish(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, _ := askDeps(t, a)

	rec := postAsk(t, deps, `{"question":"How?","language":"klingon"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — an unknown language is not an error", rec.Code)
	}
	if a.gotLang != ask.LanguageEN {
		t.Errorf("language = %q, want en", a.gotLang)
	}
}

func TestReexplainInheritsTheLanguageOfTheTurnItReanswers(t *testing.T) {
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)
	if _, err := db.Exec(`UPDATE messages SET language = 'fr' WHERE id = ?`, msgID); err != nil {
		t.Fatalf("seed language: %v", err)
	}
	orig, _, _ := store.Message(context.Background(), testSubject, msgID)

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: token") {
		t.Fatalf("want a streamed answer:\n%s", body)
	}

	msgs, _ := store.Messages(context.Background(), testSubject, orig.ThreadID)
	last := msgs[len(msgs)-1]
	if last.Language != "fr" {
		t.Errorf("new turn language = %q, want fr inherited from the original", last.Language)
	}
}
