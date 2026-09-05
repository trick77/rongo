package ask

import (
	"strconv"
	"strings"
	"testing"
)

// run feeds the tokens through a renumberer over n sources and returns what
// reached the reader, piece by piece, and as one text.
func run(n int, tokens ...string) ([]string, string) {
	r := newRenumberer(n)
	var out []string
	for _, tok := range tokens {
		if s := r.feed(tok); s != "" {
			out = append(out, s)
		}
	}
	if s := r.flush(); s != "" {
		out = append(out, s)
	}
	return out, strings.Join(out, "")
}

func TestRenumberer_aFenceSplitAcrossTokensStaysCode(t *testing.T) {
	// The closing fence arrives as "``" then "`": the two backticks are held
	// back, and the index expression before them is never read as a marker.
	_, got := run(3, "See [3]:\n``", "`go\nx := a[2]\n``", "`\nand [2].")

	if got != "See [1]:\n```go\nx := a[2]\n```\nand [2]." {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_anOpeningFenceSplitAcrossTokensStaysCode(t *testing.T) {
	// The OPEN arrives as "``" then "`", the way the close does. Read as an
	// empty inline span, the block's index expression became a citation the
	// model never made and the stray third backtick opened a fence that
	// swallowed the markers after it.
	r := newRenumberer(3)
	got := r.feed("See [3]:\n``") + r.feed("`go\nx := a[2]\n```\ndone [1]") + r.flush()

	if got != "See [1]:\n```go\nx := a[2]\n```\ndone [2]" {
		t.Errorf("got %q", got)
	}
	if len(r.order) != 2 || r.order[0] != 3 || r.order[1] != 1 {
		t.Errorf("order = %v, want the two markers in prose only", r.order)
	}
}

func TestRenumberer_aTokenEndingInAFenceDoesNotPanic(t *testing.T) {
	// "x````" opens the fence at the fourth-last byte and leaves one backtick
	// undecided: counting the trailing backticks over the whole buffer put
	// the cut before the cursor and sliced out of range.
	_, got := run(3, "x````", "go\ny\n```\n")

	if got != "x````go\ny\n```\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_anUnclosedFenceSwallowsTheRest(t *testing.T) {
	_, got := run(3, "Prose [3].\n```go\nname := args[2]\n", "more [1]")

	if got != "Prose [1].\n```go\nname := args[2]\nmore [1]" {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aHalfMarkerIsHeldBackNotStreamed(t *testing.T) {
	// A reader must never see "[10" and then "7]": the pieces reach them as
	// one renumbered marker.
	pieces, got := run(200, "cited [", "10", "7] here")

	if got != "cited [1] here" {
		t.Errorf("got %q", got)
	}
	for _, p := range pieces {
		if strings.Contains(p, "107") || p == "[" {
			t.Errorf("streamed %q, want the marker to arrive whole", pieces)
		}
	}
}

func TestRenumberer_aBracketThatIsNoMarkerIsText(t *testing.T) {
	_, got := run(3, "a [word] and [", "x] and [1", "]")

	if got != "a [word] and [x] and [1]" {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aRunOfMarkersIsSortedAscending(t *testing.T) {
	// The reader's numbering is minted on first use, so a run that reuses a
	// source cited earlier comes out of order. It is read as one run and
	// written back counting up.
	_, got := run(9, "a [3] b [7] c [5][3][7].")

	if got != "a [1] b [2] c [1][2][3]." {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aRunSortsNumericallyNotLexically(t *testing.T) {
	// Eleven sources cited before this run: [10] sorts after [2], never
	// before it.
	r := newRenumberer(20)
	for i := 1; i <= 11; i++ {
		r.feed("[" + strconv.Itoa(i) + "] ")
	}
	got := r.feed("run [11][2][10].") + r.flush()

	if !strings.HasSuffix(got, "run [2][10][11].") {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aRunSplitAcrossTokensIsStillOneRun(t *testing.T) {
	// The first group is complete where the token ends, so it may not be
	// emitted yet: the bracket that follows turns it into a run, and a
	// reader may never see the two halves in the order they were written.
	pieces, got := run(9, "see [3] and [7]. Then [5", "][3", "] done.")

	if got != "see [1] and [2]. Then [1][3] done." {
		t.Errorf("got %q", got)
	}
	for _, p := range pieces {
		if strings.HasSuffix(p, "[3]") {
			t.Errorf("streamed %q, want the run to arrive whole and sorted", pieces)
		}
	}
}

func TestRenumberer_aRunCitesASourceOnce(t *testing.T) {
	_, got := run(9, "a [3][7] b [7][3][7].")

	if got != "a [1][2] b [1][2]." {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aRunDoesNotReachOverANewline(t *testing.T) {
	// Two paragraphs, one ending on a marker and the next opening on one:
	// joining them into a run would eat the break between them.
	_, got := run(9, "one [7].\n\n[3] two [7][3]")

	if got != "one [1].\n\n[2] two [1][2]" {
		t.Errorf("got %q", got)
	}
}

func TestRenumberer_aRunWithAnInventedNumberKeepsItsOrder(t *testing.T) {
	// 12 is never a citation, and sorting the run would interleave the chips
	// with the plain text the UI drops that number to.
	_, got := run(9, "a [7][3] b [5][12][3].")

	if got != "a [1][2] b [3][12][2]." {
		t.Errorf("got %q", got)
	}
}
