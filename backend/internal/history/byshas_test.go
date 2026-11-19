package history

import (
	"context"
	"fmt"
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

func TestBySHAs_aRangePastSQLitesVariableLimitStillReads(t *testing.T) {
	// A release range is as long as BACKEND_HISTORY_DEPTH allows; bound in one
	// IN list it failed the release turn with "too many SQL variables".
	s := fixture(t)
	shas := make([]string, 0, 40001)
	shas = append(shas, "c3")
	for i := range 40000 {
		shas = append(shas, fmt.Sprintf("unrecorded-%d", i))
	}
	shas = append(shas, "c1")

	got, err := s.BySHAs(context.Background(), "shop", shas)
	if err != nil {
		t.Fatalf("BySHAs: %v", err)
	}
	if len(got) != 2 || got[0].SHA != "c3" || got[1].SHA != "c1" {
		t.Fatalf("got = %+v, want c3 then c1", got)
	}
}
