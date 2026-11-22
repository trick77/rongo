package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

// shapeTally is one run's diagrams by shape, "none" for an answer drawn as
// prose alone, sorted by name so two runs print alike:
// "classDiagram 2, none 3, swimlane 1". Failed and unjudged turns carry no
// answer and are left out.
func shapeTally(records []answerRecord, run int) string {
	n := map[string]int{}
	for _, r := range records {
		if r.Run != run || r.Answer == "" {
			continue
		}
		shape := r.Diagram
		if shape == "" {
			shape = "none"
		}
		n[shape]++
	}
	if len(n) == 0 {
		return "no answers"
	}
	keys := make([]string, 0, len(n))
	for k := range n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, n[k])
	}
	return strings.Join(parts, ", ")
}

func TestShapeTally_countsEveryAnsweredTurnOnce(t *testing.T) {
	records := []answerRecord{
		{Run: 1, Answer: "a", Diagram: "swimlane"},
		{Run: 1, Answer: "b", Diagram: "classDiagram"},
		{Run: 1, Answer: "c", Diagram: "swimlane"},
		{Run: 1, Answer: "d"},
		{Run: 1, Err: "failed"},
		{Run: 2, Answer: "e", Diagram: "flowchart"},
	}
	if got, want := shapeTally(records, 1), "classDiagram 1, none 1, swimlane 2"; got != want {
		t.Errorf("run 1 = %q, want %q", got, want)
	}
	if got, want := shapeTally(records, 3), "no answers"; got != want {
		t.Errorf("run 3 = %q, want %q", got, want)
	}
}

// The lanes-and-classes questions (2026-11-22-lanes-and-classes.md) run on
// the flow corpus's database with their own files:
//
//	BACKEND_EVAL_FLOW_QUESTIONS=diagram-questions.json BACKEND_EVAL_RUBRICS=diagram-rubrics.json
//
// They are held to the same gate as the flow questions, and every one has a
// rubric, or the answer arm stops on it after paying for the turns before.
func TestDiagramQuestionsAreWellFormed(t *testing.T) {
	t.Setenv("BACKEND_EVAL_FLOW_QUESTIONS", "diagram-questions.json")
	t.Setenv("BACKEND_EVAL_RUBRICS", "diagram-rubrics.json")
	TestFlowQuestionsAreWellFormed(t)
	rubrics := loadRubrics(t)
	for _, q := range loadFlowQuestions(t) {
		r, ok := rubrics[q.Text]
		if !ok {
			t.Errorf("%q has no rubric", q.Text)
			continue
		}
		if r.Lang != "en" && r.Lang != "de" {
			t.Errorf("%q: rubric language %q", q.Text, r.Lang)
		}
		if len(r.Must) == 0 {
			t.Errorf("%q: rubric makes no claim", q.Text)
		}
	}
}

// TestEvalTallyDiagramShapes reads an answers file TestEvalMeasureAnswers
// wrote and tallies its diagrams by shape, the answer text read again with
// ask.DiagramShape. It is how a baseline written before the shape existed
// (master's harness recorded the fence's first word, a swimlane as
// "flowchart") is counted on the same terms as the arm. No model, no
// database:
//
//	BACKEND_EVAL_ANSWERS_FILE=/tmp/rongo-answers-….json go test ./internal/retrieve/eval/ -run 'TestEvalTallyDiagramShapes$' -v
func TestEvalTallyDiagramShapes(t *testing.T) {
	path := os.Getenv("BACKEND_EVAL_ANSWERS_FILE")
	if path == "" {
		t.Skip("set BACKEND_EVAL_ANSWERS_FILE to an answers file TestEvalMeasureAnswers wrote")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var records []answerRecord
	if err := json.Unmarshal(body, &records); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	runs := 0
	for i := range records {
		records[i].Diagram = ask.DiagramShape(records[i].Answer)
		if records[i].Run > runs {
			runs = records[i].Run
		}
	}
	for run := 1; run <= runs; run++ {
		t.Logf("run %d: %s", run, shapeTally(records, run))
		for _, r := range records {
			if r.Run == run && r.Answer != "" {
				shape := r.Diagram
				if shape == "" {
					shape = "none"
				}
				t.Logf("  %-14s %s", shape, short(r.Question))
			}
		}
	}
}
