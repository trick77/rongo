package symbols

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// orderProcess is a fictional model in the shape modellers produce: a start
// event, tasks, an embedded sub-process with its two flows written inline, a
// call activity into another model, a gateway whose flows are collected at the
// end of the process with the rest, a named and an unnamed event, and the
// diagram-interchange block that is half of every real file. The indexer's
// chunk test reads the same file.
var orderProcess = func() string {
	body, err := os.ReadFile(filepath.Join("testdata", "order-intake.bpmn"))
	if err != nil {
		panic(err)
	}
	return string(body)
}()

func TestExtractBPMNAnchorsOnFlowNodes(t *testing.T) {
	syms, err := ExtractBPMN([]byte(orderProcess))
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		name, kind, scope, scopeKind string
		line, end                    int
	}
	wants := []want{
		{"Validate order", "serviceTask", "order-intake", "process", 8, 11},
		{"Charge payment", "subProcess", "order-intake", "process", 12, 20},
		{"Authorise card", "serviceTask", "Charge payment", "subProcess", 17, 17},
		{"Payment declined", "boundaryEvent", "order-intake", "process", 21, 24},
		{"Gateway_express", "exclusiveGateway", "order-intake", "process", 25, 29},
		{"Ship express", "callActivity", "order-intake", "process", 30, 33},
		{"Send confirm-ation", "sendTask", "order-intake", "process", 34, 38},
		{"Order cancelled", "endEvent", "order-intake", "process", 42, 44},
		{"sequence flows", "sequenceFlows", "order-intake", "process", 45, 54},
		{"diagram", DiagramKind, "", "", 56, 65},
	}
	if len(syms) != len(wants) {
		var got []string
		for _, s := range syms {
			got = append(got, s.Kind+" "+s.Name)
		}
		t.Fatalf("got %d symbols, want %d:\n%s", len(syms), len(wants), strings.Join(got, "\n"))
	}
	for i, w := range wants {
		s := syms[i]
		if s.Name != w.name || s.Kind != w.kind || s.Scope != w.scope || s.ScopeKind != w.scopeKind || s.Line != w.line || s.End != w.end {
			t.Errorf("symbol %d: got %+v, want %+v", i, s, w)
		}
	}
}

// The two flows inside the sub-process are written right after their nodes,
// and a run that short stays in the node's region rather than becoming a
// symbol of its own. The eight at the end of the process are the wiring
// chunk. Unnamed start and end events are not symbols.
func TestExtractBPMNShortFlowRunsFold(t *testing.T) {
	syms, err := ExtractBPMN([]byte(orderProcess))
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	for _, s := range syms {
		if s.Kind == "sequenceFlows" {
			runs++
		}
		if s.Name == "StartEvent_1" || s.Name == "Event_done" || s.Name == "Event_charge_start" {
			t.Errorf("unnamed event %s became a symbol", s.Name)
		}
	}
	if runs != 1 {
		t.Errorf("got %d flow runs, want 1", runs)
	}
}

func TestExtractBPMNPrefixAgnostic(t *testing.T) {
	// The same model under Operaton's prefix and with no prefix at all.
	for _, body := range []string{
		strings.ReplaceAll(strings.ReplaceAll(orderProcess, "camunda:", "operaton:"), `xmlns:camunda="http://camunda.org/schema/1.0/bpmn"`, `xmlns:operaton="http://operaton.org/schema/1.0/bpmn"`),
		strings.ReplaceAll(strings.ReplaceAll(orderProcess, "<bpmn:", "<"), "</bpmn:", "</"),
	} {
		syms, err := ExtractBPMN([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(syms) != 10 {
			t.Errorf("got %d symbols, want 10", len(syms))
		}
	}
}

func TestExtractBPMNRejectsNonXML(t *testing.T) {
	if _, err := ExtractBPMN([]byte("<bpmn:definitions><bpmn:process")); err == nil {
		t.Error("truncated XML produced no error")
	}
	syms, err := ExtractBPMN([]byte("   \n"))
	if err != nil || syms != nil {
		t.Errorf("blank body: got %v, %v", syms, err)
	}
}

func TestBPMNStructuralKindsCoverEveryEmittedKind(t *testing.T) {
	kinds := map[string]bool{}
	for _, k := range BPMNStructuralKinds() {
		kinds[k] = true
	}
	syms, _ := ExtractBPMN([]byte(orderProcess))
	for _, s := range syms {
		if !kinds[s.Kind] {
			t.Errorf("kind %q is emitted but not structural", s.Kind)
		}
	}
}

// A listener under a flow's extension elements is a grandchild, not a
// sibling: it must not break the run. And a sub-process whose last child is a
// flow does not lend its run to the process-level flows after it.
func TestExtractBPMNFlowRunsRespectScopeAndChildren(t *testing.T) {
	const model = `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL" xmlns:camunda="http://camunda.org/schema/1.0/bpmn">
  <process id="p">
    <subProcess id="sub" name="Inner">
      <serviceTask id="a" name="A step" />
      <sequenceFlow id="f1" sourceRef="a" targetRef="b" />
      <sequenceFlow id="f2" sourceRef="b" targetRef="c" />
    </subProcess>
    <sequenceFlow id="f3" sourceRef="x" targetRef="y" />
    <sequenceFlow id="f4" sourceRef="y" targetRef="z">
      <extensionElements>
        <camunda:executionListener event="take" delegateExpression="#{audit}" />
      </extensionElements>
    </sequenceFlow>
    <sequenceFlow id="f5" sourceRef="z" targetRef="w" />
    <sequenceFlow id="f6" sourceRef="w" targetRef="v" />
  </process>
</definitions>
`
	syms, err := ExtractBPMN([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	var runs []Symbol
	for _, s := range syms {
		if s.Kind == "sequenceFlows" {
			runs = append(runs, s)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("got %d flow runs, want 1: %+v", len(runs), runs)
	}
	if runs[0].Line != 8 || runs[0].End != 15 || runs[0].Scope != "p" || runs[0].ScopeKind != "process" {
		t.Errorf("run = %+v, want lines 8-15 under process p", runs[0])
	}
}

func TestExtractBPMNCollapsesCanvasLineBreaks(t *testing.T) {
	const model = `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <process id="p">
    <serviceTask id="t" name="Send&#10;the&#10;receipt" />
  </process>
</definitions>
`
	syms, err := ExtractBPMN([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 1 || syms[0].Name != "Send the receipt" {
		t.Errorf("got %+v", syms)
	}
}
