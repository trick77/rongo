// Package bpmn reads a BPMN 2.0 process model as a graph: which node leads to
// which, under what condition, and which model a call activity runs.
//
// It exists for the answer prompt. The chunker already puts every node and
// every sequence flow of a model in front of the model (internal/symbols reads
// the same file for that), but a list of sourceRef/targetRef pairs is not
// something an answer orders into a walk, and a call activity's target sits in
// another file that retrieval did not return. Describe renders the graph as
// one line per node, in walk order, with the branches and their conditions
// on it — read from the file at the commit the source was cited at, per turn,
// never stored. Same footing as the structure block: derived from what is
// indexed, so it cannot go stale on its own.
//
// Prefix-agnostic on the local name, like the symbol reader: bpmn:, camunda:,
// operaton: and no prefix at all all carry the same elements.
package bpmn

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Model is one file's worth of processes.
type Model struct {
	// Processes are the top-level processes in file order, executable or not.
	Processes []*Process
	// Errors, Signals and Escalations map a definition id to its name, so a
	// boundary event can say "on error paymentDeclined" rather than "Error_1".
	Errors, Signals, Escalations map[string]string
}

// Process is a process or an embedded sub-process: a scope with nodes and the
// flows between them.
type Process struct {
	ID, Name string
	// Executable is the isExecutable attribute; a collaboration's other
	// participants are drawn as non-executable pools with no nodes.
	Executable bool
	// Parent is the enclosing process for an embedded sub-process, nil at the
	// top level.
	Parent *Process
	Nodes  []*Node
	Flows  []*Flow
	// Subs are the embedded sub-processes, each also present in Nodes as a
	// node of kind subProcess.
	Subs []*Process
}

// Node is one flow node.
type Node struct {
	ID, Kind, Name string
	// Called is a call activity's calledElement: the id of the process it runs.
	Called string
	// Default is the id of the flow a gateway or activity takes when no
	// condition holds.
	Default string
	// Delegate is what an activity runs, as the engine's extension attribute
	// spells it: a delegateExpression, class, expression or external-task
	// topic. It is the bridge from a task to the code behind it.
	Delegate string
	// AttachedTo is the node a boundary event interrupts.
	AttachedTo string
	// Timer is a timer definition's duration, cycle or date, as written.
	Timer string
	// Error, Signal, Escalation, Message name what an event catches or throws,
	// resolved through the model's definitions where they resolve.
	Error, Signal, Escalation, Message string
	// Terminate marks a terminating end event.
	Terminate bool
	Line      int
}

// Flow is one sequence flow.
type Flow struct {
	ID, Source, Target, Name, Condition string
}

