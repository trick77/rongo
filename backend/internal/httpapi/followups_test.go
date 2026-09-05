package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
)

// A finished answer offers what to ask next. The call is part of the turn:
// its step shows in the trace, its tokens show in the turn's usage, and what
// it wrote is stored with the message so a reload offers the same questions.

// suggesterSpy records what the handler asked it for.
type suggesterSpy struct {
	reply    []string
	calls    int
	question string
	answer   string
	audience ask.Audience
	lang     ask.Language
	sources  []ask.Source
	scope    ask.Scope
	// pays, when set, is recorded into the meter on the context the handler
	// hands over — the way the real client would.
	pays *usage.Call
}

func (sp *suggesterSpy) fn(ctx context.Context, question, answer string, audience ask.Audience,
	sources []ask.Source, scope ask.Scope, lang ask.Language,
) []string {
	sp.calls++
	sp.question, sp.answer, sp.audience, sp.lang, sp.sources, sp.scope = question, answer, audience, lang, sources, scope
	if sp.pays != nil {
		usage.Record(ctx, *sp.pays)
	}
	return sp.reply
}

// newSuggestingServer is newTestServerWithDB plus a Suggester, which the
// shared builder has no room for.
func newSuggestingServer(t *testing.T, sp *suggesterSpy, opts ...func(*fakeAsker)) (*Server, *threads.Store, *sql.DB) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	if _, err := svc.UpsertUser(testSubject, "dev@example.invalid", true); err != nil {
		t.Fatalf("seed dev user: %v", err)
	}
	f := &fakeAsker{}
	for _, o := range opts {
		o(f)
	}
	st := threads.NewStore(db)
	deps := Deps{Auth: svc, Ask: f, Threads: st}
	if sp != nil {
		deps.Suggester = sp.fn
	}
	return NewServer(deps), st, db
}

// withAskerAnswering is an answered turn with sources: the ordinary case, and
// the only one that can be followed up on.
func withAskerAnswering() func(*fakeAsker) {
	return func(f *fakeAsker) {
		f.tokens = []string{"The ", "answer."}
		f.sources = []ask.Source{{ChunkID: 1, Repo: "rongo", Path: "backend/internal/ask/answer.go", Reason: "hit"}}
	}
}

func followupsOf(t *testing.T, body string) []string {
	t.Helper()
	for _, e := range events(body) {
		if e[0] != "followups" {
			continue
		}
		var qs []string
		if err := json.Unmarshal([]byte(e[1]), &qs); err != nil {
			t.Fatalf("followups payload %q: %v", e[1], err)
		}
		return qs
	}
	return nil
}

func TestAsk_offersFollowupsBeforeTheTurnEnds(t *testing.T) {
	// Given a turn that will be answered
	sp := &suggesterSpy{reply: []string{"What happens on a re-index?", "Where is the SHA recorded?"}}
	srv, store, _ := newSuggestingServer(t, sp, withAskerAnswering())

	// When it is asked
	body := doSSE(t, srv, "/api/ask", `{"question":"how are citations stored?","audience":"ba","language":"en"}`)

	// Then the questions reach the browser
	if got := followupsOf(t, body); strings.Join(got, "|") != strings.Join(sp.reply, "|") {
		t.Errorf("followups = %q, want %q", got, sp.reply)
	}
	// and before the two events that close the turn, so the pills are there
	// the moment the answer is finished rather than after the next reload
	order := names(events(body))
	pos := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		return -1
	}
	if pos("followups") < pos("citations") || pos("followups") > pos("done") {
		t.Errorf("event order = %v, want followups after the citations and before done", order)
	}
	// and the trace says what the wait after the answer is for
	var suggesting bool
	for _, e := range events(body) {
		if e[0] == "status" && strings.Contains(e[1], "suggesting") {
			suggesting = true
		}
	}
	if !suggesting {
		t.Error("no status step announced the suggestion call; the reader sees an unexplained pause")
	}
	// and they are stored with the turn
	msgs, err := store.Messages(context.Background(), testSubject, threadIDOf(t, body))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if got := msgs[0].Followups; strings.Join(got, "|") != strings.Join(sp.reply, "|") {
		t.Errorf("stored followups = %q, want %q", got, sp.reply)
	}
}

