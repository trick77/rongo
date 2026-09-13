package bpmn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func orderIntake(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "symbols", "testdata", "order-intake.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseReadsTheGraph(t *testing.T) {
	m, err := Parse(orderIntake(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Processes) != 1 || m.Processes[0].ID != "order-intake" || !m.Processes[0].Executable {
		t.Fatalf("processes: %+v", m.Processes)
	}
	p := m.Processes[0]
	if len(p.Nodes) != 9 {
		t.Errorf("got %d nodes, want 9", len(p.Nodes))
	}
	if len(p.Flows) != 8 {
		t.Errorf("got %d flows, want 8", len(p.Flows))
	}
	if len(p.Subs) != 1 || p.Subs[0].Name != "Charge payment" || len(p.Subs[0].Nodes) != 3 || len(p.Subs[0].Flows) != 2 {
		t.Errorf("sub-process: %+v", p.Subs)
	}
	byID := map[string]*Node{}
	for _, n := range p.Nodes {
		byID[n.ID] = n
	}
	if n := byID["Event_declined"]; n == nil || n.AttachedTo != "charge" || n.Error != "paymentDeclined" {
		t.Errorf("boundary event: %+v", n)
	}
	if n := byID["ship-express"]; n == nil || n.Called != "express-shipping" {
		t.Errorf("call activity: %+v", n)
	}
	if n := byID["Gateway_express"]; n == nil || n.Default != "Flow_standard" {
		t.Errorf("gateway: %+v", n)
	}
	var cond string
	for _, f := range p.Flows {
		if f.ID == "Flow_express" {
			cond = f.Condition
		}
	}
	if cond != "${shipping == 'EXPRESS'}" {
		t.Errorf("condition: %q", cond)
	}
}

func TestParseTimersAndDefinitionsUnderEvents(t *testing.T) {
	const model = `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <signal id="Signal_1" name="abort" />
  <process id="p" isExecutable="true">
    <intermediateCatchEvent id="wait" name="Wait">
      <timerEventDefinition><timeDuration xsi:type="tFormalExpression">${retryWait}</timeDuration></timerEventDefinition>
    </intermediateCatchEvent>
    <endEvent id="end">
      <terminateEventDefinition />
    </endEvent>
    <intermediateThrowEvent id="sig">
      <signalEventDefinition signalRef="Signal_1" />
    </intermediateThrowEvent>
  </process>
  <process id="pool" isExecutable="false" />
</definitions>`
	m, err := Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	p := m.Processes[0]
	if p.Nodes[0].Timer != "${retryWait}" || !p.Nodes[1].Terminate || p.Nodes[2].Signal != "abort" {
		t.Errorf("nodes: %+v %+v %+v", p.Nodes[0], p.Nodes[1], p.Nodes[2])
	}
	if len(m.Processes) != 2 || m.Processes[1].Executable {
		t.Errorf("the non-executable pool should be kept but marked: %+v", m.Processes)
	}
}

func TestDescribeRendersOneLinePerNode(t *testing.T) {
	m, err := Parse(orderIntake(t))
	if err != nil {
		t.Fatal(err)
	}
	got := Describe("shop/order-intake.bpmn", m, func(id string) string {
		if id == "express-shipping" {
			return "shop/express-shipping.bpmn"
		}
		return ""
	})
	for _, want := range []string{
		`Process "order-intake" in shop/order-intake.bpmn:`,
		"- startEvent StartEvent_1 (startEvent) -> Validate order\n",
		"- Validate order (serviceTask, runs #{shop.path('validateOrder')}) -> Charge payment\n",
		"- Payment declined (boundaryEvent, attached to Charge payment, error paymentDeclined) -> Order cancelled\n",
		"- exclusiveGateway Gateway_express (exclusiveGateway) -> Ship express if ${shipping == 'EXPRESS'}; Send confirm-ation (default)\n",
		`- Ship express (callActivity, calls process "express-shipping" in shop/express-shipping.bpmn) -> Send confirm-ation`,
		"- endEvent Event_done (endEvent)\n",
		`Sub-process "Charge payment" inside "order-intake":`,
		"- Authorise card (serviceTask, runs #{shop.path('authoriseCard')}) -> endEvent Event_charge_end\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dc:Bounds") {
		t.Error("diagram leaked into the listing")
	}
	// Walk order, not file order: the start event first, the boundary event
	// right after the node it interrupts, the end events last.
	idx := func(s string) int { return strings.Index(got, s) }
	if !(idx("- startEvent StartEvent_1") < idx("- Validate order") &&
		idx("- Validate order") < idx("- Charge payment (subProcess)") &&
		idx("- Charge payment (subProcess)") < idx("- Payment declined") &&
		idx("- Payment declined") < idx("- exclusiveGateway Gateway_express") &&
		idx("- Send confirm-ation") < idx("- endEvent Event_done")) {
		t.Errorf("not in walk order:\n%s", got)
	}
}

func TestDescribeNamesAnUnresolvedCall(t *testing.T) {
	m, _ := Parse(orderIntake(t))
	got := Describe("shop/order-intake.bpmn", m, func(string) string { return "" })
	if !strings.Contains(got, `calls process "express-shipping", whose model is not listed here`) {
		t.Errorf("unresolved call not named:\n%s", got)
	}
}

// bpmn-js and Camunda Modeler append the error, signal and message
// definitions to the root AFTER the process, and a process without
// isExecutable is the attribute's default, not an empty pool.
func TestParseResolvesDefinitionsWrittenAfterTheProcessAndKeepsNonExecutableGraphs(t *testing.T) {
	const model = `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <process id="p">
    <startEvent id="s" />
    <serviceTask id="t" name="Charge" />
    <boundaryEvent id="b" name="Declined" attachedToRef="t">
      <errorEventDefinition errorRef="Error_1" />
    </boundaryEvent>
    <intermediateThrowEvent id="m">
      <messageEventDefinition messageRef="Message_1" />
    </intermediateThrowEvent>
    <sequenceFlow id="f" sourceRef="s" targetRef="t" />
  </process>
  <error id="Error_1" name="paymentDeclined" errorCode="DECLINED" />
  <message id="Message_1" name="orderPlaced" />
</definitions>`
	m, err := Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	p := m.Processes[0]
	if p.Nodes[2].Error != "paymentDeclined" || p.Nodes[3].Message != "orderPlaced" {
		t.Errorf("definitions after the process did not resolve: %+v %+v", p.Nodes[2], p.Nodes[3])
	}
	got := Describe("shop/p.bpmn", m, func(string) string { return "" })
	if !strings.Contains(got, "- Charge (serviceTask)") || !strings.Contains(got, "error paymentDeclined") {
		t.Errorf("a graph without isExecutable must still be listed:\n%s", got)
	}
}

func TestParseRejectsBrokenXML(t *testing.T) {
	if _, err := Parse([]byte("<definitions><process id='p'")); err == nil {
		t.Error("no error for truncated XML")
	}
}
