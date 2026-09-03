package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
)

// testSubject is the identity dev auth mode signs in under. Every seed
// helper that does not take an explicit owner uses it.
const testSubject = "dev-user"

// fakeAsker drives the handler without a model endpoint. It emits its tokens
// one at a time, so a handler that assembles the answer before writing fails.
type fakeAsker struct {
	tokens    []string
	citations []ask.Citation
	err       error
	gotAud    ask.Audience
	gotLang   ask.Language
	// calls is what the fake "pays for" before it decides how the turn ends,
	// recorded into the meter on the context the way the real clients do.
	calls []usage.Call

	// clarification, when set, makes Run end the turn by asking instead of
	// answering.
	clarification *ask.Clarification

	resumeTokens []string
	resumeErr    error

	reexplainTokens []string
	reexplainErr    error
}

func (f *fakeAsker) Run(ctx context.Context, _ string, aud ask.Audience, lang ask.Language, ev ask.Events) (ask.Answer, *ask.Clarification, error) {
	f.gotAud = aud
	f.gotLang = lang
	for _, c := range f.calls {
		usage.Record(ctx, c)
	}
	if f.err != nil {
		return ask.Answer{}, nil, f.err
	}
	if f.clarification != nil {
		return ask.Answer{}, f.clarification, nil
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

// Resume answers from the candidate's own hits — it never searches, which is
// the whole point of a resumed turn.
func (f *fakeAsker) Resume(ctx context.Context, _ string, aud ask.Audience, lang ask.Language, _ []retrieve.Hit, ev ask.Events) (ask.Answer, error) {
	f.gotAud = aud
	f.gotLang = lang
	for _, c := range f.calls {
		usage.Record(ctx, c)
	}
	if f.resumeErr != nil {
		return ask.Answer{}, f.resumeErr
	}
	var text string
	for _, tok := range f.resumeTokens {
		text += tok
		if ev.OnToken != nil {
			ev.OnToken(tok)
		}
	}
	// A resumed turn always carries the sources it was written from, so the
	// handler has something to persist for a later re-explain.
	return ask.Answer{Text: text, Sources: []ask.Source{{ChunkID: 1, Reason: "hit"}}}, nil
}

func (f *fakeAsker) Reexplain(ctx context.Context, _ string, aud ask.Audience, lang ask.Language, _ []ask.Source, ev ask.Events) (ask.Answer, error) {
	f.gotAud = aud
	f.gotLang = lang
	for _, c := range f.calls {
		usage.Record(ctx, c)
	}
	if f.reexplainErr != nil {
		return ask.Answer{}, f.reexplainErr
	}
	var text string
	for _, tok := range f.reexplainTokens {
		text += tok
		if ev.OnToken != nil {
			ev.OnToken(tok)
		}
	}
	return ask.Answer{Text: text}, nil
}

// withAskerAsking makes Run end the turn with a two-candidate clarification.
func withAskerAsking() func(*fakeAsker) {
	return func(f *fakeAsker) {
		f.clarification = &ask.Clarification{
			Understanding: ask.Understanding{CodeTerms: []string{"auth"}},
			Candidates: []ask.Candidate{
				{Repo: "peeq", Branch: "master", ModuleKey: "oauth", Title: "Via OAuth", Summary: "s1",
					Hits: []retrieve.Hit{{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "a.go"}}},
				{Repo: "peeq", Branch: "master", ModuleKey: "sso", Title: "Via SSO", Summary: "s2",
					Hits: []retrieve.Hit{{ChunkID: 2, Repo: "peeq", Branch: "master", Path: "b.go"}}},
			},
		}
	}
}

func withAskerResuming() func(*fakeAsker) {
	return func(f *fakeAsker) { f.resumeTokens = []string{"The ", "answer."} }
}

func withAskerReexplaining() func(*fakeAsker) {
	return func(f *fakeAsker) { f.reexplainTokens = []string{"The ", "explanation."} }
}

// newTestServer builds a server over a fresh dev-auth store, wired to a
// fakeAsker shaped by opts.
func newTestServer(t *testing.T, opts ...func(*fakeAsker)) *Server {
	t.Helper()
	srv, _, _ := newTestServerWithDB(t, opts...)
	return srv
}

// newTestServerWithStore is newTestServer plus the store, for tests that seed
// data or read back what a turn recorded.
func newTestServerWithStore(t *testing.T, opts ...func(*fakeAsker)) (*Server, *threads.Store) {
	t.Helper()
	srv, st, _ := newTestServerWithDB(t, opts...)
	return srv, st
}

// newTestServerWithDB additionally exposes the raw database, for seeding rows
// (chunks, files, repo_state) no threads.Store method writes.
func newTestServerWithDB(t *testing.T, opts ...func(*fakeAsker)) (*Server, *threads.Store, *sql.DB) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	// Both users have to exist before a thread references them: threads have
	// a foreign key on the owning subject.
	if _, err := svc.UpsertUser(testSubject, "dev@example.invalid", true); err != nil {
		t.Fatalf("seed dev user: %v", err)
	}
	if _, err := svc.UpsertUser("someone-else", "other@x.invalid", false); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	f := &fakeAsker{}
	for _, o := range opts {
		o(f)
	}
	st := threads.NewStore(db)
	return NewServer(Deps{Auth: svc, Ask: f, Threads: st}), st, db
}

func doSSE(t *testing.T, srv *Server, path, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Body.String()
}

func doStatus(t *testing.T, srv *Server, path, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

// seedClarification seeds a thread, its question and the clarification it
// ended with, owned by testSubject.
func seedClarification(t *testing.T, store *threads.Store) (msgID, clarID int64) {
	t.Helper()
	return seedClarificationOwnedBy(t, store, testSubject)
}

func seedClarificationOwnedBy(t *testing.T, store *threads.Store, subject string) (msgID, clarID int64) {
	t.Helper()
	ctx := context.Background()
	th, err := store.Create(ctx, subject, "how is sign-in done?")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	msg, err := store.AddQuestion(ctx, th.ID, "ba", "en", "how is sign-in done?")
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	clarID, err = store.Clarify(ctx, msg.ID, ask.Clarification{
		Understanding: ask.Understanding{CodeTerms: []string{"auth"}},
		Candidates: []ask.Candidate{
			{Repo: "peeq", Branch: "master", ModuleKey: "oauth", Title: "Via OAuth", Summary: "s1",
				Hits: []retrieve.Hit{{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "a.go"}}},
			{Repo: "peeq", Branch: "master", ModuleKey: "sso", Title: "Via SSO", Summary: "s2",
				Hits: []retrieve.Hit{{ChunkID: 2, Repo: "peeq", Branch: "master", Path: "b.go"}}},
		},
	})
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	return msg.ID, clarID
}

// threadOf resolves the thread a clarifying message belongs to, the same way
// the handler does: through the subject-scoped Clarification read.
func threadOf(t *testing.T, store *threads.Store, msgID int64) int64 {
	t.Helper()
	clar, err := store.Clarification(context.Background(), testSubject, msgID)
	if err != nil {
		t.Fatalf("clarification: %v", err)
	}
	if clar == nil {
		t.Fatalf("no clarification for message %d", msgID)
	}
	return clar.ThreadID
}

// seedAnsweredMessageWithSources seeds a finished turn whose answer has real,
// joinable sources — a repo, a file and a chunk row, not just a bare chunk id.
func seedAnsweredMessageWithSources(t *testing.T, store *threads.Store, db *sql.DB) int64 {
	t.Helper()
	ctx := context.Background()
	chunkID := seedChunk(t, db)
	th, err := store.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	msg, err := store.AddQuestion(ctx, th.ID, "ba", "en", "frage")
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := store.Finish(ctx, msg.ID, "antwort", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := store.SaveSources(ctx, msg.ID, []ask.Source{{ChunkID: chunkID, Reason: "hit"}}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	return msg.ID
}

// seedAnsweredMessageWithoutSources seeds a finished turn that never had its
// sources saved — standing in for a chunk a re-index later removed.
func seedAnsweredMessageWithoutSources(t *testing.T, store *threads.Store) int64 {
	t.Helper()
	ctx := context.Background()
	th, err := store.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	msg, err := store.AddQuestion(ctx, th.ID, "ba", "en", "frage")
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := store.Finish(ctx, msg.ID, "antwort", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return msg.ID
}

// seedAnsweredMessageWithOneVanishedSource seeds a finished turn whose answer
// was written from two chunks, then simulates a re-index removing one of
// them — standing in for the common case: a re-index removes SOME of the
// evidence, not all of it.
func seedAnsweredMessageWithOneVanishedSource(t *testing.T, store *threads.Store, db *sql.DB) int64 {
	t.Helper()
	ctx := context.Background()
	chunkA := seedChunk(t, db)
	chunkB := seedChunkAt(t, db, "b.go")
	th, err := store.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	msg, err := store.AddQuestion(ctx, th.ID, "ba", "en", "frage")
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := store.Finish(ctx, msg.ID, "antwort", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := store.SaveSources(ctx, msg.ID, []ask.Source{
		{ChunkID: chunkA, Reason: "hit"},
		{ChunkID: chunkB, Reason: "hit"},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM chunks WHERE id = ?`, chunkB); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}
	return msg.ID
}

// seedChunkAt is seedChunk for a second file in the same repo, so a message
// can have two sources.
func seedChunkAt(t *testing.T, db *sql.DB, path string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('peeq', ?, 'def')`, path)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	res, err = db.Exec(`
		INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, content_hash)
		VALUES (?, 0, 1, 5, '', 'package b', 'package b', 'hash2')`, fileID)
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	return chunkID
}

func seedChunk(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url) VALUES ('peeq', 'https://example.invalid/peeq.git')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('peeq', 'a.go', 'abc')`)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	res, err = db.Exec(`
		INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, content_hash)
		VALUES (?, 0, 1, 5, '', 'package a', 'package a', 'hash1')`, fileID)
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	return chunkID
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
		tokens:    []string{"The ", "shipping ", "runs [1]."},
		citations: []ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go"}},
	})

	// When
	rec := postAsk(t, deps, `{"question":"How does shipping work?","audience":"ba"}`)

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
		tokens:    []string{"That is how it works [1]."},
		citations: []ask.Citation{{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 2, EndLine: 9}},
	})

	postAsk(t, deps, `{"question":"How?","audience":"ba"}`)

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
	if len(msgs) != 1 || msgs[0].Answer != "That is how it works [1]." {
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

	rec := postAsk(t, deps, `{"question":"How?","audience":"ba"}`)

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("no error event: %s", body)
	}
	if strings.Contains(body, "sk-secret") {
		t.Errorf("the upstream error reached the browser: %s", body)
	}
	list, _ := st.List(context.Background(), "dev-user")
	msgs, _ := st.Messages(context.Background(), "dev-user", list[0].ID)
	if len(msgs) != 1 || msgs[0].Question != "How?" {
		t.Errorf("messages = %+v, want the failed turn kept with its question", msgs)
	}
}

func TestAsk_theAudienceReachesThePipeline(t *testing.T) {
	a := &fakeAsker{tokens: []string{"x"}}
	deps, _ := askDeps(t, a)

	postAsk(t, deps, `{"question":"How?","audience":"dev"}`)

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
	other, err := st.Create(context.Background(), "someone-else", "Someone else's question?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := postAsk(t, deps, `{"question":"How?","audience":"ba","thread_id":`+itoa(other.ID)+`}`)

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

	rec := postAsk(t, deps, `{"question":"How?"}`)

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

// gateCalls is what a turn pays for before it decides how to end: the
// understanding gate and the query embedding.
var gateCalls = []usage.Call{
	{Step: "understand", Model: "mimo-v2.5", Prompt: 100, Completion: 20},
	{Step: "embed", Model: "text-embedding-3-small", Prompt: 12},
}

// usageEvent finds the usage event in an SSE body and decodes it.
func usageEvent(t *testing.T, body string) usage.Report {
	t.Helper()
	for _, e := range events(body) {
		if e[0] == "usage" {
			var r usage.Report
			if err := json.Unmarshal([]byte(e[1]), &r); err != nil {
				t.Fatalf("usage event %q: %v", e[1], err)
			}
			return r
		}
	}
	t.Fatalf("no usage event in:\n%s", body)
	return usage.Report{}
}

func TestAsk_theUsageEventCarriesEveryCallOfTheTurnAndTheCallsAreStored(t *testing.T) {
	// Given
	srv, st := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"The ", "answer."}
		f.calls = gateCalls
	})

	// When
	body := doSSE(t, srv, "/api/ask", `{"question":"how?"}`)

	// Then: the event sums the calls; total_tokens keeps its old name.
	got := usageEvent(t, body)
	if len(got.Calls) != 2 || got.Calls[0].Step != "understand" || got.Calls[1].Step != "embed" {
		t.Errorf("calls = %+v, want the two gate calls in order", got.Calls)
	}
	if got.Prompt != 112 || got.Completion != 20 || got.Total != 132 {
		t.Errorf("totals = %d/%d/%d, want 112/20/132", got.Prompt, got.Completion, got.Total)
	}
	if got.CostUSD != nil {
		t.Errorf("cost = %v, want none: no prices are configured", *got.CostUSD)
	}
	// And the record holds them, so a reload and the thread total see them.
	msgs, err := st.Messages(context.Background(), testSubject, threadIDOf(t, body))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].Calls) != 2 {
		t.Fatalf("stored calls = %+v, want 2", msgs)
	}
}

func TestAsk_aTurnThatFailedOrAskedBackStillReportsAndStoresWhatItPaidFor(t *testing.T) {
	cases := []struct {
		name  string
		shape func(*fakeAsker)
		ends  string
	}{
		{"failed", func(f *fakeAsker) { f.err = errors.New("upstream down"); f.calls = gateCalls }, "error"},
		{"asked back", func(f *fakeAsker) { withAskerAsking()(f); f.calls = gateCalls }, "clarification"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			srv, st := newTestServerWithStore(t, tc.shape)

			// When
			body := doSSE(t, srv, "/api/ask", `{"question":"how?"}`)

			// Then: usage arrives, and before the event that ends the turn,
			// so the browser has it whichever way the turn closed.
			got := usageEvent(t, body)
			if got.Total != 132 {
				t.Errorf("total = %d, want 132", got.Total)
			}
			var seenUsage bool
			for _, e := range events(body) {
				if e[0] == "usage" {
					seenUsage = true
				}
				if e[0] == tc.ends && !seenUsage {
					t.Errorf("%s event came before usage", tc.ends)
				}
			}
			msgs, err := st.Messages(context.Background(), testSubject, threadIDOf(t, body))
			if err != nil {
				t.Fatalf("Messages: %v", err)
			}
			if len(msgs) != 1 || len(msgs[0].Calls) != 2 {
				t.Fatalf("stored calls = %+v, want the gate calls", msgs)
			}
		})
	}
}

func TestThread_servesStoredUsagePricedWhenPricesAreConfigured(t *testing.T) {
	// Given a stored turn with usage, and a price for the gate deployment only
	srv, _ := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"The ", "answer."}
		f.calls = gateCalls
	})
	srv.deps.Prices = usage.Prices{"mimo-v2.5": usage.Price{In: 1, Out: 2}}
	body := doSSE(t, srv, "/api/ask", `{"question":"how?"}`)
	id := threadIDOf(t, body)

	// When
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/threads/%d", id), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then: usage on the message, priced from the CURRENT table — the embed
	// call has no price and carries none, the understanding call does.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var msgs []threads.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Usage == nil {
		t.Fatalf("messages = %s, want one with usage", rec.Body.String())
	}
	u := msgs[0].Usage
	if u.Total != 132 || len(u.Calls) != 2 {
		t.Errorf("usage = %+v", u)
	}
	// 100*1 + 20*2 = 140 per million
	if u.CostUSD == nil || *u.CostUSD < 0.00014-1e-12 || *u.CostUSD > 0.00014+1e-12 {
		t.Errorf("cost = %v, want 0.00014", u.CostUSD)
	}
	if u.Calls[1].CostUSD != nil {
		t.Errorf("the unpriced embed call carries a cost: %v", *u.Calls[1].CostUSD)
	}
}

func TestAsk_aTurnThatPaidForNothingSendsNoUsage(t *testing.T) {
	// Given: the first call never reached the upstream, so nothing was
	// metered.
	srv := newTestServer(t, func(f *fakeAsker) { f.err = errors.New("endpoint down") })

	// When
	body := doSSE(t, srv, "/api/ask", `{"question":"how?"}`)

	// Then: no usage event. A "0 tok" pill would claim the turn was free,
	// and the reload would show no pill at all for the same turn.
	for _, e := range events(body) {
		if e[0] == "usage" {
			t.Fatalf("usage event %s sent for a turn with no calls", e[1])
		}
	}
	if !strings.Contains(body, "event: error") {
		t.Error("the turn must still end with an error event")
	}
}

// threadIDOf reads the thread id off the first event of an SSE body.
func threadIDOf(t *testing.T, body string) int64 {
	t.Helper()
	for _, e := range events(body) {
		if e[0] == "thread" {
			var p struct {
				ThreadID int64 `json:"thread_id"`
			}
			if err := json.Unmarshal([]byte(e[1]), &p); err != nil {
				t.Fatalf("thread event: %v", err)
			}
			return p.ThreadID
		}
	}
	t.Fatalf("no thread event in:\n%s", body)
	return 0
}

func TestAskStreamsAClarificationAndEndsTheTurn(t *testing.T) {
	// Given a pipeline that asks
	srv := newTestServer(t, withAskerAsking())

	// When
	body := doSSE(t, srv, "/api/ask", `{"question":"how is sign-in done?"}`)

	// Then
	if !strings.Contains(body, "event: clarification") {
		t.Fatalf("no clarification event in:\n%s", body)
	}
	if strings.Contains(body, "event: token") {
		t.Error("a turn that asks streams no answer")
	}
	if !strings.Contains(body, "event: done") {
		t.Error("the turn must end, or the browser waits forever")
	}
}

// clarifyFailingThreads wraps a real store but makes Clarify fail, standing
// in for a write that could not commit (disk full, a busy database). It
// still needs every other method of the Threads interface, which the
// embedded *threads.Store supplies.
type clarifyFailingThreads struct {
	*threads.Store
}

func (c *clarifyFailingThreads) Clarify(context.Context, int64, ask.Clarification) (int64, error) {
	return 0, errors.New("disk full")
}

func TestAskWhenClarifyFailsToWriteTheCardIsNeverSent(t *testing.T) {
	// Given a pipeline that ends by asking, but a store that cannot write
	// the clarification
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	if _, err := svc.UpsertUser(testSubject, "dev@example.invalid", true); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	st := threads.NewStore(db)
	deps := Deps{Auth: svc, Ask: &fakeAsker{}, Threads: &clarifyFailingThreads{Store: st}}
	withAskerAsking()(deps.Ask.(*fakeAsker))

	// When
	rec := postAsk(t, deps, `{"question":"how is sign-in done?"}`)

	// Then no clarification event ships — a card whose candidates were never
	// stored would offer choices resuming them cannot honour
	body := rec.Body.String()
	if strings.Contains(body, "event: clarification") {
		t.Errorf("a clarification event shipped despite the failed write:\n%s", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Errorf("no error event:\n%s", body)
	}
	// And the turn is recorded as failed, not left answerless and errorless
	list, err := st.List(context.Background(), testSubject)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: list=%+v err=%v", list, err)
	}
	msgs, err := st.Messages(context.Background(), testSubject, list[0].ID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages: msgs=%+v err=%v", msgs, err)
	}
	if msgs[0].Error == "" {
		t.Errorf("message = %+v, want it recorded as failed", msgs[0])
	}
}

func TestAskWithAChoiceResumesWithoutSearching(t *testing.T) {
	// Given a stored clarification
	srv, store := newTestServerWithStore(t, withAskerResuming())
	msgID, clarID := seedClarification(t, store)

	// When the reader picks the second candidate
	body := doSSE(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"how is sign-in done?","clarification_message_id":%d,"choice":1}`, msgID))

	// Then
	if !strings.Contains(body, "event: token") {
		t.Fatalf("a chosen candidate must produce an answer:\n%s", body)
	}
	// and the new turn records where it came from
	msgs, err := store.Messages(context.Background(), testSubject, threadOf(t, store, msgID))
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.FromCandidateIdx != 1 {
		t.Errorf("new turn records candidate %d, want 1", last.FromCandidateIdx)
	}
	_ = clarID
}

func TestChoiceOutOfRangeIsRefusedNotGuessed(t *testing.T) {
	srv, store := newTestServerWithStore(t, withAskerResuming())
	msgID, _ := seedClarification(t, store)

	code := doStatus(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"frage","clarification_message_id":%d,"choice":7}`, msgID))
	if code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 — answering from a candidate nobody offered is worse than refusing", code)
	}
}

func TestAClarificationOfSomeoneElsesThreadIsRefused(t *testing.T) {
	// The id comes from the browser. Whether it exists is not something to
	// confirm to someone who does not own it.
	srv, store := newTestServerWithStore(t, withAskerResuming())
	msgID, _ := seedClarificationOwnedBy(t, store, "someone-else")

	code := doStatus(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"frage","clarification_message_id":%d,"choice":0}`, msgID))
	if code != http.StatusForbidden {
		t.Errorf("status %d, want 403", code)
	}
}

func TestReexplainStreamsFromTheStoredSources(t *testing.T) {
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: token") {
		t.Fatalf("want a streamed answer:\n%s", body)
	}
}

