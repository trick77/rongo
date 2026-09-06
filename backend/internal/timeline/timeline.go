// Package timeline records what one turn did and when, so the activity trace
// the reader watched is still there after they leave the thread and come back.
//
// The recorder travels on the request context, the same way usage.Meter does,
// because the steps are not all emitted from one place: most come from the
// pipeline's status events, and "suggesting" is announced by the HTTP layer
// after the answer. A context that carries no recorder records nothing, which
// is what keeps indexing and the title call out of a turn's timeline.
//
// Times, not durations: what took how long is arithmetic the reader's browser
// already does for a live turn, and storing the raw instants means a partial
// timeline from a turn that broke is still readable.
package timeline

import (
	"context"
	"sync"
	"time"
)

// Step is one status event, with the moment it was announced.
type Step struct {
	// Step is the backend's one-word name: understanding, searching, routing,
	// gathering, answering, writing, suggesting.
	Step string `json:"step"`
	// At is the moment it was announced, in epoch milliseconds — the unit the
	// browser's own live trace uses, so a stored turn and a live one are read
	// by the same code.
	At int64 `json:"at"`
}

// Trace is the whole of one turn's timeline, as it is stored and served.
//
// StartedAt and EndedAt are the turn's own, not the first and last step's: the
// turn begins before it announces anything, and it closes after the last step
// rather than on it. Without them the closing row's total would be the span
// between two steps, which is not what the reader was shown.
type Trace struct {
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	Steps     []Step `json:"steps"`
}

// Recorder collects one turn's steps. Safe for concurrent use: the pipeline
// announces from goroutines the handler does not own.
type Recorder struct {
	mu      sync.Mutex
	started time.Time
	steps   []Step
}

// New starts a recorder at the moment the turn began.
func New() *Recorder { return &Recorder{started: time.Now()} }

// Record appends one step, at now.
func (r *Recorder) Record(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, Step{Step: step, At: time.Now().UnixMilli()})
}

// Close returns the turn's timeline, ended at now.
//
// A turn that announced nothing returns a zero Trace: there is no timeline to
// show for it, and storing a pair of instants with no steps between them would
// put an empty row under a question.
func (r *Recorder) Close() Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) == 0 {
		return Trace{}
	}
	steps := make([]Step, len(r.steps))
	copy(steps, r.steps)
	return Trace{StartedAt: r.started.UnixMilli(), EndedAt: time.Now().UnixMilli(), Steps: steps}
}

type recorderKey struct{}

// With attaches a recorder to the context. context.WithoutCancel keeps values,
// so the record context a turn is closed on still finds the recorder its
// request started.
func With(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

// From returns the recorder on the context, or nil.
func From(ctx context.Context) *Recorder {
	r, _ := ctx.Value(recorderKey{}).(*Recorder)
	return r
}

// Record writes one step into the context's recorder, if there is one.
func Record(ctx context.Context, step string) {
	if r := From(ctx); r != nil {
		r.Record(step)
	}
}
