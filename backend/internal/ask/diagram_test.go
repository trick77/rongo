package ask

import (
	"testing"
)

func markers(cits []Citation) []int {
	out := make([]int, 0, len(cits))
	for _, c := range cits {
		out = append(out, c.Marker)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCitationsFor_aDiagramNodeCitesThroughItsSrc(t *testing.T) {
	// A node's src array is the same claim-to-source link a marker in prose
	// is; the chip on the node opens the same viewer.
	text := "Prose without markers.\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"NewGrant","kind":"start","src":[1]},` +
		`{"id":"b","label":"issueGrant","kind":"step","src":[2]}],"edges":[{"from":"a","to":"b"}]}` +
		"\n```\n"

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{1, 2}) {
		t.Errorf("markers = %v, want [1 2] from the two nodes", markers(got))
	}
}

func TestCitationsFor_aSequenceStepCitesThroughItsSrc(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"sequence","actors":[{"id":"u","label":"UI"},{"id":"s","label":"Store"}],` +
		`"steps":[{"from":"u","to":"s","label":"issue","kind":"call","src":[2]}]}` +
		"\n```"

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{2}) {
		t.Errorf("markers = %v, want [2]", markers(got))
	}
}

func TestCitationsFor_anIndexExpressionInADiagramLabelIsNotACitation(t *testing.T) {
	// The fence body is never scanned as text: only the src arrays count.
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"parts[2]","kind":"step","src":[]}],"edges":[]}` +
		"\n```"

	got := citationsFor(text, twoSources())

	if len(got) != 0 {
		t.Errorf("citations = %+v, want none: parts[2] is a label, not a claim", got)
	}
}

func TestCitationsFor_invalidDiagramJSONContributesNothingButProseStillCounts(t *testing.T) {
	text := "The grant is created in store.go [1].\n```diagram\n{\"type\":\"flow\",\"nodes\":[{\"src\":[2]}\n```"

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{1}) {
		t.Errorf("markers = %v, want only the prose marker [1]", markers(got))
	}
}

func TestCitationsFor_anUnclosedDiagramFenceContributesNothing(t *testing.T) {
	// A stream cut inside the fence: the browser shows the raw block, and no
	// entry is minted for a picture that was never drawn.
	text := "Prose [1].\n```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","kind":"step","src":[2]}],"edges":[]}`

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{1}) {
		t.Errorf("markers = %v, want only [1]", markers(got))
	}
}

func TestCitationsFor_anInventedNumberInSrcDropsAlone(t *testing.T) {
	text := "```diagram\n" +
		`{"type":"flow","nodes":[{"id":"a","label":"x","kind":"step","src":[1,9]}],"edges":[]}` +
		"\n```"

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{1}) {
		t.Errorf("markers = %v, want [1]: 9 has no source and drops alone", markers(got))
	}
}

func TestCitationsFor_aCodeFenceStillContributesNothing(t *testing.T) {
	// Regression for the pre-diagram rule: an index expression in a go fence
	// is code, and a src-shaped key inside code is code too.
	text := "```go\nx := a[1]\n// \"src\":[2]\n```\nProse [2]."

	got := citationsFor(text, twoSources())

	if !equalInts(markers(got), []int{2}) {
		t.Errorf("markers = %v, want only the prose [2]", markers(got))
	}
}

func TestSplitFences_tagsBodiesAndClosure(t *testing.T) {
	prose, fences := splitFences("a\n```go\nx\n```\nb\n```diagram\n{}\n")

	if prose != "a\n\nb\n" {
		t.Errorf("prose = %q, want the fences blanked and the prose kept", prose)
	}
	if len(fences) != 2 {
		t.Fatalf("fences = %+v, want two", fences)
	}
	if fences[0].tag != "go" || fences[0].body != "x\n" || !fences[0].closed {
		t.Errorf("fence 0 = %+v", fences[0])
	}
	if fences[1].tag != "diagram" || fences[1].closed {
		t.Errorf("fence 1 = %+v, want an open diagram fence", fences[1])
	}
}
