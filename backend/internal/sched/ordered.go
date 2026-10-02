package sched

import (
	"context"
	"errors"
	"sync"
)

// Readers is how many database reads one turn runs at once. Small on purpose:
// every connection the pool opens is its own wasm instance of SQLite, and a
// turn that fanned out to twenty of them would pay for the memory of twenty
// to save the time of four.
const Readers = 4

// Ordered runs f over every item, at most limit at a time, and returns the
// results in the order of the items — never in the order they finished. A
// caller that concatenates them sees exactly what a plain loop would have
// built, which is what lets a measured, order-sensitive walk run its reads
// side by side.
//
// The first item to fail, by position, is the error returned; the rest are
// cancelled. By position rather than by time, so a turn that fails reports
// the same failure on every run.
func Ordered[T, R any](ctx context.Context, limit int, items []T, f func(context.Context, T) (R, error)) ([]R, error) {
	out := make([]R, len(items))
	if len(items) == 0 {
		return out, nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit == 1 || len(items) == 1 {
		for i, it := range items {
			r, err := f(ctx, it)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make([]error, len(items))
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	started := 0
	for i, it := range items {
		// Acquired here, not in the goroutine: once an item has failed, the
		// ones not yet started are skipped rather than started and cancelled.
		slots <- struct{}{}
		if ctx.Err() != nil {
			<-slots
			break
		}
		started++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			r, err := f(ctx, it)
			if err != nil {
				errs[i] = err
				cancel()
				return
			}
			out[i] = r
		}()
	}
	wg.Wait()
	// The earliest failure that is not merely the cancellation another one
	// caused; failing that — the caller's own context ended — the earliest.
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, context.Canceled) {
			return nil, err
		}
		if first == nil {
			first = err
		}
	}
	if first != nil {
		return nil, first
	}
	// Nothing failed, yet not everything ran: the caller's context ended
	// before the last item was started.
	if started < len(items) {
		return nil, ctx.Err()
	}
	return out, nil
}
