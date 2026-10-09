package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/timeline"
	"github.com/trick77/rongo/internal/usage"
)

// turn is one answer being written: the row it lands on, the meter and the
// timeline it is watched through, and the stream it is sent on. handleAsk
// and handleReexplain end their turns the same way, and this is that way,
// once.
type turn struct {
	s   *Server
	ctx context.Context
	// record outlives the request: a reader who closes the tab mid-answer
	// cancels ctx, and writing the outcome on it would leave a row with
	// neither an answer nor an error — indistinguishable from a turn still
	// in flight.
	record   context.Context
	threadID int64
	msgID    int64
	meter    *usage.Meter
	steps    *timeline.Recorder
	st       *sseStream
	streamed bool
	// choice is the card a resumed turn answers, nil otherwise: finish writes
	// its link with the answer, so the two cannot disagree.
	choice *threads.Choice

	started time.Time
	mu      sync.Mutex
	step    string
}

// beginTurn opens the turn on ctx and returns the context the pipeline runs
// on: every paid call lands in one meter, the gates included, and every step
// it announces in one recorder, so the trace the reader watched is still
// there when they come back to the thread. The record context is derived
// AFTER both are attached, so the writes that outlive the request still see
// the meter.
func (s *Server) beginTurn(ctx context.Context, threadID, msgID int64, st *sseStream) (context.Context, *turn) {
	meter := usage.New()
	ctx = usage.WithMeter(ctx, meter)
	steps := timeline.New()
	ctx = timeline.With(ctx, steps)
	return ctx, &turn{s: s, ctx: ctx, record: context.WithoutCancel(ctx), threadID: threadID, msgID: msgID,
		meter: meter, steps: steps, st: st, started: time.Now()}
}

// stop ends a turn whose pipeline returned an error: the log line that says
// where it was, then the failure on the record and the stream. The message
// is generic: the error may quote an upstream body, and that is not
// something to hand a browser.
func (t *turn) stop(msg string, err error) {
	turnStopped(t.ctx, msg, t.threadID, err, t.progress()...)
	t.fail(failureMessage(err))
}

// mark notes the step the pipeline reported last.
func (t *turn) mark(step string) {
	t.mu.Lock()
	t.step = step
	t.mu.Unlock()
}

// progress is the step the turn was in and how long it had run, as log
// attributes for the line that reports how it stopped. took as .String():
// the JSON handler renders a Duration as nanoseconds.
func (t *turn) progress() []any {
	t.mu.Lock()
	step := t.step
	t.mu.Unlock()
	if step == "" {
		step = "starting"
	}
	return []any{"step", step, "took", time.Since(t.started).Round(time.Millisecond).String()}
}

// events is what the pipeline reports through: status and detail onto the
// timeline and the stream, tokens onto the stream. Callers add OnNotice and
// OnMemory where a turn has them.
func (t *turn) events() ask.Events {
	return ask.Events{
		OnStatus: func(step string) {
			t.mark(step)
			t.st.send("status", map[string]any{"step": step, "at": timeline.Record(t.ctx, step)})
		},
		OnDetail: func(step string, d map[string]any) {
			timeline.Detail(t.ctx, step, d)
			t.st.send("detail", map[string]any{"step": step, "detail": d})
		},
		OnToken: func(tok string) { t.streamed = true; t.st.send("token", map[string]any{"text": tok}) },
	}
}

// closeRecord stores what the turn paid for and how it was watched, and
// tells the browser the first of those, on EVERY exit: answered, asked back,
// found nothing, failed. The gates ran either way. Sent before the event that
// ends the turn, so the browser has the number whichever way the turn closed.
// A turn that paid for nothing (the first call never reached the upstream)
// sends nothing, the same as the stored record shows for it after a reload.
// The timeline is stored on the same terms and never sent: the browser has
// been drawing it live all along.
func (t *turn) closeRecord() {
	if err := t.s.deps.Threads.SaveSteps(t.record, t.msgID, t.steps.Close()); err != nil {
		recordFailed(t.ctx, "record steps failed", err)
	}
	calls := t.meter.Calls()
	if len(calls) == 0 {
		return
	}
	if err := t.s.deps.Threads.SaveUsage(t.record, t.msgID, calls); err != nil {
		recordFailed(t.ctx, "record usage failed", err)
	}
	t.st.send("usage", usage.Price(calls))
}

// fail records the turn as failed with why, closes the record and tells the
// browser. The id goes out with the failure, not only with a done: asking
// again is another attempt at THIS question, and the browser can only say so
// if it knows which row it is retrying. Without it a retry writes head 0 and
// the record claims the question was typed twice.
func (t *turn) fail(why string) {
	if err := t.s.deps.Threads.Fail(t.record, t.msgID, why); err != nil {
		recordFailed(t.ctx, "record turn failure failed", err)
	}
	t.closeRecord()
	t.st.send("error", map[string]any{"message": why, "message_id": t.msgID})
}