func TestAsk_theSuggesterIsGivenTheTurnItIsFollowingUp(t *testing.T) {
	sp := &suggesterSpy{reply: []string{"Und dann?"}}
	srv, _, _ := newSuggestingServer(t, sp, withAskerAnswering())

	doSSE(t, srv, "/api/ask", `{"question":"wie werden Zitate gespeichert?","audience":"dev","language":"de"}`)

	if sp.question != "wie werden Zitate gespeichert?" {
		t.Errorf("question = %q", sp.question)
	}
	if sp.answer != "The answer." {
		t.Errorf("answer = %q, want the text that was just streamed", sp.answer)
	}
	if sp.audience != ask.AudienceDev {
		t.Errorf("audience = %q, want the turn's", sp.audience)
	}
	if sp.lang != ask.LanguageDE {
		t.Errorf("language = %q, want the turn's", sp.lang)
	}
	if len(sp.sources) == 0 {
		t.Error("the suggester was given no sources to ground its questions in")
	}
}

func TestAsk_theSuggestionCallIsPartOfWhatTheTurnPaid(t *testing.T) {
	// Metered with the rest of the turn rather than in a meter of its own:
	// the number under the answer is what the answer cost, pills included.
	sp := &suggesterSpy{
		reply: []string{"What happens on a re-index?"},
		pays:  &usage.Call{Step: "followups", Model: "mimo-v2.5", Prompt: 400, Completion: 20},
	}
	srv, store, _ := newSuggestingServer(t, sp, withAskerAnswering())

	body := doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)

	rep := usageEvent(t, body)
	var found bool
	for _, c := range rep.Calls {
		if c.Step == "followups" {
			found = true
		}
	}
	if !found {
		t.Errorf("the usage event does not carry the suggestion call: %+v", rep.Calls)
	}
	// and it is stored, so the number survives the reload the same way
	msgs, err := store.Messages(context.Background(), testSubject, threadIDOf(t, body))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	var stored bool
	for _, c := range msgs[0].Calls {
		if c.Step == "followups" {
			stored = true
		}
	}
	if !stored {
		t.Errorf("the stored usage does not carry the suggestion call: %+v", msgs[0].Calls)
	}
}

func TestAsk_withoutASuggesterTheTurnSimplyEnds(t *testing.T) {
	srv, _, _ := newSuggestingServer(t, nil, withAskerAnswering())

	body := doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)

	if strings.Contains(body, "event: followups") {
		t.Errorf("followups without a suggester:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("the turn did not end:\n%s", body)
	}
}

func TestAsk_nothingFoundIsNotFollowedUp(t *testing.T) {
	// An answer written from no sources is the nothing-found reply. Offering
	// a question there would be inventing one the index cannot answer.
	sp := &suggesterSpy{reply: []string{"What else?"}}
	srv, _, _ := newSuggestingServer(t, sp, func(f *fakeAsker) { f.tokens = []string{"Found nothing."} })

	body := doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)

	if sp.calls != 0 {
		t.Errorf("the suggester was called %d times on a nothing-found turn", sp.calls)
	}
	if strings.Contains(body, "event: followups") {
		t.Errorf("pills under a nothing-found answer:\n%s", body)
	}
}

func TestAsk_aTurnThatFailedOrAskedBackIsNotFollowedUp(t *testing.T) {
	sp := &suggesterSpy{reply: []string{"What else?"}}
	srv, _, _ := newSuggestingServer(t, sp, func(f *fakeAsker) { f.err = errors.New("upstream down") })
	doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)
	if sp.calls != 0 {
		t.Errorf("the suggester was called %d times on a failed turn", sp.calls)
	}

	sp = &suggesterSpy{reply: []string{"What else?"}}
	srv, _, _ = newSuggestingServer(t, sp, withAskerAsking())
	body := doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)
	if sp.calls != 0 {
		t.Errorf("the suggester was called %d times on a turn that asked back", sp.calls)
	}
	if strings.Contains(body, "event: followups") {
		t.Errorf("pills beside a clarification card:\n%s", body)
	}
}

