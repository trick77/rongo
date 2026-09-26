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
//
// progress is the turn's step and elapsed time (turn.progress): an error
// names what failed, not that the turn spent thirteen minutes getting there.
func turnStopped(ctx context.Context, msg string, threadID int64, err error, progress ...any) {
	if threadWasDeleted(ctx) {
		slog.Info("turn stopped, thread deleted", append([]any{"thread", threadID}, progress...)...)
		return
	}
	// A cancelled turn fails on whatever it was waiting on, and that error
	// hides the cancel: sqlite-vec reports an interrupt as "SQL logic error:
	// chunks iter error", which reads as a corrupt database. The cause says
	// what happened; context.Canceled is the reader's connection closing.
	if ctx.Err() != nil {
		slog.Warn("turn cancelled", append([]any{"thread", threadID,
			"cause", context.Cause(ctx).Error(), "err", err}, progress...)...)
		return
	}
	slog.Error(msg, append([]any{"thread", threadID, "err", err}, progress...)...)
}

// recordMissed is recordFailed for the writes a turn can lose without losing
// anything a reader needs — a title, the follow-up pills.
func recordMissed(ctx context.Context, msg string, err error) {
	if err == nil || threadWasDeleted(ctx) {
		return
	}
	slog.Warn(msg, "err", err)
}

// claims is the clarifications being answered right now. A card is closed by
// the answer that came out of it, and that link is written only once the
// answer has landed — so between the choice and the answer the card looks
// open, and a second choice (a double-click, a second tab) would pass the
// "already answered" check, run a second full resume and link both rows to
// the card. The claim closes that window in process: one rongo, one
// database, so a map is the whole lock. A failed resume releases it, which
// keeps the rule that a failed turn leaves the card open for a retry.
type claims struct {
	mu   sync.Mutex
	held map[int64]struct{}
}

func newClaims() *claims {
	return &claims{held: map[int64]struct{}{}}
}

// claim takes the clarification for the turn about to answer it. ok is false
// when another turn holds it; release must be called exactly once when ok.
func (c *claims) claim(id int64) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, taken := c.held[id]; taken {
		return nil, false
	}
	c.held[id] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.held, id)
			c.mu.Unlock()
		})
	}, true
}
