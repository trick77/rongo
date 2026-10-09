package sched

import (
	"context"
	"sync"
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

func TestHeartbeat_firesUntilStoppedAndNotAfter(t *testing.T) {
	// Given: a beat every millisecond, counted under a lock because the
	// ticker runs on its own goroutine.
	var mu sync.Mutex
	beats := 0
	stop := Heartbeat(context.Background(), time.Millisecond, func() {
		mu.Lock()
		beats++
		mu.Unlock()
	})

	// When: it has had time to fire, and is then stopped.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := beats
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	mu.Lock()
	atStop := beats
	mu.Unlock()
	time.Sleep(5 * time.Millisecond)

	// Then: it fired, and stop returned only once no beat could follow —
	// a caller reads what the beats recorded right after stop.
	if atStop == 0 {
		t.Fatal("Heartbeat() never fired")
	}
	mu.Lock()
	defer mu.Unlock()
	if beats != atStop {
		t.Errorf("beats after stop = %d, want %d: stop must wait for the goroutine", beats, atStop)
	}
}

func TestHeartbeat_endsWithTheContext(t *testing.T) {
	// Given: a context cancelled while the ticker runs.
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	beats := 0
	stop := Heartbeat(ctx, time.Millisecond, func() {
		mu.Lock()
		beats++
		mu.Unlock()
	})
	defer stop()

	// When
	cancel()
	time.Sleep(5 * time.Millisecond)
	mu.Lock()
	atCancel := beats
	mu.Unlock()
	time.Sleep(10 * time.Millisecond)

	// Then: nothing fires once the context is done.
	mu.Lock()
	defer mu.Unlock()
	if beats != atCancel {
		t.Errorf("beats after cancel = %d, want %d", beats, atCancel)
	}
}

func TestHeartbeat_nonPositiveIntervalIsOff(t *testing.T) {
	// Given: a disabled heartbeat. time.NewTicker panics below 1ns, so the
	// guard is what lets a caller switch the beat off with -1.
	fired := false
	stop := Heartbeat(context.Background(), -1, func() { fired = true })

	// When
	time.Sleep(5 * time.Millisecond)
	stop()

	// Then
	if fired {
		t.Error("Heartbeat(-1) fired, want it off")
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
