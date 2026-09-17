package ask

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the .golden files: go test ./internal/ask/ -update
var update = flag.Bool("update", false, "rewrite the corpus golden files")

// The corpus in testdata/diagrams holds answers as models actually wrote
// them, one file per shape that has been seen. Three regressions came from
// testing the shape the prompt asks for and nothing else; every one of them
// would have been a file here.
//
// Nothing is asserted about the wording of a fixture, only the invariants
// that make a diagram a diagram: it leaves the renumberer inside one
// ```mermaid fence with its body byte for byte as it came, the prose around
// it renumbered, and a stream broken into single bytes says exactly what one
// whole string says. Whether the browser draws the body is the other half,
// asked of the renderer's own parser in ui/src/corpus.test.ts on the same
// files.

// corpusSources is how many sources the fixtures may cite. Generous, so a
// new file can use whatever markers the answer it came from used.
const corpusSources = 60

func corpus(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("testdata", "diagrams")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("corpus is empty")
	}
	return out
}

// diagramFence returns the body of the one diagram fence in text, read as
// the browser reads it (markdown.tsx: a `mermaid` or `diagram` tag), and
// whether there is exactly one.
func diagramFence(text string) (string, bool) {
	var body []string
	found, inside, isDiagram := 0, false, false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			if inside {
				inside, isDiagram = false, false
				continue
			}
			inside = true
			tag := infoTag(strings.TrimSpace(l))
			isDiagram = tag == "mermaid" || tag == "diagram"
			if isDiagram {
				found++
			}
			continue
		}
		if inside && isDiagram {
			body = append(body, l)
		}
	}
	return strings.Join(body, "\n"), found == 1
}

func TestCorpus_everyShapeLeavesAsADiagramFence(t *testing.T) {
	for name, text := range corpus(t) {
		t.Run(name, func(t *testing.T) {
			rn := newRenumberer(corpusSources)
			out := rn.feed(text) + rn.flush()

			body, ok := diagramFence(out)
			if !ok {
				t.Fatalf("no single diagram fence in:\n%s", out)
			}
			// The fence is code to this end: what the model wrote is what
			// the renderer gets, a marker-shaped label included.
			if came, _ := diagramFence(text); came != body {
				t.Errorf("fence body was rewritten\ngot:\n%s\nwant:\n%s", body, came)
			}
			if DiagramKind(out) == "" {
				t.Errorf("DiagramKind = %q, want the type the fence names:\n%s", "", body)
			}

			// The golden is what the browser is handed, and the UI reads the
			// same files (ui/src/corpus.test.ts): an answer this end passes
			// and the other end will not draw is the defect only a shared
			// artefact catches.
			golden := filepath.Join("testdata", "diagrams", strings.TrimSuffix(name, ".txt")+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run go test -update): %v", err)
			}
			if out != string(want) {
				t.Errorf("output differs from %s\ngot:\n%s\nwant:\n%s", golden, out, want)
			}
		})
	}
}

func TestCorpus_aStreamSaysWhatOneStringSays(t *testing.T) {
	for name, text := range corpus(t) {
		t.Run(name, func(t *testing.T) {
			whole := newRenumberer(corpusSources)
			want := whole.feed(text) + whole.flush()

			// Byte by byte is the worst split a stream can hand over: every
			// hold in decide() is exercised.
			var b strings.Builder
			rn := newRenumberer(corpusSources)
			for i := 0; i < len(text); i++ {
				b.WriteString(rn.feed(text[i : i+1]))
			}
			b.WriteString(rn.flush())

			if got := b.String(); got != want {
				t.Errorf("streamed differs from whole\nstreamed:\n%s\nwhole:\n%s", got, want)
			}
		})
	}
}

// What must NOT count as a diagram: a code block that happens to hold the
// words, a brace in prose, JSON in the answer. rongo indexes rongo, so a
// Developer answer explaining the format carries these very words.
func TestCorpus_codeThatOnlyLooksLikeADiagramIsLeftAlone(t *testing.T) {
	for name, text := range map[string]string{
		"a config with its own type":   "Config [1].\n\n```json\n{\"pipeline\":{\"type\":\"flow\",\"steps\":2}}\n```\n",
		"the syntax, quoted":           "The prompt asks for [1]:\n\n```go\nconst head = \"flowchart TD\"\n```\n",
		"a brace in running prose":     "The handler returns { on the empty path [1].\n",
		"an object that is not a spec": "State [1].\n\n{\"repo\":\"rongo\",\"branch\":\"master\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			rn := newRenumberer(corpusSources)
			out := rn.feed(text) + rn.flush()
			if DiagramKind(out) != "" {
				t.Errorf("read as a diagram:\n%s", out)
			}
			if !strings.Contains(out, "[1]") {
				t.Errorf("the prose marker did not survive:\n%s", out)
			}
		})
	}
}
