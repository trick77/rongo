package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTurnProgress_namesTheStepAndHowLongItRan(t *testing.T) {
	// A turn that dies thirteen minutes in has to say where it was: the
	// error alone named the search, not that the search was all of it.
	tr := &turn{started: time.Now().Add(-3 * time.Second)}
	tr.mark("understanding")
	tr.mark("searching")

	attrs := tr.progress()

	if len(attrs) != 4 || attrs[0] != "step" || attrs[1] != "searching" || attrs[2] != "took" {
		t.Fatalf("progress() = %v, want step=searching and took", attrs)
	}
	took, err := time.ParseDuration(attrs[3].(string))
	if err != nil || took < 3*time.Second {
		t.Errorf("took = %v (%v), want at least 3s", attrs[3], err)
	}
}

func TestTurnProgress_beforeAnyStep(t *testing.T) {
	tr := &turn{started: time.Now()}
	if attrs := tr.progress(); attrs[1] != "starting" {
		t.Errorf("progress() = %v, want step=starting", attrs)
	}
}

// TestFinishEmptyAnswerRecordsFailure: a row with an empty answer and no
// error is what an unfinished turn looks like. The share ceiling stops below
// it, and FailOrphaned rewrites it at the next boot — so the browser was told
// "done" about a turn the record later calls failed. Failed now, plainly.
func TestFinishEmptyAnswerRecordsFailure(t *testing.T) {
	deps, st := askDeps(t, &fakeAsker{tokens: []string{" \n"}})

	body := postAsk(t, deps, `{"question":"how is sign-in done?","audience":"ba"}`).Body.String()

	if !strings.Contains(body, "event: error") || strings.Contains(body, "event: done") {
		t.Fatalf("want the turn failed, got:\n%s", body)
	}
	list, err := st.List(context.Background(), testSubject)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	msgs, err := st.Messages(context.Background(), testSubject, list[0].ID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages: %+v %v", msgs, err)
	}
	if msgs[0].Error == "" || msgs[0].Answer != "" {
		t.Errorf("row = %+v, want it recorded as failed", msgs[0])
	}
}
