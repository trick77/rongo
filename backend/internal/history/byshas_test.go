package history

import (
	"context"
	"testing"
)

func TestBySHAs_returnsTheRowsInTheOrderAskedAndSkipsTheUnrecorded(t *testing.T) {
	s := fixture(t)

	got, err := s.BySHAs(context.Background(), "shop", []string{"c3", "nope", "c1", "l1"})
	if err != nil {
		t.Fatalf("BySHAs: %v", err)
	}

	// l1 is loom's, nope was never recorded: neither comes back, and the
	// two that do keep the caller's order, which is the range's.
	if len(got) != 2 || got[0].SHA != "c3" || got[1].SHA != "c1" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].ID == 0 || got[0].Branch != "main" || got[0].Subject == "" || len(got[0].Paths) != 1 {
		t.Errorf("row = %+v, want id, branch, subject and paths", got[0])
	}
}
