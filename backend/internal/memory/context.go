package memory

import (
	"context"
	"sync"
)

// Holder is the reader's rules as one turn sees them. It travels on the
// context the way the usage meter does, and like the meter it is a pointer
// for a reason: a directive said in the same breath as a question is written
// mid-turn, and the answer that follows has to see it. A context value cannot
// be replaced once the turn is under way; the holder can.
//
// A nil holder on the context means memory is off for the deployment: the
// understanding step then asks for no directive and the answer gets no block.
type Holder struct {
	mu   sync.Mutex
	rows []Row
}

// NewHolder wraps the rows read at the top of a turn.
func NewHolder(rows []Row) *Holder {
	return &Holder{rows: rows}
}

// Rows is a copy of the current rules. Nil on a nil holder.
func (h *Holder) Rows() []Row {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Row, len(h.rows))
	copy(out, h.rows)
	return out
}

// Apply folds what a directive did into the rows: the deleted go, the new
// one comes first.
func (h *Holder) Apply(a Added) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	gone := map[int64]bool{}
	for _, id := range a.Deleted {
		gone[id] = true
	}
	kept := make([]Row, 0, len(h.rows)+1)
	if a.Row.ID != 0 {
		kept = append(kept, a.Row)
	}
	for _, r := range h.rows {
		if !gone[r.ID] {
			kept = append(kept, r)
		}
	}
	h.rows = kept
}

type holderKey struct{}

// With attaches the holder to the context.
func With(ctx context.Context, h *Holder) context.Context {
	return context.WithValue(ctx, holderKey{}, h)
}

// From returns the holder on the context, or nil when memory is off.
func From(ctx context.Context) *Holder {
	h, _ := ctx.Value(holderKey{}).(*Holder)
	return h
}
