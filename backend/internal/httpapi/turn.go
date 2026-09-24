package httpapi

import (
	"context"

	"github.com/trick77/rongo/internal/ask"
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
	msgID    int64
	meter    *usage.Meter
	steps    *timeline.Recorder
	st       *sseStream
	streamed bool
}

func (s *Server) beginTurn(ctx, record context.Context, msgID int64, meter *usage.Meter, steps *timeline.Recorder, st *sseStream) *turn {
	return &turn{s: s, ctx: ctx, record: record, msgID: msgID, meter: meter, steps: steps, st: st}
}

// events is what the pipeline reports through: status and detail onto the
// timeline and the stream, tokens onto the stream. Callers add OnNotice and
// OnMemory where a turn has them.
func (t *turn) events() ask.Events {
	return ask.Events{
		OnStatus: func(step string) {
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
// written from.
func (t *turn) finish(text string, cites []ask.Citation, sources []ask.Source) {
	if err := t.s.deps.Threads.Finish(t.record, t.msgID, text, cites); err != nil {
		recordFailed(t.ctx, "record answer failed", err)
	}
	if err := t.s.deps.Threads.SaveSources(t.record, t.msgID, sources); err != nil {
		recordFailed(t.ctx, "record sources failed", err)
	}
}

// parseAudience reads the wire value; anything but the Developer's is the
// Analyst's.
func parseAudience(s string) ask.Audience {
	if s == string(ask.AudienceDev) {
		return ask.AudienceDev
	}
	return ask.AudienceBA
}
