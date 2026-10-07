package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/threads"
)

// readFailingThreads is a store whose named reads fail the way a busy
// database does, so a test can see what a turn does without them.
type readFailingThreads struct {
	*threads.Store
	scope, last, ordinal bool
}

var errLocked = errors.New("database is locked")

func (r *readFailingThreads) ThreadScope(ctx context.Context, subject string, id int64) ([]string, error) {
	if r.scope {
		return nil, errLocked
	}
	return r.Store.ThreadScope(ctx, subject, id)
}

func (r *readFailingThreads) LastTurnBefore(ctx context.Context, subject string, id int64, before int) (threads.Message, bool, error) {
	if r.last {
		return threads.Message{}, false, errLocked
	}
	return r.Store.LastTurnBefore(ctx, subject, id, before)
}

func (r *readFailingThreads) MessageOrdinal(ctx context.Context, subject string, id int64) (int, bool, error) {
	if r.ordinal {
		return 0, false, errLocked
	}
	return r.Store.MessageOrdinal(ctx, subject, id)
}

// refusedBeforeTheStream asserts a 500 with no stream and no new row: the
// refusal has to land before AddQuestion, or the record holds a question
// nobody answered.
func refusedBeforeTheStream(t *testing.T, rec *httptest.ResponseRecorder, st *threads.Store, threadID int64, rows int) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "event:") || strings.Contains(rec.Body.String(), errLocked.Error()) {
		t.Errorf("body = %q, want no stream and no database text", rec.Body.String())
	}
	msgs, err := st.Messages(context.Background(), testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != rows {
		t.Errorf("thread holds %d rows, want %d — nothing written", len(msgs), rows)
	}
}

// threadWithOneAnswer is a thread narrowed to rongo by one answered turn,
// served through a store whose reads the test can break afterwards.
func threadWithOneAnswer(t *testing.T, a *fakeAsker) (Deps, *threads.Store, *readFailingThreads, threads.Thread) {
	t.Helper()
	a.tokens, a.scope = []string{"x"}, ask.Scope{Known: []string{"rongo"}}
	deps, st := headDeps(t, a)
	postAsk(t, deps, `{"question":"How does rongo cite sources?","audience":"ba"}`)
	list, err := st.List(context.Background(), testSubject)
	if err != nil || len(list) != 1 {
		t.Fatalf("list threads: %v (%d)", err, len(list))
	}
	failing := &readFailingThreads{Store: st}
	deps.Threads = failing
	return deps, st, failing, list[0]
}

