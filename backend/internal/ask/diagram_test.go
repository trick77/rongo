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
	"    e1[\"Policenantrag eingegangen\"]\n" +
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
// @trick77/diagram's diagramKind).
func TestDiagramKind_readsTheTypeOffTheFence(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"flowchart": {"Lead.\n```mermaid\n" + kafkaMap + "```\nMore.", "flowchart"},
		"sequence":  {"```mermaid\nsequenceDiagram\n  A->>B: x\n```", "sequenceDiagram"},
		"state":     {"```mermaid\nstateDiagram-v2\n  [*] --> A\n```", "stateDiagram-v2"},
		"er":        {"```mermaid\nerDiagram\n  A ||--o{ B : has\n```", "erDiagram"},
		// A directive or a comment before the type is skipped.
		"directive": {"```mermaid\n%%{init: {}}%%\n\nflowchart TD\n  a\n```", "flowchart"},
		// So is YAML front matter, which the browser reads past too; an
		// unclosed one is no front matter, and its `---` is no type.
		"front matter":    {"```mermaid\n---\ntitle: Order\n---\nflowchart LR\n  a\n```", "flowchart"},
		"unclosed matter": {"```mermaid\n---\ntitle: Order\nflowchart LR\n  a\n```", ""},
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

// DiagramShape is what the harness tallies where the kind alone does not
// tell two pictures apart: a swimlane and a trigger-to-target mapping are
// both a flowchart LR with subgraphs.
func TestDiagramShape_tellsASwimlaneFromAMapping(t *testing.T) {
	fence := func(body string) string { return "Lead [1].\n\n```mermaid\n" + body + "```\nMore." }
	lanes := "flowchart LR\n" +
		"  subgraph a[\"Sales\"]\n    s1[\"Take order\"] --> s2{\"Credit ok?\"}\n  end\n" +
		"  subgraph b[\"Warehouse\"]\n    w1[\"Pack\"]\n  end\n" +
		"  s2 -->|\"yes\"| w1\n  w1 --> s1\n"
	for name, tc := range map[string]struct{ text, want string }{
		"lanes":   {fence(lanes), "swimlane"},
		"mapping": {fence(kafkaMap), "flowchart"},
		// Top-down is a flow with zones, never lanes.
		"td zones": {fence(strings.Replace(lanes, "flowchart LR", "flowchart TD", 1)), "flowchart"},
		// A step outside every role is not a lane picture.
		"loose step": {fence(lanes + "  x[\"Loose\"] --> s1\n"), "flowchart"},
		// One role is no lanes.
		"one role":  {fence("flowchart LR\n  subgraph a\n    p --> q\n  end\n"), "flowchart"},
		"no fence":  {"Prose.", ""},
		"sequence":  {fence("sequenceDiagram\n  A->>B: x\n"), "sequenceDiagram"},
		"class":     {fence("classDiagram\n  A <|-- B\n"), "classDiagram"},
		"self loop": {fence(lanes + "  w1 --> w1\n"), "flowchart"},
	} {
		if got := DiagramShape(tc.text); got != tc.want {
			t.Errorf("%s: DiagramShape = %q, want %q", name, got, tc.want)
		}
	}
}

// Every link and title form mermaid reads, read the same way: a word in a
// link is its label, never a step; a dotted link with a label still links;
// a `~~~` is a layout hint, its steps steps but no arrow between them; a
// title without brackets is the whole rest of the line.
func TestDiagramShape_readsEveryLinkAndTitleForm(t *testing.T) {
	fence := func(body string) string { return "```mermaid\n" + body + "```\n" }
	// Two roles, one arrow each way: lanes, not a mapping.
	lanes := func(head, a, b, back string) string {
		return head + "\n" +
			"  " + a + "\n    s1[\"Take order\"] --> s2{\"Credit ok?\"}\n  end\n" +
			"  " + b + "\n    w1[\"Pack\"]\n  end\n" +
			"  s2 --> w1\n  " + back + "\n"
	}
	// One step a role, one arrow across: whether it is lanes or the mapping
	// turns on the arrow back alone.
	pair := func(back string) string {
		return "flowchart LR\n" +
			"  subgraph a[\"Sales\"]\n    s1[\"Take order\"]\n  end\n" +
			"  subgraph b[\"Warehouse\"]\n    w1[\"Pack\"]\n  end\n" +
			"  s1 --> w1\n  " + back + "\n"
	}
	sales, store := `subgraph a["Sales"]`, `subgraph b["Warehouse"]`
	for name, tc := range map[string]struct{ text, want string }{
		"labelled solid":  {fence(pair("w1 -- done --> s1")), "swimlane"},
		"labelled thick":  {fence(pair("w1 == done ==> s1")), "swimlane"},
		"labelled dotted": {fence(pair("w1 -. done .-> s1")), "swimlane"},
		"quoted dotted":   {fence(pair(`w1 -. "done" .-> s1`)), "swimlane"},
		"plain dotted":    {fence(pair("w1 -.-> s1")), "swimlane"},
		// The only way back is a layout hint: every arrow runs from the first
		// role into the second, which is the mapping.
		"invisible back": {fence(pair("w1 ~~~ s1")), "flowchart"},
		// A step reached only by a layout hint is still a step, outside every role.
		"invisible loose": {fence(lanes("flowchart LR", sales, store, "w1 --> s1\n  w1 ~~~ loose")), "flowchart"},
		"bare titles":     {fence(lanes("flowchart LR", "subgraph Order service", "subgraph Order desk", "w1 --> s1")), "swimlane"},
		"quoted titles":   {fence(lanes("flowchart LR", `subgraph "Sales team"`, `subgraph "Sales desk"`, "w1 --> s1")), "swimlane"},
		"direction semi":  {fence(lanes("flowchart LR;", sales, store, "w1 --> s1")), "swimlane"},
		"accessibility": {fence(lanes("flowchart LR\n  accTitle: Checkout\n  accDescr: Who does what", sales, store, "w1 --> s1")),
			"swimlane"},
	} {
		if got := DiagramShape(tc.text); got != tc.want {
			t.Errorf("%s: DiagramShape = %q, want %q", name, got, tc.want)
		}
	}
}

// The tally reads the corpus as the browser draws it: ui/src/corpus.test.ts
// asserts the same files' shapes off the renderer's parse.
func TestDiagramShape_agreesWithTheBrowserOnTheCorpus(t *testing.T) {
	want := map[string]string{
		"eval-swimlane-checkout-en.txt": "swimlane",
		"mapping-lr.txt":                "flowchart",
		"eval-class-payment-en.txt":     "classDiagram",
	}
	for name, text := range corpus(t) {
		got := DiagramShape(text)
		if w, ok := want[name]; ok {
			if got != w {
				t.Errorf("%s: DiagramShape = %q, want %q", name, got, w)
			}
			continue
		}
		if got == "swimlane" {
			t.Errorf("%s: DiagramShape = swimlane, but the browser draws no lanes there", name)
		}
	}
}
