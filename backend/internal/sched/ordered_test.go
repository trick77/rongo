package sched

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrdered_returnsResultsInItemOrderWhateverFinishedFirst(t *testing.T) {
	// Given items whose work finishes in reverse
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}

	// When
	got, err := Ordered(context.Background(), 4, items, func(_ context.Context, i int) (string, error) {
		time.Sleep(time.Duration(len(items)-i) * 2 * time.Millisecond)
		return fmt.Sprint(i), nil
	})

	// Then
	if err != nil {
		t.Fatalf("Ordered() err = %v", err)
	}
	for i, s := range got {
		if s != fmt.Sprint(i) {
			t.Fatalf("got %v, want the items' own order", got)
		}
	}
}

func TestOrdered_neverRunsMoreThanTheLimitAtOnce(t *testing.T) {
	var running, peak atomic.Int32

	_, err := Ordered(context.Background(), 3, make([]int, 20), func(context.Context, int) (int, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
		return 0, nil
	})

	if err != nil {
		t.Fatalf("Ordered() err = %v", err)
	}
	if peak.Load() > 3 {
		t.Errorf("ran %d at once, want at most 3", peak.Load())
	}
}

func TestOrdered_reportsTheFailureItselfNotTheCancellationItCaused(t *testing.T) {
	// Given the last item fails at once and the earlier ones wait on their
	// context, as a query does
	boom := errors.New("database is locked")
	items := []int{0, 1, 2}

	// When
	_, err := Ordered(context.Background(), 3, items, func(ctx context.Context, i int) (int, error) {
		if i == 2 {
			return 0, boom
		}
		<-ctx.Done()
		return 0, fmt.Errorf("read %d: %w", i, ctx.Err())
	})

	// Then
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the failure that stopped the others", err)
	}
}

func TestOrdered_stopsStartingWorkOnceOneItemFailed(t *testing.T) {
	var started atomic.Int32

	_, err := Ordered(context.Background(), 2, make([]int, 50), func(context.Context, int) (int, error) {
		started.Add(1)
		return 0, errors.New("nope")
	})

	if err == nil {
		t.Fatal("Ordered() succeeded")
	}
	if started.Load() == 50 {
		t.Error("every item was started after the first had already failed")
	}
}

func TestOrdered_aCallersCancellationIsReportedAsThat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Ordered(ctx, 4, []int{1, 2, 3}, func(ctx context.Context, _ int) (int, error) {
		return 0, ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
}

func TestOrdered_oneAtATimeIsAPlainLoop(t *testing.T) {
	var order []int

	got, err := Ordered(context.Background(), 1, []int{3, 1, 2}, func(_ context.Context, i int) (int, error) {
		order = append(order, i)
		return i * 2, nil
	})

	if err != nil || fmt.Sprint(got) != "[6 2 4]" || fmt.Sprint(order) != "[3 1 2]" {
		t.Errorf("got %v in order %v, err %v", got, order, err)
	}
	if _, err := Ordered(context.Background(), 0, []int{1}, func(context.Context, int) (int, error) {
		return 0, errors.New("nope")
	}); err == nil {
		t.Error("a failing item under limit 0 returned no error")
	}
	if got, err := Ordered(context.Background(), 4, nil, func(context.Context, int) (int, error) { return 0, nil }); err != nil || len(got) != 0 {
		t.Errorf("no items: got %v, err %v", got, err)
	}
}
