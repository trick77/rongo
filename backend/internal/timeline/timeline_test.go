package timeline

import (
	"context"
	"sync"
	"testing"
)

func TestRecorder_keepsTheStepsInTheOrderTheyWereAnnounced(t *testing.T) {
	r := New()
	r.Record("understanding")
	r.Record("searching")
	r.Record("writing")

	tr := r.Close()

	if len(tr.Steps) != 3 || tr.Steps[0].Step != "understanding" || tr.Steps[2].Step != "writing" {
		t.Fatalf("Steps = %+v, want the three in order", tr.Steps)
	}
	// The turn's own span: it began before the first step and closed after the
	// last, so neither end is a step's instant.
	if tr.StartedAt > tr.Steps[0].At || tr.EndedAt < tr.Steps[2].At {
		t.Errorf("span %d..%d does not contain the steps %+v", tr.StartedAt, tr.EndedAt, tr.Steps)
	}
}

func TestRecorder_aTurnThatAnnouncedNothingHasNoTimeline(t *testing.T) {
	// Two instants with nothing between them would put an empty frame under a
	// question.
	tr := New().Close()

	if tr.StartedAt != 0 || tr.EndedAt != 0 || len(tr.Steps) != 0 {
		t.Errorf("Trace = %+v, want the zero trace", tr)
	}
}

func TestRecord_withoutARecorderOnTheContextIsANoOp(t *testing.T) {
	// That is how indexing and the title call stay out of a turn's timeline:
	// their context never carries one.
	Record(context.Background(), "searching")

	if From(context.Background()) != nil {
		t.Error("From on a bare context returned a recorder")
	}
}

func TestRecord_writesIntoTheContextsRecorder(t *testing.T) {
	r := New()
	ctx := With(context.Background(), r)

	Record(ctx, "gathering")

	if steps := r.Close().Steps; len(steps) != 1 || steps[0].Step != "gathering" {
		t.Errorf("Steps = %+v, want the one recorded through the context", steps)
	}
}

func TestRecorder_recordsFromSeveralGoroutinesAtOnce(t *testing.T) {
	// The pipeline announces from goroutines the handler does not own.
	r := New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record("searching")
		}()
	}
	wg.Wait()

	if got := len(r.Close().Steps); got != 50 {
		t.Errorf("Steps = %d, want 50", got)
	}
}