func TestAsk_aSuggesterThatCameBackEmptyCostsNothingButThePills(t *testing.T) {
	sp := &suggesterSpy{reply: nil}
	srv, store, _ := newSuggestingServer(t, sp, withAskerAnswering())

	body := doSSE(t, srv, "/api/ask", `{"question":"how?","audience":"ba","language":"en"}`)

	if sp.calls != 1 {
		t.Errorf("suggester calls = %d, want 1", sp.calls)
	}
	if strings.Contains(body, "event: followups") {
		t.Errorf("an empty suggestion list must send no event:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("the turn did not end:\n%s", body)
	}
	msgs, _ := store.Messages(context.Background(), testSubject, threadIDOf(t, body))
	if len(msgs[0].Followups) != 0 {
		t.Errorf("stored followups = %q, want none", msgs[0].Followups)
	}
}

func TestResumedAndReexplainedTurnsAreFollowedUpToo(t *testing.T) {
	// Every path that produces an answer ends the same way: an answer is an
	// answer whether it came from a card or from a second reading.
	sp := &suggesterSpy{reply: []string{"What happens on a re-index?"}}
	srv, store, _ := newSuggestingServer(t, sp, withAskerResuming())
	msgID, _ := seedClarification(t, store)

	body := doSSE(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"how is sign-in done?","clarification_message_id":%d,"choice":1}`, msgID))
	if got := followupsOf(t, body); len(got) != 1 {
		t.Errorf("resumed turn followups = %q, want one:\n%s", got, body)
	}

	sp = &suggesterSpy{reply: []string{"What happens on a re-index?"}}
	srv, store, db := newSuggestingServer(t, sp, func(f *fakeAsker) {
		withAskerReexplaining()(f)
		f.sources = []ask.Source{{ChunkID: 1, Reason: "hit"}}
	})
	answered := seedAnsweredMessageWithSources(t, store, db)

	body = doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", answered), `{"audience":"dev"}`)
	if got := followupsOf(t, body); len(got) != 1 {
		t.Errorf("re-explained turn followups = %q, want one:\n%s", got, body)
	}
	if sp.audience != ask.AudienceDev {
		t.Errorf("audience = %q, want the re-explained turn's", sp.audience)
	}
}

func TestFollowups_aResumedTurnKeepsTheRepositoriesTheIndexLacks(t *testing.T) {
	// Only Run fills Answer.Scope. Reading the scope off the answer would
	// hand the suggestion prompt an empty one on every resumed and
	// re-explained turn, and the pills could then offer a question about a
	// repository the index never had - which can only come back as
	// "nothing found".
	sp := &suggesterSpy{reply: []string{"What happens on a re-index?"}}
	srv, store, _ := newSuggestingServer(t, sp, withAskerResuming())
	msgID, _ := seedClarification(t, store)
	if err := store.SetScope(context.Background(), msgID, ask.Scope{Known: []string{"peeq"}, Unknown: []string{"shop-backend"}}); err != nil {
		t.Fatalf("SetScope: %v", err)
	}

	doSSE(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"how is sign-in done in peeq and shop-backend?","clarification_message_id":%d,"choice":1}`, msgID))

	if strings.Join(sp.scope.Unknown, ",") != "shop-backend" {
		t.Errorf("scope.Unknown = %q, want the repositories the index lacks", sp.scope.Unknown)
	}
}

func TestFollowups_aReexplainedTurnKeepsTheRepositoriesTheIndexLacks(t *testing.T) {
	sp := &suggesterSpy{reply: []string{"What happens on a re-index?"}}
	srv, store, db := newSuggestingServer(t, sp, func(f *fakeAsker) {
		withAskerReexplaining()(f)
		f.sources = []ask.Source{{ChunkID: 1, Reason: "hit"}}
	})
	answered := seedAnsweredMessageWithSources(t, store, db)
	if err := store.SetScope(context.Background(), answered, ask.Scope{Known: []string{"peeq"}, Unknown: []string{"shop-backend"}}); err != nil {
		t.Fatalf("SetScope: %v", err)
	}

	doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", answered), `{"audience":"dev"}`)

	if strings.Join(sp.scope.Unknown, ",") != "shop-backend" {
		t.Errorf("scope.Unknown = %q, want the repositories the index lacks", sp.scope.Unknown)
	}
}

func TestFollowups_aFreshTurnPassesTheScopeTheAnswerCarried(t *testing.T) {
	sp := &suggesterSpy{reply: []string{"What happens on a re-index?"}}
	srv, _, _ := newSuggestingServer(t, sp, func(f *fakeAsker) {
		withAskerAnswering()(f)
		f.scope = ask.Scope{Known: []string{"peeq"}, Unknown: []string{"shop-backend"}}
	})

	doSSE(t, srv, "/api/ask", `{"question":"how is sign-in done in peeq and shop-backend?","audience":"ba","language":"en"}`)

	if strings.Join(sp.scope.Unknown, ",") != "shop-backend" {
		t.Errorf("scope.Unknown = %q, want the repositories the index lacks", sp.scope.Unknown)
	}
}
