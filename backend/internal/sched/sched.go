// Package sched holds the loop primitives the background workers share:
// cancellable sleep, jittered intervals and the heartbeat a long call logs
// while it runs.
package sched

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Heartbeat calls fn every interval until the returned stop is called or ctx
// ends, on its own goroutine. stop waits for that goroutine, so no beat lands
// after it returns and a caller may read what the beats recorded. A
// non-positive interval is OFF: stop is a no-op and fn never runs.
func Heartbeat(ctx context.Context, every time.Duration, fn func()) (stop func()) {
	if every <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if ctx.Err() != nil {
					return
				}
				fn()
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// Jittered spreads d by up to ±20%, so several repositories polled on the same
// interval do not all hit their forge in the same second.
func Jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.2
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread) //nolint:gosec // jitter and backoff, not a secret: no security property depends on this value
}

// Sleep waits for d or until ctx is done. It reports false if the context ended,
// so a caller can exit its loop without a second select.
func Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
