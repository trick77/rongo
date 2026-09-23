package symbols

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ExtractBPMN reads a BPMN 2.0 process model and returns one Symbol per flow
// node, so the chunker anchors on "serviceTask Validate order" the way it
// anchors on "method run" in Java, and one Symbol for the diagram-interchange
// block so the chunker can leave the coordinates out.
//
// It is a plain XML pass, deterministic and re-run whenever the file is: no
// model, no inference, nothing stored that the file does not say. Namespace
// prefixes are ignored on purpose — Camunda 7, Operaton and Zeebe models all
// carry the OMG elements under different prefixes, and the local name is what
// identifies a task.
//
// What anchors and what does not is a chunking decision, not a modelling one:
//
//   - Activities (tasks, call activities, sub-processes) and gateways always
//     anchor: a gateway's region carries the outgoing flows with their
//     conditions, which is where "what decides X" is answered.
//   - Events anchor only when the modeller named them. An unnamed end event
//     is three lines of id and nothing a question would name; folded into the
//     neighbouring region it still gets indexed, on its own it would be a
//     chunk of noise.
//   - A run of three or more consecutive sequence flows anchors as one
//     "sequenceFlows" symbol. Modellers that write a node's flows right after
//     it keep them in the node's region (a run of one or two); modellers that
//     collect every flow of the process at its end get a wiring chunk under
//     the process rather than under whichever node happened to come last.
//   - The DI block is one symbol of kind "diagram". Its region is dropped by
//     the chunker; the symbol itself stays so the viewer can say where it is.
//
// Text and processing errors are returned as an error: a file that does not
// parse as XML is not a BPMN model, and the caller falls back to line windows
// the way it does for a ctags failure.
func ExtractBPMN(body []byte) ([]Symbol, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	lm := newLineMap(body)
	lineOf, tagStart := lm.lineOf, lm.tagStart

	type open struct {
		sym   int // index into out, -1 for elements that are not symbols
		local string
		// scope is what the children of this element report as their
		// enclosing symbol: a process or a sub-process.
		scopeName, scopeKind string
	}
	var (
		stack  []open
		out    []Symbol
		flows  = -1 // index of the open run of sequence flows, if any
		flowN  int
		lastEl string // local name of the previous sibling start element
	)
	closeFlows := func() {
		if flows >= 0 && flowN < minFlowRun {
			// Too short a run to stand on its own: forget it, the lines stay
			// in the preceding node's region. The run is the last symbol
			// while it is open, because every other symbol closes it first.
			out = out[:flows]
		}
		flows, flowN = -1, 0
	}

	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bpmn: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local
			line := tagStart(dec.InputOffset())
			o := open{local: local, sym: -1}
			if len(stack) > 0 {
				o.scopeName, o.scopeKind = stack[len(stack)-1].scopeName, stack[len(stack)-1].scopeKind
			}
			switch {
			case local == "sequenceFlow":
				if flows < 0 || lastEl != "sequenceFlow" {
					closeFlows()
					out = append(out, Symbol{Name: "sequence flows", Kind: "sequenceFlows",
						Scope: o.scopeName, ScopeKind: o.scopeKind, Line: line})
					flows = len(out) - 1
				}
				flowN++
				o.sym = flows
			case local == "BPMNDiagram":
				closeFlows()
				out = append(out, Symbol{Name: "diagram", Kind: DiagramKind, Line: line})
				o.sym = len(out) - 1
			case bpmnNodeKinds[local]:
				closeFlows()
				name := attr(t, "name")
				id := attr(t, "id")
				if name == "" {
					if bpmnEventKinds[local] {
						// Unnamed event: not a symbol, see above.
						break
					}
					name = id
				}
				if name == "" {
					break
				}
				out = append(out, Symbol{Name: name, Kind: local,
					Scope: o.scopeName, ScopeKind: o.scopeKind, Line: line})
				o.sym = len(out) - 1
				if bpmnContainerKinds[local] {
					o.scopeName, o.scopeKind = name, local
				}
			case local == "process":
				closeFlows()
				if id := attr(t, "id"); id != "" {
					o.scopeName, o.scopeKind = id, "process"
				}
			}
			// Only a SIBLING breaks a run of flows: a child of a flow (its
			// condition, a listener under its extension elements) or of a
			// node is not one. Siblings are the children of a process or of
			// a container, which the stack tells apart.
			if len(stack) > 0 && (stack[len(stack)-1].local == "process" || bpmnContainerKinds[stack[len(stack)-1].local]) {
				lastEl = local
			}
			stack = append(stack, o)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("bpmn: end element without a start")
			}
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if o.local == "process" || bpmnContainerKinds[o.local] {
				// A run of flows ends with its scope: the flows after a
				// sub-process are the process's, not a continuation.
				closeFlows()
				lastEl = o.local
			}
			if o.sym >= 0 && o.sym < len(out) {
				// The bound matters: a flow run shorter than minFlowRun was
				// cut from out while its elements were still open.
				end := lineOf(dec.InputOffset() - 1)
				if end > out[o.sym].End {
					out[o.sym].End = end
				}
			}
		}
	}
	closeFlows()
	return out, nil
}

