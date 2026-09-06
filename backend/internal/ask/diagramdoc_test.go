package ask

import (
	"strings"
	"testing"
)

// A node of a diagram is a place in the mechanism, and a document is not one.
// A chip that opens a README under a step of the control flow says the step
// was read there. The markers are dropped rather than the node: no citation
// is better than a citation naming prose.

// docAndCode is a README followed by the file it describes, so a fixture can
// cite either and say which one it meant.
func docAndCode() []Source {
	return []Source{
		{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "README.md",
			StartLine: 1, EndLine: 40, Text: "A grant is issued per playback.", Reason: "hit"},
		{ChunkID: 2, Repo: "peeq", Branch: "master", Path: "backend/internal/playbackgrant/store.go",
			StartLine: 1, EndLine: 30, Text: "func NewGrant() {}", Reason: "hit"},
	}
}

// masked builds the renumberer the way the answer path does: with the mask
// that says which of its sources are documentation.
func masked(t *testing.T, sources []Source) *renumberer {
	t.Helper()
	rn := newRenumberer(len(sources))
	rn.docs = docMask(sources)
	return rn
}

func TestRenumber_aNodeRestingOnlyOnDocumentationCitesNothing(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"Grant","src":[1]},` +
		`{"id":"b","label":"NewGrant","src":[2]}],"edges":[]}` +
		"\n```\n"

	rn := masked(t, docAndCode())
	out := rn.feed(text) + rn.flush()

	if !strings.Contains(out, `"label":"Grant","src":[]`) {
		t.Errorf("out = %q, want the README node drawn with an empty src", out)
	}
	// The code node is the reader's [1]: the dropped marker never took a
	// number, so the numbering skips nothing.
	if !strings.Contains(out, `"label":"NewGrant","src":[1]`) {
		t.Errorf("out = %q, want the code node numbered [1]", out)
	}
	cits := rn.citations(docAndCode())
	if len(cits) != 1 || cits[0].Path != "backend/internal/playbackgrant/store.go" {
		t.Errorf("citations = %+v, want only the code source", cits)
	}
}

func TestRenumber_aNodeOnBothKeepsOnlyTheCode(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"NewGrant","src":[1,2]}],"edges":[]}` +
		"\n```\n"

	rn := masked(t, docAndCode())
	out := rn.feed(text) + rn.flush()

	if !strings.Contains(out, `"src":[1]`) {
		t.Errorf("out = %q, want the README marker dropped and the code kept", out)
	}
	if cits := rn.citations(docAndCode()); len(cits) != 1 {
		t.Errorf("citations = %+v, want the README uncited", cits)
	}
}

func TestRenumber_theProseStillCitesTheDocument(t *testing.T) {
	// The rule is about the picture, not about the answer. A claim resting
	// on a document is made in prose, where answerCommon has the model name
	// the document in the sentence that makes it.
	text := "The README states it [1].\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[1]}],"edges":[]}` +
		"\n```\n"

	rn := masked(t, docAndCode())
	out := rn.feed(text) + rn.flush()

	if !strings.Contains(out, "README states it [1]") {
		t.Errorf("out = %q, want the prose marker untouched", out)
	}
	if !strings.Contains(out, `"src":[]`) {
		t.Errorf("out = %q, want the node to carry no chip", out)
	}
	cits := rn.citations(docAndCode())
	if len(cits) != 1 || cits[0].Path != "README.md" {
		t.Errorf("citations = %+v, want the README, cited by the prose", cits)
	}
}

func TestRenumber_aDocumentBesideAnInventedNumberIsStillDropped(t *testing.T) {
	// This array takes the fallback branch, where the numbers are rewritten
	// where they stand because one of them is invented. A filter living
	// inside the sorted branch would leave the README chip on the node.
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[1, 9]}],"edges":[]}` +
		"\n```\n"

	rn := masked(t, docAndCode())
	out := rn.feed(text) + rn.flush()

	if strings.Contains(out, `"src":[1`) {
		t.Errorf("out = %q, want no reader number: the only real source was the README", out)
	}
	if cits := rn.citations(docAndCode()); len(cits) != 0 {
		t.Errorf("citations = %+v, want none", cits)
	}
}

func TestRenumber_aSequenceStepDropsItsDocumentationToo(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"sequence","actors":[{"id":"a","label":"Reader"},{"id":"b","label":"Store"}],` +
		`"steps":[{"from":"a","to":"b","label":"NewGrant","kind":"call","src":[1]}]}` +
		"\n```\n"

	rn := masked(t, docAndCode())
	out := rn.feed(text) + rn.flush()

	if !strings.Contains(out, `"src":[]`) {
		t.Errorf("out = %q, want the step to carry no chip", out)
	}
}

func TestRenumber_withoutAMaskNothingIsDropped(t *testing.T) {
	// The corpus and the older diagram tests construct with a count alone. A
	// renumberer that was never told what its sources are may not guess.
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","src":[1]}],"edges":[]}` +
		"\n```\n"

	rn := newRenumberer(2)
	out := rn.feed(text) + rn.flush()

	if !strings.Contains(out, `"src":[1]`) {
		t.Errorf("out = %q, want the marker kept", out)
	}
}

func TestDocMask_marksTheDocumentsAndNothingElse(t *testing.T) {
	got := docMask(docAndCode())

	if len(got) != 2 || !got[0] || got[1] {
		t.Errorf("docMask = %v, want the README marked and the code not", got)
	}
}
