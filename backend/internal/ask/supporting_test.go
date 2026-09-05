package ask

import (
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

func TestWorthOfferingKeepsDocumentationOffTheCard(t *testing.T) {
	// Given a docs directory that matched well — prose in domain vocabulary is
	// exactly what a natural-language question matches — beside the code that
	// implements what it describes.
	hits := []retrieve.Hit{
		{ChunkID: 1, Repo: "rongo", Path: "docs/measurements/candidates.md", Score: 0.90},
		{ChunkID: 2, Repo: "rongo", Path: "backend/internal/llm/client.go", Score: 0.70},
	}

	// When the card is assembled.
	got := worthOffering(candidates(hits, moduleByDir))

	// Then the reader is never asked to choose between a document and the
	// mechanism it describes.
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want only the code: %+v", len(got), got)
	}
	if got[0].ModuleKey != "backend/internal/llm" {
		t.Errorf("candidate is %q, want the code module", got[0].ModuleKey)
	}
}

func TestWorthOfferingKeepsAModuleThatMixesDocsAndCode(t *testing.T) {
	// Dropping it would lose the mechanism in order to keep out its README.
	hits := []retrieve.Hit{
		{ChunkID: 1, Repo: "rongo", Path: "backend/internal/llm/README.md", Score: 0.90},
		{ChunkID: 2, Repo: "rongo", Path: "backend/internal/llm/client.go", Score: 0.85},
		{ChunkID: 3, Repo: "rongo", Path: "backend/internal/ask/route.go", Score: 0.80},
	}

	got := worthOffering(candidates(hits, moduleByDir))

	if len(got) != 2 {
		t.Fatalf("got %d candidates, want both modules: %+v", len(got), got)
	}
}

func TestWorthOfferingFallsBackWhenEverythingIsSupportingMaterial(t *testing.T) {
	// "What does the README say about X" and "how is this tested?" are real
	// questions. A filter that can empty the list would turn a weak answer
	// into no answer at all — and returning the leader alone would silently
	// answer from one of two, because dominates() cannot ask about a list of
	// one.
	hits := []retrieve.Hit{
		{ChunkID: 1, Repo: "rongo", Path: "docs/models.md", Score: 0.90},
		{ChunkID: 2, Repo: "peeq", Path: "docs/models.md", Score: 0.88},
	}

	got := worthOffering(candidates(hits, moduleByDir))

	if len(got) != 2 {
		t.Fatalf("got %d candidates, want both documents kept: %+v", len(got), got)
	}
}

func TestOnlySupportingTreatsTestsAndDocsAlike(t *testing.T) {
	cases := []struct {
		name string
		hits []retrieve.Hit
		want bool
	}{
		{"documentation", []retrieve.Hit{{Path: "docs/a.md"}, {Path: "README.md"}}, true},
		{"tests", []retrieve.Hit{{Path: "internal/llm/client_test.go"}}, true},
		{"a mix of both", []retrieve.Hit{{Path: "README.md"}, {Path: "internal/llm/client_test.go"}}, true},
		{"one code hit", []retrieve.Hit{{Path: "README.md"}, {Path: "internal/llm/client.go"}}, false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		if got := onlySupporting(c.hits); got != c.want {
			t.Errorf("onlySupporting(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
