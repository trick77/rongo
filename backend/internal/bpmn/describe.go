package bpmn

import (
	"fmt"
	"strings"
)

// Describe renders one model's executable processes as text for the answer
// prompt: one line per node in walk order, its outgoing flows on the same
// line with their conditions, embedded sub-processes as blocks of their own
// under the parent. Deterministic, no model, nothing inferred: every word is
// an attribute of the file.
//
// where names the file for the reader of the prompt ("repo/path"); resolved
// says which file a call activity's target lives in, or "" when the corpus
// does not carry it, so the listing can say so rather than let the answer
// guess.
func Describe(where string, m *Model, resolved func(processID string) string) string {
	var b strings.Builder
	for _, p := range m.Processes {
		if len(p.Nodes) == 0 {
			// A collaboration's other participants: pools with no nodes.
			// isExecutable is NOT the gate: the attribute defaults to false,
			// and a documentation model or an older export with a full
			// graph and no attribute is still the wiring the reader asked
			// about.
			continue
		}
		describeProcess(&b, where, p, m, resolved)
	}
	return b.String()
}

func describeProcess(b *strings.Builder, where string, p *Process, m *Model, resolved func(string) string) {
	if p.Parent == nil {
		fmt.Fprintf(b, "Process %q in %s:\n", p.ID, where)
	} else {
		fmt.Fprintf(b, "Sub-process %q inside %q:\n", label(p.ID, p.Name, "subProcess"), p.Parent.ID)
	}
	byID := map[string]*Node{}
	for _, n := range p.Nodes {
		byID[n.ID] = n
	}
	out := map[string][]*Flow{}
	for _, f := range p.Flows {
		out[f.Source] = append(out[f.Source], f)
	}
	labelOf := func(id string) string {
		if n, ok := byID[id]; ok {
			return label(n.ID, n.Name, n.Kind)
		}
		return id
	}
	for _, n := range walkOrder(p) {
		fmt.Fprintf(b, "- %s (%s", label(n.ID, n.Name, n.Kind), n.Kind)
		if n.Delegate != "" {
			fmt.Fprintf(b, ", runs %s", n.Delegate)
		}
		if n.Called != "" {
			if file := resolved(n.Called); file != "" {
				fmt.Fprintf(b, ", calls process %q in %s", n.Called, file)
			} else {
				fmt.Fprintf(b, ", calls process %q, whose model is not listed here", n.Called)
			}
		}
		if n.AttachedTo != "" {
			fmt.Fprintf(b, ", attached to %s", labelOf(n.AttachedTo))
		}
		if n.Timer != "" {
			fmt.Fprintf(b, ", timer %s", n.Timer)
		}
		if n.Error != "" {
			fmt.Fprintf(b, ", error %s", n.Error)
		}
		if n.Signal != "" {
			fmt.Fprintf(b, ", signal %s", n.Signal)
		}
		if n.Escalation != "" {
			fmt.Fprintf(b, ", escalation %s", n.Escalation)
		}
		if n.Message != "" {
			fmt.Fprintf(b, ", message %s", n.Message)
		}
		if n.Terminate {
			b.WriteString(", terminates the process")
		}
		b.WriteString(")")
		for i, f := range out[n.ID] {
			if i == 0 {
				b.WriteString(" ->")
			} else {
				b.WriteString(";")
			}
			b.WriteString(" ")
			b.WriteString(labelOf(f.Target))
			switch {
			case f.Condition != "":
				fmt.Fprintf(b, " if %s", f.Condition)
			case f.ID != "" && f.ID == n.Default:
				b.WriteString(" (default)")
			}
			if f.Name != "" {
				fmt.Fprintf(b, " [%s]", f.Name)
			}
		}
		b.WriteString("\n")
	}
	for _, sub := range p.Subs {
		describeProcess(b, where, sub, m, resolved)
	}
}

// walkOrder is the order a reader would walk the process in: from each start
// event along the flows, breadth first, then the boundary events beside the
// node they interrupt and whatever the flows never reached, in file order.
// The file's own order is whatever the modeller clicked last, and a listing
// in that order reads as a jumble.
func walkOrder(p *Process) []*Node {
	byID := map[string]*Node{}
	for _, n := range p.Nodes {
		byID[n.ID] = n
	}
	out := map[string][]*Flow{}
	for _, f := range p.Flows {
		out[f.Source] = append(out[f.Source], f)
	}
	seen := map[string]bool{}
	var order []*Node
	visit := func(start *Node) {
		queue := []*Node{start}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			order = append(order, n)
			// The interrupting events right after the node they hang on,
			// so an error path is read where it leaves the happy path.
			for _, m := range p.Nodes {
				if m.AttachedTo == n.ID && !seen[m.ID] {
					queue = append(queue, m)
				}
			}
			for _, f := range out[n.ID] {
				if t, ok := byID[f.Target]; ok && !seen[t.ID] {
					queue = append(queue, t)
				}
			}
		}
	}
	for _, n := range p.Nodes {
		if n.Kind == "startEvent" {
			visit(n)
		}
	}
	for _, n := range p.Nodes {
		if !seen[n.ID] {
			visit(n)
		}
	}
	return order
}

// label is how a node is named in the listing: its label where the modeller
// gave one, else its kind and id, so an unnamed gateway is still a thing the
// answer can refer to.
func label(id, name, kind string) string {
	if name != "" {
		return name
	}
	return kind + " " + id
}
