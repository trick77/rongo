package ask

import (
	"context"
	"strings"
	"testing"
)

// TestAnswer_testSourcesAreLabelledAndTheRuleSaysCiteTheMechanism: a test
// beside the code it exercises reaches the prompt marked as one, so the
// model does not read a fake as the client, and the rule says which of the
// two a claim about the mechanism cites.
func TestAnswer_testSourcesAreLabelledAndTheRuleSaysCiteTheMechanism(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	sources := []Source{
		{ChunkID: 1, Repo: "llmwire", Branch: "master", Path: "registry.go", Symbol: "NewRegistry",
			StartLine: 78, EndLine: 132, Text: "func NewRegistry(doc []byte) (*Registry, error) {", Reason: "hit"},
		{ChunkID: 2, Repo: "llmwire", Branch: "master", Path: "registry_test.go", Symbol: "TestNewRegistry_Rejects",
			StartLine: 426, EndLine: 450, Text: "func TestNewRegistry_Rejects(t *testing.T) {", Reason: "hit"},
	}
	if _, err := NewAnswerer(c).Answer(context.Background(), "How are profiles loaded?", AudienceBA, LanguageEN, sources, Scope{Known: []string{"llmwire"}}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	p := *prompt
	if !strings.Contains(p, "registry_test.go:426-450 (TestNewRegistry_Rejects) (test)") {
		t.Errorf("the test source is not labelled:\n%s", p)
	}
	if strings.Contains(p, "registry.go:78-132 (NewRegistry) (test)") {
		t.Error("the code beside the test got a test label")
	}
	if !strings.Contains(p, `marked "(test)"`) {
		t.Error("the prompt does not say what a source marked (test) is")
	}
}