// finish records the answer with its citations and the sources it was
// written from, together: an answer whose sources did not land would read
// back as one whose basis is no longer indexed.
//
// False when nothing landed, and the turn is then recorded as failed: the row
// would otherwise hold neither an answer nor an error, which is what a turn
// still in flight looks like, while the browser was told it was done. An
// empty answer is the same row by another road — the share ceiling stops
// below it and FailOrphaned rewrites it at the next boot — so it fails too.
//
// A resumed turn's card link lands in the same write, so a failed finish
// leaves the card open for a retry and a finished one has closed it.
func (t *turn) finish(text string, cites []ask.Citation, sources []ask.Source) bool {
	if strings.TrimSpace(text) == "" {
		turnStopped(t.ctx, "empty answer", t.threadID, errEmptyAnswer, t.progress()...)
		t.fail(turnFailed)
		return false
	}
	if err := t.s.deps.Threads.FinishWithSources(t.record, t.msgID, text, cites, sources, t.choice); err != nil {
		recordFailed(t.ctx, "record answer failed", err)
		t.fail(turnFailed)
		return false
	}
	return true
}

// errEmptyAnswer is why a turn whose pipeline returned no text failed.
var errEmptyAnswer = errors.New("the answer is empty")

// finishTurn ends a turn that produced an answer: the citations, the follow-up
// questions it offers next, what it paid, and the event that closes it. All
// three answering paths - a fresh turn, a resumed clarification and a
// re-explain - end here, so the order they end in cannot drift apart.
//
// The suggestion call runs on the turn's context, meter and all: it is part
// of what the turn cost and is metered with the rest of it. scope is passed
// rather than read off the answer: only Run fills Answer.Scope, so a resumed
// or re-explained turn would hand the suggestion prompt an empty one and lose
// the rule that keeps a pill off a repository the index lacks. Every caller
// already has the scope in hand - they record it a few lines up.
func (t *turn) finishTurn(question string, answer ask.Answer, audience ask.Audience, scope ask.Scope, lang ask.Language) {
	// A turn with no sources was answered by a template, never a stream:
	// nothing found, no commits in the window, an instruction kept. The
	// text is on the record, and this is the one place it reaches the
	// browser live; without it the reader saw the trace close over an empty
	// answer until a reload. Only when nothing streamed: a text sent twice
	// is an answer read twice.
	sourceless := len(answer.Sources) == 0
	if !t.streamed && answer.Text != "" {
		t.st.send("token", map[string]any{"text": answer.Text})
	}
	t.st.send("citations", answer.Citations)
	t.suggestFollowups(question, answer, audience, scope, lang)
	t.closeRecord()
	// Language again, for the re-explain path: it opens no thread event, and
	// its turn is filed in the thread's language whatever it asked for.
	// Sourceless too: the page hides "Explain as Developer" on a turn that
	// has nothing to re-explain from, live as on a reload.
	t.st.send("done", map[string]any{"message_id": t.msgID, "language": string(lang), "sourceless": sourceless})
}

// suggestFollowups offers two or three questions to ask next, under the answer
// that prompted them.
//
// Synchronous, unlike the title: it is written FROM the answer, so it cannot
// start earlier, and running it inline is what puts its tokens in the turn's
// own usage report instead of a meter nobody reads until the next reload. The
// step is announced first, because a wait a person can see is a wait and a
// wait they cannot is a hang.
//
// An answer with no sources is the nothing-found reply. There is nothing to
// follow up on, and suggesting anything there would be inventing a question
// the index cannot answer.
func (t *turn) suggestFollowups(question string, answer ask.Answer, audience ask.Audience, scope ask.Scope, lang ask.Language) {
	// Never for a thread the reader deleted while it was being answered: the
	// message row this would be written to is already gone with it, and the
	// call would be paid for to fill a column nobody will ever read.
	if t.s.deps.Suggester == nil || len(answer.Sources) == 0 || threadWasDeleted(t.ctx) {
		return
	}
	t.st.send("status", map[string]any{"step": "suggesting", "at": timeline.Record(t.ctx, "suggesting")})
	// record, not ctx: a reader who closes the tab, reloads, or loses the
	// connection between the last word and this call cancelled the request,
	// and with it the only chance this answer ever had at suggestions - the
	// column is written once, here, and nothing goes back for it later. The
	// answer itself is already stored on record a few lines up for the same
	// reason. The meter rides along: WithoutCancel keeps the values.
	call, cancel := context.WithTimeout(t.record, followupsCallTimeout)
	defer cancel()
	// Dropping the request's cancellation does not mean dropping the thread's:
	// a reader who deletes the thread WHILE this call is running is owed the
	// same stop as one who deleted it a moment earlier, and the row the answer
	// would be written to is going with it either way.
	defer context.AfterFunc(t.ctx, func() {
		if threadWasDeleted(t.ctx) {
			cancel()
		}
	})()
	qs := t.s.deps.Suggester(call, question, answer.Text, audience, answer.Sources, scope, lang)
	if len(qs) == 0 {
		return
	}
	if err := t.s.deps.Threads.SaveFollowups(t.record, t.msgID, qs); err != nil {
		// The pills are worth a warning and nothing more: the answer is
		// written and the turn is finished either way.
		recordMissed(t.ctx, "record followups failed", err)
	}
	t.st.send("followups", qs)
}

// parseAudience reads the wire value; anything but the Developer's is the
// Analyst's.
func parseAudience(s string) ask.Audience {
	if s == string(ask.AudienceDev) {
		return ask.AudienceDev
	}
	return ask.AudienceBA
}
