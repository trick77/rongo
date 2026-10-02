package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/threads"
)

// answeredThread is a thread of owner's with one finished answer.
func answeredThread(t *testing.T, st *threads.Store, owner string) threads.Thread {
	t.Helper()
	ctx := context.Background()
	th, err := st.Create(ctx, owner, "How?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := st.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := st.Finish(ctx, m.ID, "Like so [1].", []ask.Citation{{Marker: 1, Repo: "r", Path: "a.go", StartLine: 1, EndLine: 2}}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return th
}

func feedbackPath(th threads.Thread) string {
	return fmt.Sprintf("/api/threads/%s/feedback", th.PublicID)
}

func TestFeedback_storesReadsAndClearsTheVerdict(t *testing.T) {
	srv, st := threadActions(t)
	th := answeredThread(t, st, testSubject)

	// Nothing given yet reads as null, not a 404: the thread is there.
	rec := act(srv, http.MethodGet, feedbackPath(th), "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("empty read = %d %q, want 200 null", rec.Code, rec.Body.String())
	}

	rec = act(srv, http.MethodPut, feedbackPath(th), `{"verdict":-1,"reason":"incomplete"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var put threads.Feedback
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if put.Verdict != -1 || put.Reason != "incomplete" || put.UpToMessageID == 0 {
		t.Errorf("put answered %+v, want the stored verdict with what it covers", put)
	}

	rec = act(srv, http.MethodGet, feedbackPath(th), "")
	var got threads.Feedback
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got != put {
		t.Errorf("read %+v, want %+v", got, put)
	}

	rec = act(srv, http.MethodDelete, feedbackPath(th), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	if _, found, _ := st.Feedback(context.Background(), testSubject, th.ID); found {
		t.Error("verdict still stored after delete")
	}
}

func TestFeedback_refusesAVerdictOrReasonOutsideTheSet(t *testing.T) {
	srv, st := threadActions(t)
	th := answeredThread(t, st, testSubject)

	for _, body := range []string{
		`{"verdict":0}`,
		`{"verdict":2}`,
		`{"verdict":-1,"reason":"Wrong"}`,
		`{"verdict":1,"reason":"wrong"}`, // a reason says what was off; a 👍 has none
		`not json`,
	} {
		rec := act(srv, http.MethodPut, feedbackPath(th), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}
	if _, found, _ := st.Feedback(context.Background(), testSubject, th.ID); found {
		t.Error("a refused verdict was stored")
	}
}

// Someone else's thread and no thread at all answer alike, every method.
func TestFeedback_someoneElsesThreadIsNotFound(t *testing.T) {
	srv, st := threadActions(t)
	theirs := answeredThread(t, st, otherSubject)
	_, _ = st.SetFeedback(context.Background(), otherSubject, theirs.ID, 1, "")

	for _, path := range []string{feedbackPath(theirs), "/api/threads/nosuchthreadaddress000/feedback"} {
		for _, c := range []struct{ method, body string }{
			{http.MethodGet, ""},
			{http.MethodPut, `{"verdict":-1}`},
			{http.MethodDelete, ""},
		} {
			rec := act(srv, c.method, path, c.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want 404", c.method, path, rec.Code)
			}
		}
	}
	if fb, _, _ := st.Feedback(context.Background(), otherSubject, theirs.ID); fb.Verdict != 1 {
		t.Errorf("their verdict = %+v, want it untouched", fb)
	}
}

func TestFeedback_aThreadWithNoAnswerYetIsNotFound(t *testing.T) {
	srv, st := threadActions(t)
	th, _ := st.Create(context.Background(), testSubject, "How?")

	rec := act(srv, http.MethodPut, feedbackPath(th), `{"verdict":1}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPublicShare_carriesNoFeedback(t *testing.T) {
	srv, st, _ := shareServer(t)
	th := sharedTurn(t, st, testSubject)
	if ok, err := st.SetFeedback(context.Background(), testSubject, th.ID, -1, "wrong"); err != nil || !ok {
		t.Fatalf("SetFeedback: ok=%v err=%v", ok, err)
	}
	sh := share(t, srv, th.PublicID)

	rec := getPublic(srv, "/api/shares/"+sh.Token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, leak := range []string{"verdict", "feedback", "wrong"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("shared page carries %q: %s", leak, rec.Body.String())
		}
	}
}

// feedbackFailingThreads stands in for a database that cannot answer: every
// feedback call fails, or with readOnly only the read after a good write.
type feedbackFailingThreads struct {
	*threads.Store
	readOnly bool
}

var errDisk = errors.New("disk full")

func (f *feedbackFailingThreads) Owns(ctx context.Context, subject string, id int64) (bool, error) {
	if f.readOnly {
		return f.Store.Owns(ctx, subject, id)
	}
	return false, errDisk
}

func (f *feedbackFailingThreads) SetFeedback(ctx context.Context, subject string, id int64, verdict int, reason string) (bool, error) {
	if f.readOnly {
		return f.Store.SetFeedback(ctx, subject, id, verdict, reason)
	}
	return false, errDisk
}

func (f *feedbackFailingThreads) ClearFeedback(context.Context, string, int64) (bool, error) {
	return false, errDisk
}

func (f *feedbackFailingThreads) Feedback(context.Context, string, int64) (threads.Feedback, bool, error) {
	return threads.Feedback{}, false, errDisk
}

func TestFeedback_aStoreThatFailsAnswers500(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		srv, st := threadActions(t)
		th := answeredThread(t, st, testSubject)
		srv.deps.Threads = &feedbackFailingThreads{Store: st, readOnly: readOnly}

		for _, c := range []struct{ method, body string }{
			{http.MethodGet, ""},
			{http.MethodPut, `{"verdict":1}`},
			{http.MethodDelete, ""},
		} {
			rec := act(srv, c.method, feedbackPath(th), c.body)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("readOnly=%v %s: status = %d, want 500", readOnly, c.method, rec.Code)
			}
		}
	}
}
