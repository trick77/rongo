package ask

import (
	"strings"
	"testing"
)

// A diagram fence is a code fence to the renumberer: nothing in it is a
// marker, and nothing in it is rewritten. The picture cites through the
// sentence that introduces it, and that sentence renumbers as prose does.

// renumbered runs the whole answer through the renumberer in one go.
func renumbered(t *testing.T, sources int, text string) (string, *renumberer) {
	t.Helper()
	rn := newRenumberer(sources)
	out := rn.feed(text) + rn.flush()
	return out, rn
}

const kafkaMap = "flowchart LR\n" +
	"  subgraph Ereignis\n" +
	"    e1[\"Schadenmeldung eingegangen\"]\n" +
	"    e2[\"Nachreichung eingegangen\"]\n" +
	"  end\n" +
	"  subgraph Topic\n" +
	"    t1[\"Benachrichtigung\"]\n" +
	"    t2[\"Arbeitsunfähigkeit\"]\n" +
	"  end\n" +
	"  e1 --> t1\n" +
	"  e2 --> t1\n" +
	"  e2 -->|\"nur mit Zeiträumen\"| t2\n"

func TestRenumber_aDiagramFenceIsLeftExactlyAsItCame(t *testing.T) {
	text := "Die Zuordnung steht im Code [7][3].\n\n```mermaid\n" + kafkaMap + "```\n\nDanach [7]."

	out, rn := renumbered(t, 9, text)

	if !strings.Contains(out, "```mermaid\n"+kafkaMap+"```") {
		t.Errorf("out = %q, want the fence byte for byte as it came", out)
	}
	if !strings.HasPrefix(out, "Die Zuordnung steht im Code [1][2].") || !strings.HasSuffix(out, "Danach [1].") {
		t.Errorf("out = %q, want the prose around it renumbered", out)
	}
	if len(rn.order) != 2 {
		t.Errorf("order = %v, want the two prose sources and nothing from the fence", rn.order)
	}
}

func TestRenumber_aNumberInADiagramLabelIsNotACitation(t *testing.T) {
	// A label may hold anything: an index expression, a step number, a
	// marker the model wrote against the prompt. None of it is a claim.
	text := "```mermaid\nflowchart TD\n  a[\"parts[2]\"] --> b[\"Schritt [1]\"]\n```"

	out, rn := renumbered(t, 2, text)

	if out != text {
		t.Errorf("out = %q, want %q", out, text)
	}
	if len(rn.order) != 0 {
		t.Errorf("order = %v, want nothing cited", rn.order)
	}
}

func TestRenumber_aDiagramSplitAcrossTokensIsStillOneFence(t *testing.T) {
	text := "Sequenz [4].\n```mermaid\nsequenceDiagram\n  A->>B: call [4]\n  B-->>A: ok\n```\nEnde [2]."
	want := "Sequenz [1].\n```mermaid\nsequenceDiagram\n  A->>B: call [4]\n  B-->>A: ok\n```\nEnde [2]."

	for _, size := range []int{1, 2, 3, 5, 7, 11} {
		rn := newRenumberer(5)
		var out strings.Builder
		for i := 0; i < len(text); i += size {
			end := i + size
			if end > len(text) {
				end = len(text)
			}
			out.WriteString(rn.feed(text[i:end]))
		}
		out.WriteString(rn.flush())
		if out.String() != want {
			t.Errorf("token size %d: out = %q, want %q", size, out.String(), want)
		}
	}
}

func TestRenumber_aSameLineTripleBacktickSpanIsNotAFence(t *testing.T) {
	// Reading the rest of the line as an info string would leave the fence
	// open over the whole answer, and every marker after it would reach the
	// reader as the prompt's number with no citation behind it.
	out, rn := renumbered(t, 9, "see ```foo``` and [3]. More prose [5].\n")

	if !strings.Contains(out, "and [1].") || !strings.Contains(out, "prose [2].") {
		t.Errorf("out = %q, want the markers after the span renumbered", out)
	}
	if len(rn.order) != 2 {
		t.Errorf("order = %v, want both markers cited", rn.order)
	}
}

func TestRenumber_anUnclosedDiagramFenceStillEndsWhole(t *testing.T) {
	// A cut stream: the browser shows the block as text, so nothing may be
	// held back for a close that never comes.
	rn := newRenumberer(2)
	out := rn.feed("```mermaid\nflowchart TD\n  a --> b[\"x") + rn.flush()

	if !strings.HasSuffix(out, "b[\"x") {
		t.Errorf("out = %q, want the partial fence flushed as it came", out)
	}
}

// DiagramKind is what the answers harness counts: the diagram type the fence
// names, under the browser's reading of a fence (markdown.tsx fenceRe,
// diagram.tsx diagramKind).
func TestDiagramKind_readsTheTypeOffTheFence(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"flowchart": {"Lead.\n```mermaid\n" + kafkaMap + "```\nMore.", "flowchart"},
		"sequence":  {"```mermaid\nsequenceDiagram\n  A->>B: x\n```", "sequenceDiagram"},
		"state":     {"```mermaid\nstateDiagram-v2\n  [*] --> A\n```", "stateDiagram-v2"},
		"er":        {"```mermaid\nerDiagram\n  A ||--o{ B : has\n```", "erDiagram"},
		// A directive or a comment before the type is skipped.
		"directive": {"```mermaid\n%%{init: {}}%%\n\nflowchart TD\n  a\n```", "flowchart"},
		// The browser takes the first token of the info string, and the
		// older tag still opens a diagram.
		"header variant": {"```mermaid graph\nflowchart TD\n  a\n```", "flowchart"},
		"diagram tag":    {"```diagram\nflowchart TD\n  a\n```", "flowchart"},
		// A code fence before the diagram is skipped, not mistaken for it.
		"after code": {"```go\nx := 1\n```\n```mermaid\nerDiagram\n  A\n```", "erDiagram"},
		"prose":      {"Just prose [1].", ""},
		"code":       {"```go\nx := 1\n```", ""},
		"empty":      {"```mermaid\n\n```", ""},
		// The older JSON spec under either tag: the browser converts it, but
		// the harness counts the shape the model wrote, and it wrote none.
		"legacy json":     {"```diagram\n{\"type\":\"flow\",\"nodes\":[]}\n```", ""},
		"json as mermaid": {"```mermaid\n{\"type\":\"flow\"}\n```", ""},
		"unclosed":        {"```mermaid\nflowchart TD\n  a", ""},
	} {
		if got := DiagramKind(tc.text); got != tc.want {
			t.Errorf("%s: DiagramKind = %q, want %q", name, got, tc.want)
		}
	}
}
