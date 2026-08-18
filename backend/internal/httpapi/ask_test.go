package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/threads"
)

// fakeAsker drives the handler without a model endpoint. It emits its tokens
// one at a time, so a handler that assembles the answer before writing fails.
type fakeAsker struct {
	tokens    []string
	citations []ask.Citation
	err       error
	gotAud    ask.Audience
}

func (f *fakeAsker) Run(_ context.Context, _ string, aud ask.Audience, ev ask.Events) (ask.Answer, *ask.Clarification, error) {
	f.gotAud = aud
	if f.err != nil {
		return ask.Answer{}, nil, f.err
	}
	if ev.OnStatus != nil {
		ev.OnStatus("verstehen")
	}
	var text string
	for _, tok := range f.tokens {
		text += tok
		if ev.OnToken != nil {
			ev.OnToken(tok)
		}
	}
	return ask.Answer{Text: text, Citations: f.citations}, nil, nil
}

func askDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// askDeps wires a dev-auth server over a real thread store.
func askDeps(t *testing.T, a Asker) (Deps, *threads.Store) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	return Deps{Auth: svc, Ask: a, Threads: threads.NewStore(db)}, threads.NewStore(db)
}

func postAsk(t *testing.T, deps Deps, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewServer(deps).ServeHTTP(rec, req)
	return rec
}

// events splits an SSE body into "event: name" / "data: payload" pairs.
func events(body string) [][2]string {
	var out [][2]string
	for _, block := range strings.Split(body, "\n\n") {
		var name, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name != "" {
			out = append(out, [2]string{name, data})
		}
	}
	return out
}

func names(evs [][2]string) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e[0]
	}
	return out
}

func TestAsk_streamsThreadStatusTokensAndCitations(t *testing.T) {
	// Given
	deps, _ := askDeps(t, &fakeAsker{
		tokens:    []string{"Der ", "Versand ", "laeuft [1]."},
		citations: []ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go"}},
	})

	// When
	rec := postAsk(t, deps, `{"question":"Wie laeuft der Versand?","audience":"ba"}`)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	got := names(events(rec.Body.String()))
	for _, want := range []string{"thread", "status", "token", "citations", "done"} {
		var found bool
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no %q event; got %v", want, got)
		}
	}
	// Three separate token events. One event carrying the whole answer would
	// mean the reader waits for the last word before seeing the first.
	var tokens int
	for _, e := range events(rec.Body.String()) {
		if e[0] == "token" {
			tokens++
		}
	}
	if tokens != 3 {
		t.Errorf("token events = %d, want one per token", tokens)
	}
	// The thread event comes first: the UI needs the id before anything else,
	// so a reload mid-answer still finds the conversation.
	if got[0] != "thread" {
		t.Errorf("first event = %q, want thread", got[0])
	}
}

func TestAsk_persistsTheAnswerAndItsCitations(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{
		tokens:    []string{"So laeuft es [1]."},
		citations: []ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 2, EndLine: 9}},
	})

	postAsk(t, deps, `{"question":"Wie?","audience":"ba"}`)

	list, err := st.List(context.Background(), "dev-user")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("threads = %+v, want one", list)
	}
	msgs, err := st.Messages(context.Background(), "dev-user", list[0].ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Answer != "So laeuft es [1]." {
		t.Fatalf("messages = %+v", msgs)
	}
	if len(msgs[0].Citations) != 1 {
		t.Errorf("citations = %+v, want the evidence stored with the answer", msgs[0].Citations)
	}
}

func TestAsk_aFailedTurnKeepsItsQuestionAndSaysNothingSecret(t *testing.T) {
	// The pipeline error may quote an upstream body. The browser gets a plain
	// message; the record gets the question and the failure.
	deps, st := askDeps(t, &fakeAsker{err: errors.New("upstream said: Bearer sk-secret")})

	rec := postAsk(t, deps, `{"question":"Wie?","audience":"ba"}`)

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("no error event: %s", body)
	}
	if strings.Contains(body, "sk-secret") {
		t.Errorf("the upstream error reached the browser: %s", body)
	}
	list, _ := st.List(context.Background(), "dev-user")
	msgs, _ := st.Messages(context.Background(), "dev-user", list[0].ID)
	if len(msgs) != 1 || msgs[0].Question != "Wie?" {
		t.Errorf("messages = %+v, want the failed turn kept with its question", msgs)
	}
}

func TestAsk_theAudienceReachesThePipeline(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, _ := askDeps(t, a)

	postAsk(t, deps, `{"question":"Wie?","audience":"dev"}`)

	if a.gotAud != ask.AudienceDev {
		t.Errorf("audience = %q, want dev", a.gotAud)
	}
}

func TestAsk_anotherUsersThreadIsRefused(t *testing.T) {
	// The thread id comes from the browser. Continuing someone else's
	// conversation must not be a matter of guessing a number.
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})
	// The other user has to exist: threads reference users, and a thread with
	// no owner would make this test pass for the wrong reason.
	if _, err := deps.Auth.UpsertUser("someone-else", "other@x.invalid", false); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	other, err := st.Create(context.Background(), "someone-else", "Fremde Frage?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := postAsk(t, deps, `{"question":"Wie?","audience":"ba","thread_id":`+itoa(other.ID)+`}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestAsk_anEmptyQuestionIsRejectedBeforeAnythingIsRecorded(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"x"}})

	rec := postAsk(t, deps, `{"question":"   ","audience":"ba"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	list, _ := st.List(context.Background(), "dev-user")
	if len(list) != 0 {
		t.Errorf("threads = %+v, want none created for an empty question", list)
	}
}

func TestAsk_withoutAPipelineAnswers503(t *testing.T) {
	db := askDB(t)
	deps := Deps{Auth: auth.NewService(db, "dev", ""), Threads: threads.NewStore(db)}

	rec := postAsk(t, deps, `{"question":"Wie?"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
