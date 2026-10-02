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

// cancelsOnReturn answers every search and ends the caller's context as the
// last one returns.
type cancelsOnReturn struct {
	*fakeSearch
	cancel context.CancelFunc
}

func (s *cancelsOnReturn) Search(ctx context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	hits, err := s.fakeSearch.Search(ctx, q)
	s.cancel()
	return hits, err
}

func TestAComparisonKeepsItsHitsWhenOnlyTheCallerGaveUp(t *testing.T) {
	// Given searches that all succeed, under a context that ends meanwhile
	db := gatherDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	search := &cancelsOnReturn{
		fakeSearch: &fakeSearch{hits: []retrieve.Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go", Score: 1}}, indexed: []string{"peeq", "rongo"}},
		cancel:     cancel,
	}
	c := twoStepUpstream(t, threeReposReply, "x")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	// When
	got, err := p.searchScoped(ctx, "q", "", []string{"q"}, "", []string{"peeq", "rongo"}, nil, false)

	// Then no search failed, so nothing is reported as failed here: the next
	// stage reads the context for itself
	if err != nil || len(got) == 0 {
		t.Errorf("got %d hits, err %v; want the hits and no error", len(got), err)
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
