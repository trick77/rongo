// Package sched holds the loop primitives the background workers share:
// cancellable sleep and jittered intervals.
package sched

import (
	"context"
	"math/rand/v2"
	"time"
)

// Jittered spreads d by up to ±20%, so several repositories polled on the same
// interval do not all hit their forge in the same second.
func Jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.2
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
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
