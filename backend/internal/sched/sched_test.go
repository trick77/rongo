package sched

import (
	"context"
	"testing"
	"time"
)

func TestJittered_staysWithinTwentyPercent(t *testing.T) {
	// Given: the point of the jitter is to stop every repository hitting its
	// forge in the same second, without drifting so far that a 30-minute poll
	// becomes an hour.
	base := 30 * time.Minute

	for i := 0; i < 200; i++ {
		got := Jittered(base)

		if got < time.Duration(float64(base)*0.8) || got > time.Duration(float64(base)*1.2) {
			t.Fatalf("Jittered(%v) = %v, want within ±20%%", base, got)
		}
	}
}

func TestJittered_passesThroughNonPositive(t *testing.T) {
	if got := Jittered(0); got != 0 {
		t.Errorf("Jittered(0) = %v, want 0", got)
	}
	if got := Jittered(-time.Second); got != -time.Second {
		t.Errorf("Jittered(-1s) = %v, want -1s", got)
	}
}

func TestSleep_returnsFalseWhenContextEnds(t *testing.T) {
	// Given: a context cancelled while the sleep is in flight.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// When
	start := time.Now()
	ok := Sleep(ctx, time.Hour)

	// Then: it returns promptly, not in an hour.
	if ok {
		t.Error("Sleep() = true after cancellation, want false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Sleep() took %v, want it to return as soon as the context ended", elapsed)
	}
}

func TestSleep_returnsTrueWhenTheTimerFires(t *testing.T) {
	ok := Sleep(context.Background(), time.Millisecond)

	if !ok {
		t.Error("Sleep() = false, want true when the timer fired normally")
	}
}

func TestSleep_reportsAnAlreadyCancelledContext(t *testing.T) {
	// Given: a zero duration takes the fast path, which must still honour a
	// context that is already done — otherwise a shutdown could be swallowed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if ok := Sleep(ctx, 0); ok {
		t.Error("Sleep(ctx, 0) = true for a cancelled context, want false")
	}
}