// Parse reads one model. A file that is not well-formed XML is an error; a
// well-formed file with no process is an empty Model.
func Parse(body []byte) (*Model, error) {
	m := &Model{Errors: map[string]string{}, Signals: map[string]string{}, Escalations: map[string]string{}}
	messages := map[string]string{}
	lines := lineStarts(body)
	lineOf := func(off int64) int {
		i := bytes.LastIndexByte(body[:off], '<')
		if i < 0 {
			i = int(off) - 1
		}
		return sort.Search(len(lines), func(j int) bool { return lines[j] > i })
	}

	var stack []frame
	var scope *Process
	var text strings.Builder
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bpmn: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "BPMNDiagram" {
				// Nothing below is the graph.
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("bpmn: %w", err)
				}
				continue
			}
			f := frame{local: t.Name.Local}
			text.Reset()
			id, name := attr(t, "id"), attr(t, "name")
			switch {
			case t.Name.Local == "process":
				p := &Process{ID: id, Name: name, Executable: attr(t, "isExecutable") == "true"}
				m.Processes = append(m.Processes, p)
				f.proc, scope = p, p
			case t.Name.Local == "error":
				m.Errors[id] = firstOf(name, attr(t, "errorCode"), id)
			case t.Name.Local == "signal":
				m.Signals[id] = firstOf(name, id)
			case t.Name.Local == "escalation":
				m.Escalations[id] = firstOf(name, attr(t, "escalationCode"), id)
			case t.Name.Local == "message":
				messages[id] = firstOf(name, id)
			case t.Name.Local == "sequenceFlow" && scope != nil:
				fl := &Flow{ID: id, Source: attr(t, "sourceRef"), Target: attr(t, "targetRef"), Name: name}
				scope.Flows = append(scope.Flows, fl)
				f.flow = fl
			case nodeKinds[t.Name.Local] && scope != nil:
				n := &Node{ID: id, Kind: t.Name.Local, Name: name, Called: attr(t, "calledElement"),
					Default: attr(t, "default"), AttachedTo: attr(t, "attachedToRef"), Line: lineOf(dec.InputOffset()),
					Delegate: firstOf(attr(t, "delegateExpression"), attr(t, "class"), attr(t, "expression"), attr(t, "topic"))}
				scope.Nodes = append(scope.Nodes, n)
				f.node = n
				if containerKinds[t.Name.Local] {
					sub := &Process{ID: id, Name: name, Executable: scope.Executable, Parent: scope}
					scope.Subs = append(scope.Subs, sub)
					f.proc, scope = sub, sub
				}
			case t.Name.Local == "errorEventDefinition":
				if n := enclosingNode(stack); n != nil {
					n.Error = firstOf(attr(t, "errorRef"), "error")
				}
			case t.Name.Local == "signalEventDefinition":
				if n := enclosingNode(stack); n != nil {
					n.Signal = firstOf(attr(t, "signalRef"), "signal")
				}
			case t.Name.Local == "escalationEventDefinition":
				if n := enclosingNode(stack); n != nil {
					n.Escalation = firstOf(attr(t, "escalationRef"), "escalation")
				}
			case t.Name.Local == "messageEventDefinition":
				if n := enclosingNode(stack); n != nil {
					// Unlike the others, a message with no reference says
					// nothing worth a word: the engine's throw goes through
					// the delegate, which is listed.
					n.Message = attr(t, "messageRef")
					// A message throw event carries its delegate on the
					// definition, not on the event.
					if n.Delegate == "" {
						n.Delegate = firstOf(attr(t, "delegateExpression"), attr(t, "class"), attr(t, "expression"), attr(t, "topic"))
					}
				}
			case t.Name.Local == "terminateEventDefinition":
				if n := enclosingNode(stack); n != nil {
					n.Terminate = true
				}
			}
			stack = append(stack, f)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("bpmn: end element %s without a start", t.Name.Local)
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			body := strings.Join(strings.Fields(text.String()), " ")
			switch f.local {
			case "timeDuration", "timeCycle", "timeDate":
				if n := enclosingNode(stack); n != nil && body != "" {
					n.Timer = body
				}
			case "conditionExpression":
				if fl := enclosingFlow(stack); fl != nil {
					fl.Condition = body
				}
			}
			text.Reset()
			if f.proc != nil {
				scope = f.proc.Parent
			}
		}
	}
	// The references resolve AFTER the walk: bpmn-js and Camunda Modeler
	// append the error, signal and message definitions to the root after the
	// process, so at the time a boundary event is read its errorRef names a
	// definition the decoder has not met yet. Read eagerly, every real export
	// listed "error Error_0k3x1" instead of the name.
	resolve := func(p *Process) {}
	resolve = func(p *Process) {
		for _, n := range p.Nodes {
			n.Error = firstOf(m.Errors[n.Error], n.Error)
			n.Signal = firstOf(m.Signals[n.Signal], n.Signal)
			n.Escalation = firstOf(m.Escalations[n.Escalation], n.Escalation)
			n.Message = firstOf(messages[n.Message], n.Message)
		}
		for _, sub := range p.Subs {
			resolve(sub)
		}
	}
	for _, p := range m.Processes {
		resolve(p)
	}
	return m, nil
}

// nodeKinds are the flow-node elements the graph is made of.
var nodeKinds = map[string]bool{
	"task": true, "serviceTask": true, "userTask": true, "sendTask": true,
	"receiveTask": true, "scriptTask": true, "businessRuleTask": true,
	"manualTask": true, "callActivity": true, "subProcess": true,
	"adHocSubProcess": true, "transaction": true,
	"exclusiveGateway": true, "parallelGateway": true, "inclusiveGateway": true,
	"eventBasedGateway": true, "complexGateway": true,
	"startEvent": true, "endEvent": true, "intermediateCatchEvent": true,
	"intermediateThrowEvent": true, "boundaryEvent": true,
}

var containerKinds = map[string]bool{"subProcess": true, "adHocSubProcess": true, "transaction": true}

func enclosingNode(stack []frame) *Node {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].node != nil {
			return stack[i].node
		}
		if stack[i].flow != nil || stack[i].proc != nil {
			return nil
		}
	}
	return nil
}

func enclosingFlow(stack []frame) *Flow {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].flow != nil {
			return stack[i].flow
		}
		if stack[i].node != nil || stack[i].proc != nil {
			return nil
		}
	}
	return nil
}

// frame is the parser's per-element state; declared at package level so the
// helpers above can take a slice of it.
type frame struct {
	local string
	proc  *Process
	node  *Node
	flow  *Flow
}

func attr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name {
			return strings.Join(strings.Fields(a.Value), " ")
		}
	}
	return ""
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func lineStarts(body []byte) []int {
	starts := []int{0}
	for i, b := range body {
		if b == '\n' && i+1 < len(body) {
			starts = append(starts, i+1)
		}
	}
	return starts
}