// minFlowRun is how many consecutive sequence flows it takes before they are
// their own symbol rather than the tail of the node before them.
const minFlowRun = 3

// bpmnNodeKinds are the flow-node elements that become symbols. Their local
// names double as the symbol kind, so the breadcrumb reads "serviceTask X".
var bpmnNodeKinds = map[string]bool{
	"task": true, "serviceTask": true, "userTask": true, "sendTask": true,
	"receiveTask": true, "scriptTask": true, "businessRuleTask": true,
	"manualTask": true, "callActivity": true, "subProcess": true,
	"adHocSubProcess": true, "transaction": true,
	"exclusiveGateway": true, "parallelGateway": true, "inclusiveGateway": true,
	"eventBasedGateway": true, "complexGateway": true,
	"startEvent": true, "endEvent": true, "intermediateCatchEvent": true,
	"intermediateThrowEvent": true, "boundaryEvent": true,
}

// bpmnEventKinds are the nodes that only become symbols when named.
var bpmnEventKinds = map[string]bool{
	"startEvent": true, "endEvent": true, "intermediateCatchEvent": true,
	"intermediateThrowEvent": true, "boundaryEvent": true,
}

// bpmnContainerKinds are the nodes whose children report them as scope.
var bpmnContainerKinds = map[string]bool{
	"subProcess": true, "adHocSubProcess": true, "transaction": true,
}

// BPMNStructuralKinds are the symbol kinds ExtractBPMN emits that a chunk may
// anchor on: every node kind, the flow run and the diagram block. The chunker
// merges them into its ctags set.
func BPMNStructuralKinds() []string {
	kinds := make([]string, 0, len(bpmnNodeKinds)+2)
	for k := range bpmnNodeKinds {
		kinds = append(kinds, k)
	}
	kinds = append(kinds, "sequenceFlows", "diagram")
	sort.Strings(kinds)
	return kinds
}

// DiagramKind is the kind of the one symbol that marks the diagram-interchange
// block; the chunker drops that region.
const DiagramKind = "diagram"

// attr returns one attribute with its whitespace collapsed: a modeller breaks
// a long label across the canvas with &#10;, which the decoder turns into a
// newline, and a newline has no place in a symbol name or a breadcrumb line.
func attr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name {
			return strings.Join(strings.Fields(a.Value), " ")
		}
	}
	return ""
}

// lineMap maps a decoder's byte offsets back to 1-based lines.
type lineMap struct {
	body   []byte
	starts []int // byte offset each line begins at
}

func newLineMap(body []byte) lineMap {
	starts := []int{0}
	for i, b := range body {
		if b == '\n' && i+1 < len(body) {
			starts = append(starts, i+1)
		}
	}
	return lineMap{body: body, starts: starts}
}

// lineOf is the line holding the byte at offset.
func (m lineMap) lineOf(offset int64) int {
	return sort.Search(len(m.starts), func(i int) bool { return m.starts[i] > int(offset) })
}

// tagStart is the line a start tag opens on. InputOffset after a start tag is
// the byte past its ">"; the tag's own line is where its "<" sits. A "<"
// cannot occur inside a tag, so the last one before end is it.
func (m lineMap) tagStart(end int64) int {
	i := bytes.LastIndexByte(m.body[:end], '<')
	if i < 0 {
		return m.lineOf(end - 1)
	}
	return m.lineOf(int64(i))
}
