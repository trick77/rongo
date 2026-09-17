package ask

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeModels is an index of process models in memory: repo -> path -> body.
type fakeModels struct {
	files map[string]map[string]string
	err   error
}

func (f fakeModels) Models(_ context.Context, repo string) ([]ModelRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []ModelRef
	for p := range f.files[repo] {
		out = append(out, ModelRef{Path: p, SHA: "abc1234"})
	}
	return out, nil
}

func (f fakeModels) ReadModel(_ context.Context, repo, path, _ string) ([]byte, error) {
	body, ok := f.files[repo][path]
	if !ok {
		return nil, errors.New("no such model")
	}
	return []byte(body), nil
}

func orderIntakeModel(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "symbols", "testdata", "order-intake.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// expressShipping is the model the order-intake call activity runs.
const expressShipping = `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <process id="express-shipping" isExecutable="true">
    <startEvent id="s" />
    <serviceTask id="book" name="Book courier" />
    <endEvent id="e" />
    <sequenceFlow id="f1" sourceRef="s" targetRef="book" />
    <sequenceFlow id="f2" sourceRef="book" targetRef="e" />
  </process>
</definitions>`

func TestDescribeProcessesListsTheCitedModelAndWhatItCalls(t *testing.T) {
	p := &Pipeline{models: fakeModels{files: map[string]map[string]string{
		"shop": {
			"workflow/order-intake.bpmn":     orderIntakeModel(t),
			"workflow/express-shipping.bpmn": expressShipping,
			"workflow/unrelated.bpmn":        strings.Replace(expressShipping, "express-shipping", "returns", 1),
		},
	}}}
	sources := []Source{
		{Repo: "shop", Path: "src/Order.java"},
		{Repo: "shop", Path: "workflow/order-intake.bpmn", Symbol: "Validate order"},
		{Repo: "shop", Path: "workflow/order-intake.bpmn", Symbol: "sequence flows"},
	}
	got := p.describeProcesses(context.Background(), sources)

	if !strings.Contains(got, `Process "order-intake" in shop/workflow/order-intake.bpmn:`) {
		t.Errorf("cited model missing:\n%s", got)
	}
	if !strings.Contains(got, `calls process "express-shipping" in shop/workflow/express-shipping.bpmn`) {
		t.Errorf("call not resolved to its file:\n%s", got)
	}
	if !strings.Contains(got, `Process "express-shipping" in shop/workflow/express-shipping.bpmn:`) ||
		!strings.Contains(got, "- Book courier (serviceTask)") {
		t.Errorf("called model not listed:\n%s", got)
	}
	if strings.Contains(got, `"returns"`) {
		t.Errorf("a model nothing cites or calls was listed:\n%s", got)
	}
	if strings.Count(got, `Process "order-intake"`) != 1 {
		t.Errorf("cited model listed more than once:\n%s", got)
	}
}

func TestDescribeProcessesIsEmptyWithoutModelsOrCitations(t *testing.T) {
	sources := []Source{{Repo: "shop", Path: "workflow/order-intake.bpmn"}}
	if got := (&Pipeline{}).describeProcesses(context.Background(), sources); got != "" {
		t.Errorf("no models wired, got %q", got)
	}
	p := &Pipeline{models: fakeModels{files: map[string]map[string]string{"shop": {"workflow/order-intake.bpmn": orderIntakeModel(t)}}}}
	if got := p.describeProcesses(context.Background(), []Source{{Repo: "shop", Path: "src/Order.java"}}); got != "" {
		t.Errorf("no model cited, got %q", got)
	}
}

func TestDescribeProcessesSurvivesAnUnreadableIndex(t *testing.T) {
	p := &Pipeline{models: fakeModels{err: errors.New("db locked")}}
	sources := []Source{{Repo: "shop", Path: "workflow/order-intake.bpmn"}}
	if got := p.describeProcesses(context.Background(), sources); got != "" {
		t.Errorf("got %q, want nothing rather than a failed turn", got)
	}
	// A model that is not XML is skipped, the rest still listed.
	p = &Pipeline{models: fakeModels{files: map[string]map[string]string{"shop": {
		"workflow/order-intake.bpmn": orderIntakeModel(t),
		"workflow/broken.bpmn":       "<definitions><process",
	}}}}
	if got := p.describeProcesses(context.Background(), sources); !strings.Contains(got, `Process "order-intake"`) {
		t.Errorf("one broken model cost the listing:\n%s", got)
	}
}

func TestTheProcessListingReachesThePromptAndIsNeverCited(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	listing := "Process \"order-intake\" in shop/workflow/order-intake.bpmn:\n- Validate order (serviceTask) -> Charge payment\n"
	_, err := NewAnswerer(c).Answer(context.Background(), "Walk me through order intake", AudienceBA, LanguageEN,
		bothReposSources(), Scope{Known: []string{"peeq", "rongo"}, Processes: listing}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "- Validate order (serviceTask) -> Charge payment") {
		t.Errorf("the wiring must reach the model:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "never this listing") {
		t.Errorf("the block must say it is not a source:\n%s", *prompt)
	}
	// The listing is order plus branch conditions, a flowchart in text, and
	// the block asks for it drawn: the strongest signal the prompt has that
	// the answer is a process.
	if !strings.Contains(*prompt, "draw it as the answer's diagram") {
		t.Errorf("the block must ask for the walk as the diagram:\n%s", *prompt)
	}
	// And nothing of it without a listing.
	c, prompt, _ = streamUpstream(t, "x")
	_, err = NewAnswerer(c).Answer(context.Background(), "Walk me through order intake", AudienceBA, LanguageEN,
		bothReposSources(), Scope{Known: []string{"peeq", "rongo"}}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*prompt, "process models among the sources") || strings.Contains(*prompt, "draw it as the answer's diagram") {
		t.Errorf("an empty listing must add nothing:\n%s", *prompt)
	}
}

func TestDescribeProcessesStopsAtTheCap(t *testing.T) {
	// A model whose listing alone exceeds the cap is left out and the cut is
	// named; a small one after it still fits.
	var big strings.Builder
	big.WriteString(`<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL"><process id="big" isExecutable="true">`)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&big, `<serviceTask id="t%d" name="A task with a long enough label to cost characters" />`, i)
	}
	big.WriteString(`</process></definitions>`)
	p := &Pipeline{models: fakeModels{files: map[string]map[string]string{"shop": {
		"workflow/big.bpmn":   big.String(),
		"workflow/small.bpmn": expressShipping,
	}}}}
	got := p.describeProcesses(context.Background(), []Source{
		{Repo: "shop", Path: "workflow/big.bpmn"}, {Repo: "shop", Path: "workflow/small.bpmn"},
	})
	if strings.Contains(got, `Process "big"`) || !strings.Contains(got, `Process "express-shipping"`) || !strings.Contains(got, "left out for length") {
		t.Errorf("cap not applied as expected:\n%.300s", got)
	}
}
