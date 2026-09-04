package ask

import (
	"strings"
	"testing"
)

// A diagram fence cites through the src arrays of its nodes. Those numbers
// are the same claim a marker in prose is, so they pass through the same
// renumberer and land in the same evidence panel.

// renumbered runs the whole answer through the renumberer in one go.
func renumbered(t *testing.T, sources int, text string) (string, *renumberer) {
	t.Helper()
	rn := newRenumberer(sources)
	out := rn.feed(text) + rn.flush()
	return out, rn
}

func TestRenumber_aDiagramNodeCitesThroughItsSrc(t *testing.T) {
	text := "Prose without markers.\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"NewGrant","src":[2]},` +
		`{"id":"b","label":"issueGrant","src":[1]}],"edges":[]}` +
		"\n```\n"

	out, rn := renumbered(t, 2, text)

	// First appearance decides the reader's number: source 2 is cited first.
	if !strings.Contains(out, `"src":[1]`) || !strings.Contains(out, `"src":[2]`) {
		t.Errorf("out = %q, want the src arrays renumbered 1 then 2", out)
	}
	cits := rn.citations(twoSources())
	if len(cits) != 2 || cits[0].Path != "backend/internal/httpapi/grant.go" {
		t.Errorf("citations = %+v, want both, the reader's [1] being prompt source 2", cits)
	}
}

func TestRenumber_aDiagramAndTheProseShareOneNumbering(t *testing.T) {
	// The invariant the feature rests on: a chip on a node is the chip in
	// the prose, so the same source may not read [1] in one and [2] in the
	// other.
	text := "The grant is created in store.go [1].\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[2]},{"id":"b","label":"y","src":[1]}],"edges":[]}` +
		"\n```"

	out, rn := renumbered(t, 2, text)

	if !strings.Contains(out, "store.go [1]") {
		t.Errorf("out = %q, want the prose marker to stay the reader's [1]", out)
	}
	if !strings.Contains(out, `"src":[2]`) || !strings.Contains(out, `"src":[1]`) {
		t.Errorf("out = %q, want the node on source 1 to read [1], as the prose does", out)
	}
	if len(rn.citations(twoSources())) != 2 {
		t.Errorf("citations = %+v, want one row per source", rn.citations(twoSources()))
	}
}

func TestRenumber_anIndexExpressionInADiagramLabelIsNotACitation(t *testing.T) {
	// Anchored on the "src" key, never on a bracket: a label is text.
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"parts[2]","src":[]}],"edges":[]}` +
		"\n```"

	out, rn := renumbered(t, 2, text)

	if !strings.Contains(out, `"label":"parts[2]"`) {
		t.Errorf("out = %q, want the label untouched", out)
	}
	if len(rn.citations(twoSources())) != 0 {
		t.Errorf("citations = %+v, want none: parts[2] is a label, not a claim", rn.citations(twoSources()))
	}
}

func TestRenumber_aCodeFenceNextToADiagramStillCitesNothing(t *testing.T) {
	text := "```go\nx := a[1]\n// \"src\":[2]\n```\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[2]}],"edges":[]}` +
		"\n```"

	out, rn := renumbered(t, 2, text)

	if !strings.Contains(out, "x := a[1]") || !strings.Contains(out, "// \"src\":[2]") {
		t.Errorf("out = %q, want the go fence untouched, src-shaped comment included", out)
	}
	cits := rn.citations(twoSources())
	if len(cits) != 1 || cits[0].Marker != 1 {
		t.Errorf("citations = %+v, want only the diagram's source, as the reader's [1]", cits)
	}
}

func TestRenumber_anInventedNumberInSrcIsLeftAloneAndNeverCited(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[1,9]}],"edges":[]}` +
		"\n```"

	out, rn := renumbered(t, 2, text)

	if !strings.Contains(out, `"src":[1,9]`) {
		t.Errorf("out = %q, want 9 left as it came", out)
	}
	if len(rn.citations(twoSources())) != 1 {
		t.Errorf("citations = %+v, want only the real source", rn.citations(twoSources()))
	}
}

func TestRenumber_aDiagramSplitAcrossTokensStillRenumbers(t *testing.T) {
	// The normal case: the fence, the key and the array all arrive in
	// pieces, and a half-written src may not reach the reader as it came.
	rn := newRenumberer(2)
	var got strings.Builder
	for _, tok := range []string{"Text.\n``", "`diag", "ram\n{\"nodes\":[{\"sr", "c\":[", "2", "]},{\"src\"", ": [1", ", 2]}]}\n``", "`\n"} {
		got.WriteString(rn.feed(tok))
	}
	got.WriteString(rn.flush())

	out := got.String()
	if !strings.Contains(out, `"src":[1]`) {
		t.Errorf("out = %q, want the first src renumbered to the reader's [1]", out)
	}
	if !strings.Contains(out, `"src":[2, 1]`) {
		t.Errorf("out = %q, want the grouped src renumbered, separators kept", out)
	}
	if !strings.HasPrefix(out, "Text.\n```diagram\n") {
		t.Errorf("out = %q, want the fence header intact", out)
	}
}

func TestRenumber_anUnclosedDiagramFenceStillEndsWhole(t *testing.T) {
	// A cut stream: the browser shows the block as text, so nothing may be
	// held back for a close that never comes.
	rn := newRenumberer(2)
	out := rn.feed("```diagram\n{\"nodes\":[{\"src\":[2") + rn.flush()

	if !strings.Contains(out, `"src":[2`) {
		t.Errorf("out = %q, want the partial array flushed as it came", out)
	}
}
