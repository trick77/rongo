package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

// The fictional order-intake model the symbols package tests against: every
// assertion here is about what the CHUNKER does with the symbols that reader
// emits, so both read the same file.
func orderIntakeModel(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "symbols", "testdata", "order-intake.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestChunkFile_bpmnAnchorsOnFlowNodesAndDropsTheDiagram(t *testing.T) {
	body := orderIntakeModel(t)
	syms, err := symbols.ExtractBPMN(body)
	if err != nil {
		t.Fatal(err)
	}
	chunks := ChunkFile("shop", "main", "src/main/resources/order-intake.bpmn", body, syms, DefaultChunkOptions())

	var bySymbol []string
	for _, c := range chunks {
		bySymbol = append(bySymbol, c.Symbol)
		if strings.Contains(c.RawText, "<bpmndi:") || strings.Contains(c.RawText, "dc:Bounds") {
			t.Errorf("chunk %q (lines %d-%d) carries diagram coordinates", c.Symbol, c.StartLine, c.EndLine)
		}
		if strings.Contains(c.Text, "diagram") {
			t.Errorf("chunk %q embeds the diagram symbol", c.Symbol)
		}
	}
	// The preamble (definitions, error, process opening, the unnamed start
	// event) is the first chunk with no symbol; then one chunk per node, the
	// wiring chunk last. Nothing after the process element survives. The
	// sub-process is not an anchor, the same way a class is not: its child
	// task is, and carries the sub-process in its breadcrumb.
	want := []string{
		"", "Validate order", "Authorise card", "Payment declined",
		"Gateway_express", "Ship express", "Send confirm-ation", "Order cancelled", "sequence flows",
	}
	if strings.Join(bySymbol, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks by symbol:\n got %q\nwant %q", bySymbol, want)
	}

	// The breadcrumb reads like a Java one, which is what lets a question in
	// business language reach a task by its label.
	validate := chunks[1]
	if !strings.Contains(validate.Text, "process order-intake > serviceTask Validate order") {
		t.Errorf("breadcrumb missing from:\n%s", validate.Text)
	}
	authorise := chunks[2]
	if !strings.Contains(authorise.Text, "subProcess Charge payment > serviceTask Authorise card") {
		t.Errorf("nested breadcrumb missing from:\n%s", authorise.Text)
	}
	// A run of two flows inside the sub-process stays with its node.
	if !strings.Contains(authorise.RawText, `id="Flow_c2"`) {
		t.Errorf("the flow after the task left its region:\n%s", authorise.RawText)
	}
	// The gateway's region is where its conditions would be looked for; here
	// the conditions sit in the wiring chunk, which names the gateway.
	wiring := chunks[len(chunks)-1]
	if !strings.Contains(wiring.RawText, "${shipping == 'EXPRESS'}") || !strings.Contains(wiring.Text, "sequenceFlows sequence flows") {
		t.Errorf("wiring chunk:\n%s", wiring.Text)
	}
	// A label the modeller hyphenated for the canvas is still findable by its
	// id in the keyword lane.
	if !strings.Contains(chunks[6].SearchText, `id="confirm"`) {
		t.Errorf("hyphenated task lost its id:\n%s", chunks[6].SearchText)
	}
	// The last chunk ends where the process does, not at the end of the file.
	if wiring.EndLine >= 56 {
		t.Errorf("wiring chunk runs to line %d, into the diagram block", wiring.EndLine)
	}
}

func TestLanguageOf_bpmn(t *testing.T) {
	if got := LanguageOf("src/main/resources/workflow/order-intake.bpmn"); got != "bpmn" {
		t.Errorf("got %q, want bpmn", got)
	}
	if got := LanguageOf("Order.BPMN"); got != "bpmn" {
		t.Errorf("upper-case extension: got %q, want bpmn", got)
	}
}
