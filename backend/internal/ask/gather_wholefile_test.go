package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// TestGather_readsTheWholeOfASmallFileAHitSitsIn: the two unique misses the
// walk could not reach were "a constant explained in a comment three levels
// down a large file" — the hit was in the file, the answer was in the chunk
// next to it, and no symbol linked them. A small file is read whole, the way
// every harness reads the file a search pointed at; cited lines stay each
// chunk's own.
func TestGather_readsTheWholeOfASmallFileAHitSitsIn(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "sched.go", 1, 21, 40, "run", "func run() { if age > oldAfter { park() } }")
	seedChunk(t, db, "sched.go", 0, 1, 20, "oldAfter", "// oldAfter is when a video counts as old material.\nconst oldAfter = 30")
	seedChunk(t, db, "sched.go", 2, 41, 60, "park", "func park() {}")

	got, err := NewGatherer(db, GatherOptions{MaxHops: 0, TokenBudget: 10000, WholeFileTokens: 1500}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("sources = %v, want the hit and both of its neighbours", paths(got))
	}
	if got[0].StartLine != 21 || got[0].Reason != "hit" {
		t.Errorf("the hit must come first, unchanged: %+v", got[0])
	}
	for _, s := range got[1:] {
		if s.Reason != "file:whole" || s.Hop != 0 {
			t.Errorf("neighbour = %+v, want reason file:whole at hop 0", s)
		}
	}
}

// TestGather_readsOnlyTheHitsOwnSymbolOutOfALargeFile: a large file is not
// read whole — that is how a hit in a thousand-line controller would eat the
// budget — but a symbol whose region was cut into several chunks is read
// across all of them, because half a function is not a mechanism.
func TestGather_readsOnlyTheHitsOwnSymbolOutOfALargeFile(t *testing.T) {
	db := gatherDB(t)
	big := strings.Repeat("x ", 4000) // ~2000 tokens: over the 1500 whole-file limit
	hitID := seedChunk(t, db, "ctl.go", 1, 21, 40, "handle", "func handle() { part one }")
	seedChunk(t, db, "ctl.go", 2, 41, 60, "handle", "// still handle: part two")
	seedChunk(t, db, "ctl.go", 0, 1, 20, "other", "func other() { "+big+" }")
	seedChunk(t, db, "ctl.go", 3, 61, 80, "third", "func third() {}")

	got, err := NewGatherer(db, GatherOptions{MaxHops: 0, TokenBudget: 10000, WholeFileTokens: 1500}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var lines []int
	for _, s := range got {
		lines = append(lines, s.StartLine)
	}
	if len(got) != 2 || got[1].StartLine != 41 || got[1].Reason != "file:symbol" {
		t.Errorf("sources start at %v (%+v), want the hit and the rest of its symbol only", lines, got)
	}
}

// TestGather_wholeFileOffLeavesTheWalkAsItWas: the option is zero for the
// harness arm that measures what it buys, and then nothing but the hit and
// the symbol walk arrive.
func TestGather_wholeFileOffLeavesTheWalkAsItWas(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "sched.go", 1, 21, 40, "run", "func run() {}")
	seedChunk(t, db, "sched.go", 0, 1, 20, "oldAfter", "const oldAfter = 30")

	got, err := NewGatherer(db, GatherOptions{MaxHops: 0, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("sources = %v, want the hit alone with whole-file reading off", paths(got))
	}
}