// failedRow adds a failed turn to the thread, joining head.
func failedRow(t *testing.T, st *threads.Store, threadID int64, audience, question string, head int64) threads.Message {
	t.Helper()
	m, err := st.AddQuestion(context.Background(), threadID, audience, "en", question, head)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Fail(context.Background(), m.ID, "kaputt"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	return m
}

// TestAsk_aThreadWhosePinCannotBeReadFailsBeforeTheStream: an unreadable pin
// is not an un-narrowed thread. Searching the whole corpus under a thread
// that named one repository answers a question nobody asked.
func TestAsk_aThreadWhosePinCannotBeReadFailsBeforeTheStream(t *testing.T) {
	t.Run("typed into the thread", func(t *testing.T) {
		deps, st, failing, th := threadWithOneAnswer(t, &fakeAsker{})
		failing.scope = true

		rec := postAsk(t, deps, fmt.Sprintf(`{"thread_id":%q,"question":"und das?","audience":"ba"}`, th.PublicID))

		refusedBeforeTheStream(t, rec, st, th.ID, 1)
	})
	t.Run("a retry of a row that recorded no scope", func(t *testing.T) {
		deps, st, failing, th := threadWithOneAnswer(t, &fakeAsker{})
		failed := failedRow(t, st, th.ID, "ba", "und das?", 0)
		failing.scope = true

		rec := postAsk(t, deps, fmt.Sprintf(`{"thread_id":%q,"question":"und das?","audience":"ba","head_message_id":%d}`,
			th.PublicID, failed.ID))

		refusedBeforeTheStream(t, rec, st, th.ID, 2)
	})
}

// TestAsk_aReworkWhoseAntecedentCannotBeReadFails: "summarize" with no
// previous turn is a fresh search dressed as a summary. A previous turn that
// could not be READ is not a previous turn that is absent.
func TestAsk_aReworkWhoseAntecedentCannotBeReadFails(t *testing.T) {
	t.Run("the last answered turn", func(t *testing.T) {
		deps, st, failing, th := threadWithOneAnswer(t, &fakeAsker{})
		failing.last = true

		rec := postAsk(t, deps, fmt.Sprintf(`{"thread_id":%q,"question":"summarize","audience":"ba"}`, th.PublicID))

		refusedBeforeTheStream(t, rec, st, th.ID, 1)
	})
	t.Run("the head of a retried continuation", func(t *testing.T) {
		deps, st, failing, th := threadWithOneAnswer(t, &fakeAsker{})
		msgs, err := st.Messages(context.Background(), testSubject, th.ID)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("messages: %v (%d)", err, len(msgs))
		}
		// A re-explain of the first turn that failed: its head is not itself.
		failed := failedRow(t, st, th.ID, "dev", msgs[0].Question, msgs[0].ID)
		failing.ordinal = true

		rec := postAsk(t, deps, fmt.Sprintf(`{"thread_id":%q,"question":%q,"audience":"dev","head_message_id":%d}`,
			th.PublicID, msgs[0].Question, failed.ID))

		refusedBeforeTheStream(t, rec, st, th.ID, 2)
	})
	t.Run("the head of a re-explained rework row", func(t *testing.T) {
		a := &fakeAsker{reexplainTokens: []string{"y"}}
		deps, st, failing, th := threadWithOneAnswer(t, a)
		ctx := context.Background()
		rework, err := st.AddQuestion(ctx, th.ID, "ba", "en", "summarize", 0)
		if err != nil {
			t.Fatalf("add rework: %v", err)
		}
		if err := st.Finish(ctx, rework.ID, "Short.", nil); err != nil {
			t.Fatalf("finish rework: %v", err)
		}
		// The rework re-explained once already: this row's head is the rework.
		again, err := st.AddQuestion(ctx, th.ID, "dev", "en", "summarize", rework.ID)
		if err != nil {
			t.Fatalf("add re-explain: %v", err)
		}
		if err := st.SetScope(ctx, again.ID, ask.Scope{Intent: ask.IntentRework}); err != nil {
			t.Fatalf("set scope: %v", err)
		}
		if err := st.Finish(ctx, again.ID, "Kurz.", nil); err != nil {
			t.Fatalf("finish: %v", err)
		}
		failing.ordinal = true

		srv := NewServer(deps)
		body := doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", again.ID), `{"audience":"ba"}`)

		if !strings.Contains(body, "event: error") || strings.Contains(body, "event: done") {
			t.Fatalf("want the turn to fail, got:\n%s", body)
		}
		if a.reworked {
			t.Error("a rework whose antecedent could not be read must not run")
		}
	})
}

// TestInternalError_aClosedTabIsNotADatabaseFault: the read failed because the
// reader left, and the log says so at Warn rather than paging anyone.
func TestInternalError_aClosedTabIsNotADatabaseFault(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()

	internalError(ctx, rec, "read thread scope failed", errLocked)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), errLocked.Error()) {
		t.Errorf("status = %d body = %q, want a bare 500", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") || strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("log = %q, want it at Warn", buf.String())
	}
}

// failingMemories cannot read the reader's rules.
type failingMemories struct{ *memory.Store }

func (failingMemories) List(context.Context, string) ([]memory.Row, error) {
	return nil, errLocked
}

// TestAsk_memoryUnreadableFailsBeforeTheStream: a reader's rules that could
// not be read are not a reader without rules. The answer would be written
// against what they told rongo, in a form they asked it never to use.
func TestAsk_memoryUnreadableFailsBeforeTheStream(t *testing.T) {
	t.Run("a typed question", func(t *testing.T) {
		a := &fakeAsker{tokens: []string{"x"}}
		srv, st, ms, _ := memoryServer(t, a)
		srv.deps.Memory = failingMemories{ms}

		req := httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(`{"question":"frage","audience":"ba"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "event:") {
			t.Errorf("status = %d body = %q, want a 500 before the stream", rec.Code, rec.Body.String())
		}
		// Not even the thread: a refusal must not leave an empty one behind.
		list, err := st.List(context.Background(), testSubject)
		if err != nil || len(list) != 0 {
			t.Errorf("threads = %+v (%v), want none", list, err)
		}
	})
	t.Run("a re-explain", func(t *testing.T) {
		a := &fakeAsker{reexplainTokens: []string{"y"}}
		srv, st, ms, db := memoryServer(t, a)
		id := seedAnsweredMessageWithSources(t, st, db)
		srv.deps.Memory = failingMemories{ms}

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/messages/%d/reexplain", id), strings.NewReader(`{"audience":"dev"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "event:") {
			t.Errorf("status = %d body = %q, want a 500 before the stream", rec.Code, rec.Body.String())
		}
	})
}
