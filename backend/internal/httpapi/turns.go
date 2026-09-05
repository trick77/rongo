package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// errThreadDeleted is why a turn was cancelled, when what cancelled it was the
// thread it is being written into going away. The record writes at the end of a
// turn look for it: a turn cancelled because the reader closed the tab must
// still record how it ended, and a turn whose thread is gone must not try —
// every one of those writes hangs off a message row the cascade already took,
// so they would each log a foreign-key error for a row deleted on purpose.
var errThreadDeleted = errors.New("thread deleted")

// turns is the turns being answered right now, one entry per live stream, so
// that deleting a thread can stop the answer being written into it. Without it
// a delete mid-answer leaves the model calls running to completion — and paid
// for — against a thread that no longer exists.
type turns struct {
	mu   sync.Mutex
	next uint64
	live map[int64]map[uint64]context.CancelCauseFunc
}

func newTurns() *turns {
	return &turns{live: map[int64]map[uint64]context.CancelCauseFunc{}}
}

// add registers a live turn and returns the removal its handler defers. Turns
// are held per thread AND per token rather than one per thread: two tabs can
// ask into the same thread at the same time, and the second must not evict the
// first's cancel.
func (t *turns) add(threadID int64, cancel context.CancelCauseFunc) func() {
	if threadID == 0 {
		return func() {}
	}
	t.mu.Lock()
	token := t.next
	t.next++
	byToken := t.live[threadID]
	if byToken == nil {
		byToken = map[uint64]context.CancelCauseFunc{}
		t.live[threadID] = byToken
	}
	byToken[token] = cancel
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		byToken, ok := t.live[threadID]
		if !ok {
			return
		}
		delete(byToken, token)
		if len(byToken) == 0 {
			delete(t.live, threadID)
		}
	}
}

// cancel stops every turn in flight on a thread. Called once the delete has
// gone through, so cancelling someone's turn is a thing only the thread's owner
// can do.
func (t *turns) cancel(threadID int64) {
	t.mu.Lock()
	byToken := t.live[threadID]
	delete(t.live, threadID)
	t.mu.Unlock()
	// Outside the lock: never a callback under a mutex, and each of these
	// runs whatever the cancelled turn is waiting on.
	for _, cancel := range byToken {
		cancel(errThreadDeleted)
	}
}

// threadWasDeleted reports whether ctx is a turn stopped because its thread is
// gone, rather than one that failed or whose reader closed the tab.
func threadWasDeleted(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errThreadDeleted)
}

// recordFailed reports a record write that did not land, and says nothing when
// the reason is that the thread was deleted while the turn ran. Every one of
// those writes hangs off a message row the cascade already took, so they fail
// by design; logging them would fill the log with errors for rows a reader
// asked to be rid of. ctx is the TURN's context, never the record's: the record
// outlives the request precisely by dropping the cancellation this reads.
func recordFailed(ctx context.Context, msg string, err error) {
	if err == nil || threadWasDeleted(ctx) {
		return
	}
	slog.Error(msg, "err", err)
}

// turnStopped reports a turn that ended without an answer, and separates the
// one that ended because its thread was deleted from the ones that failed. A
// reader who deletes a thread mid-answer asked for exactly this; it is not an
// error, and logging it as one would put a red line in the log for every
// delete that landed on a live turn.
func turnStopped(ctx context.Context, msg string, threadID int64, err error) {
	if threadWasDeleted(ctx) {
		slog.Info("turn stopped, thread deleted", "thread", threadID)
		return
	}
	slog.Error(msg, "err", err)
}

// recordMissed is recordFailed for the writes a turn can lose without losing
// anything a reader needs — a title, the follow-up pills.
func recordMissed(ctx context.Context, msg string, err error) {
	if err == nil || threadWasDeleted(ctx) {
		return
	}
	slog.Warn(msg, "err", err)
}
