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

// A thread is answered in one language. A follow-up asking for another one is
// answered in the thread's, and the pipeline is told the same thing the record
// holds — otherwise a stale tab would produce a French answer stored as German.
func TestAsk_aFollowupIsAnsweredInTheThreadsLanguage(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, st := askDeps(t, a)

	postAsk(t, deps, `{"question":"How?","audience":"ba","language":"de"}`)
	list, _ := st.List(context.Background(), testSubject)
	threadID := list[0].ID

	a.gotLang = ""
	postAsk(t, deps, fmt.Sprintf(`{"question":"And then?","audience":"ba","language":"fr","thread_id":%d}`, threadID))

	if a.gotLang != ask.LanguageDE {
		t.Errorf("language = %q, want de — the thread's language, not the composer's", a.gotLang)
	}
	msgs, _ := st.Messages(context.Background(), testSubject, threadID)
	if len(msgs) != 2 || msgs[1].Language != "de" {
		t.Errorf("messages = %+v, want the follow-up stored as de", msgs)
	}
}

// The composer cannot know a thread's language before it asks - it may be a
// thread still loading, or one whose turns are older than the pin. The stream
// says which language the record took, first thing.
func TestAsk_theThreadEventCarriesTheLanguageTheRecordTook(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, st := askDeps(t, a)

	postAsk(t, deps, `{"question":"Wie?","audience":"ba","language":"de"}`)
	list, _ := st.List(context.Background(), testSubject)

	rec := postAsk(t, deps, fmt.Sprintf(`{"question":"Und dann?","audience":"ba","language":"fr","thread_id":%d}`, list[0].ID))
	for _, e := range events(rec.Body.String()) {
		if e[0] == "thread" && !strings.Contains(e[1], `"language":"de"`) {
			t.Errorf("thread event = %s, want the thread's language on it", e[1])
		}
	}
}