func TestReexplainAddsANewTurnAndLeavesThePreviousAnswerUntouched(t *testing.T) {
	// Given a finished turn with sources
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)
	orig, found, err := store.Message(context.Background(), testSubject, msgID)
	if err != nil || !found {
		t.Fatalf("Message: found=%v err=%v", found, err)
	}

	// When re-explaining for the dev audience
	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: token") {
		t.Fatalf("want a streamed answer:\n%s", body)
	}

	// Then the thread has one more message
	msgs, err := store.Messages(context.Background(), testSubject, orig.ThreadID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want the original turn plus the re-explained one", msgs)
	}

	// and the previous answer is untouched
	if msgs[0].ID != orig.ID || msgs[0].Answer != orig.Answer || msgs[0].Audience != orig.Audience {
		t.Errorf("previous turn = %+v, want it unchanged from %+v", msgs[0], orig)
	}

	// and the new turn carries the requested audience, its own sources and
	// never resumed a clarification
	last := msgs[len(msgs)-1]
	if last.ID == orig.ID {
		t.Fatal("re-explain must create a new message, not rewrite the old one")
	}
	if last.Audience != "dev" {
		t.Errorf("new turn audience = %q, want dev", last.Audience)
	}
	if last.FromCandidateIdx != -1 {
		t.Errorf("new turn FromCandidateIdx = %d, want -1 — it did not resume a clarification", last.FromCandidateIdx)
	}
	newSources, _, err := store.Sources(context.Background(), testSubject, last.ID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(newSources) == 0 {
		t.Error("the new turn must have its own sources, so it can itself be re-explained later")
	}
}

func TestReexplainSaysSoWhenTheBasisIsGone(t *testing.T) {
	srv, store := newTestServerWithStore(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithoutSources(t, store)

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: error") {
		t.Errorf("a vanished basis is an error, never the same question answered from other code:\n%s", body)
	}
}

func TestReexplainSaysSoWhenOnlyPartOfTheBasisIsGone(t *testing.T) {
	// A re-index usually removes SOME chunks, not all of them. Answering from
	// the survivors would be the same question answered from different code
	// than the reader was shown — a silent substitution, exactly what the
	// invariants forbid.
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithOneVanishedSource(t, store, db)

	body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)
	if !strings.Contains(body, "event: error") {
		t.Errorf("a partially vanished basis is an error, never an answer from the surviving source:\n%s", body)
	}
	if !strings.Contains(body, basisGone) {
		t.Errorf("want the basisGone message, not the generic turnFailed one:\n%s", body)
	}
	if strings.Contains(body, "event: token") {
		t.Errorf("a partially vanished basis must not stream an answer:\n%s", body)
	}
}
