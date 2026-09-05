package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestTurns_cancelStopsEveryTurnOnTheThread(t *testing.T) {
	// Given two tabs answering into one thread, and one into another
	testee := newTurns()
	ctxA, cancelA := context.WithCancelCause(context.Background())
	defer cancelA(nil)
	ctxB, cancelB := context.WithCancelCause(context.Background())
	defer cancelB(nil)
	ctxOther, cancelOther := context.WithCancelCause(context.Background())
	defer cancelOther(nil)
	testee.add(1, cancelA)
	testee.add(1, cancelB)
	testee.add(2, cancelOther)

	// When
	testee.cancel(1)

	// Then
	for name, ctx := range map[string]context.Context{"first tab": ctxA, "second tab": ctxB} {
		if ctx.Err() == nil {
			t.Errorf("%s is still running", name)
		}
		if !errors.Is(context.Cause(ctx), errThreadDeleted) {
			t.Errorf("%s cause = %v, want errThreadDeleted", name, context.Cause(ctx))
		}
	}
	if ctxOther.Err() != nil {
		t.Error("the other thread's turn was cancelled too")
	}
}

func TestTurns_aFinishedTurnIsNotHeld(t *testing.T) {
	// Given a turn that has already ended
	testee := newTurns()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	remove := testee.add(1, cancel)
	remove()

	// When the thread is deleted afterwards
	testee.cancel(1)

	// Then nothing is left holding it: the registry is empty, so a long-lived
	// process does not accumulate one entry per thread it has ever answered.
	if ctx.Err() != nil {
		t.Error("a removed turn was cancelled")
	}
	testee.mu.Lock()
	defer testee.mu.Unlock()
	if len(testee.live) != 0 {
		t.Errorf("live = %v, want it empty", testee.live)
	}
}

func TestTurns_aThreadlessTurnRegistersNothing(t *testing.T) {
	// A re-explain resolves its thread from the message; id 0 means it has
	// none, and nothing may be filed under a thread that does not exist.
	testee := newTurns()
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	testee.add(0, cancel)()

	testee.mu.Lock()
	defer testee.mu.Unlock()
	if len(testee.live) != 0 {
		t.Errorf("live = %v, want it empty", testee.live)
	}
}

func TestRecordFailed_saysNothingAboutADeletedThread(t *testing.T) {
	// A write that failed because the row was deliberately deleted is not an
	// error anyone needs to read; one that failed for any other reason — a
	// reader who closed the tab, say — still is.
	var log bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	deleted, cancelDeleted := context.WithCancelCause(context.Background())
	cancelDeleted(errThreadDeleted)
	closed, cancelClosed := context.WithCancelCause(context.Background())
	cancelClosed(nil)

	recordFailed(deleted, "record answer failed", errors.New("FOREIGN KEY constraint failed"))
	recordMissed(deleted, "set thread title failed", errors.New("FOREIGN KEY constraint failed"))
	if log.Len() != 0 {
		t.Errorf("a deleted thread logged %q, want silence", log.String())
	}

	recordFailed(closed, "record answer failed", errors.New("disk is full"))
	recordMissed(closed, "record followups failed", errors.New("disk is full"))
	for _, want := range []string{"record answer failed", "record followups failed"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log = %q, want %q reported", log.String(), want)
		}
	}
}

func TestTurnStopped_readsADeleteAsWhatItIs(t *testing.T) {
	// The turn's own outcome, on the same rule: a turn cut short by a delete
	// did what it was told, and must not read as a failure in the log.
	var log bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	deleted, cancel := context.WithCancelCause(context.Background())
	cancel(errThreadDeleted)

	turnStopped(deleted, "turn failed", 7, context.Canceled)

	if strings.Contains(log.String(), "ERROR") {
		t.Errorf("log = %q, want no error for a deliberate delete", log.String())
	}
	if !strings.Contains(log.String(), "thread deleted") {
		t.Errorf("log = %q, want the delete accounted for", log.String())
	}
}
