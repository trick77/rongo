package ask

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/retrieve"
)

// oneSideFails fails the search of one repository and holds every other one
// until its context ends, the way a search waiting on its reranker call does.
type oneSideFails struct {
	*fakeSearch
	failing string
	err     error
}

func (s *oneSideFails) Search(ctx context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	if len(q.Repos) == 1 && q.Repos[0] == s.failing {
		return nil, s.err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("still searching after a sibling failed")
	}
}

func TestAComparisonStopsItsOtherSearchesWhenOneFails(t *testing.T) {
	// Given a comparison of three repositories whose last search fails
	db := gatherDB(t)
	boom := errors.New("embedding endpoint down")
	search := &oneSideFails{fakeSearch: &fakeSearch{indexed: []string{"peeq", "rongo", "go-sqlite3"}}, failing: "go-sqlite3", err: boom}
	c := twoStepUpstream(t, threeReposReply, "x")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	// When
	_, err := p.searchScoped(context.Background(), "q", "", []string{"q"}, "", []string{"peeq", "rongo", "go-sqlite3"}, nil, false)

	// Then the others are cancelled rather than paid for to the end, and the
	// turn reports the failure that caused it, not a sibling's cancellation
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the search failure itself", err)
	}
}
