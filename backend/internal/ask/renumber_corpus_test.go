package ask

import (
	"encoding/json"
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
// that make a diagram a diagram: it leaves the renumberer inside a ```diagram
// fence, every number in a src array is one of the reader's, and a stream
// broken into single bytes says exactly what one whole string says.

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

// diagramFence returns the body of the one ```diagram fence in text, and
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
			isDiagram = infoTag(strings.TrimSpace(l)) == "diagram"
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
				t.Fatalf("no single ```diagram fence in:\n%s", out)
			}

			// The body has to be the JSON the browser parses. A src written
			// as a chain of groups is not, which is the whole reason #45
			// existed.
			var spec map[string]any
			if err := json.Unmarshal([]byte(body), &spec); err != nil {
				t.Fatalf("fence body is not JSON: %v\n%s", err, body)
			}
			if spec["type"] != "flow" && spec["type"] != "sequence" {
				t.Fatalf("type = %v, want flow or sequence", spec["type"])
			}

			// Every chip the picture will draw is one of the reader's
			// numbers. A prompt index left behind here puts a wrong source
			// under a node.
			n := len(rn.citations(fakeSources(corpusSources)))
			for _, m := range srcNumbers(t, spec) {
				if m < 1 || m > n {
					t.Errorf("src holds %d, outside the reader's 1..%d", m, n)
				}
			}

			// The golden is what the browser is handed, and the UI reads the
			// same files (ui/src/corpus.test.ts): a spec this end normalises
			// and the other end will not draw is the same defect as one
			// neither touches, and only a shared artefact catches it.
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

// The other half of reading a block by its content: what must NOT be read as
// a diagram. rongo indexes rongo, so a Developer answer explaining the format
// carries these very words, and a code block turned into a picture is the
// same defect as a picture turned into a code block.
func TestCorpus_codeThatOnlyLooksLikeASpecIsLeftAlone(t *testing.T) {
	for name, text := range map[string]string{
		"a config with its own type":     "Config [1].\n\n```json\n{\"pipeline\":{\"type\":\"flow\",\"steps\":2}}\n```\n",
		"the format, quoted":             "The prompt asks for [1]:\n\n```json\n{\"shape\":\"{\\\"type\\\":\\\"flow\\\"}\"}\n```\n",
		"a type this file does not draw": "Not ours [1].\n\n```json\n{\"type\":\"pie\",\"slices\":[]}\n```\n",
		"a brace in running prose":       "The handler returns { on the empty path [1].\n",
		"an object that is not a spec":   "State [1].\n\n{\"repo\":\"rongo\",\"branch\":\"master\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			rn := newRenumberer(corpusSources)
			out := rn.feed(text) + rn.flush()
			if strings.Contains(out, "```diagram") {
				t.Errorf("read as a diagram:\n%s", out)
			}
		})
	}
}

// srcNumbers collects every src entry of a spec.
func srcNumbers(t *testing.T, spec map[string]any) []int {
	t.Helper()
	var out []int
	for _, key := range []string{"nodes", "steps"} {
		items, _ := spec[key].([]any)
		for _, it := range items {
			rec, _ := it.(map[string]any)
			arr, _ := rec["src"].([]any)
			for _, v := range arr {
				f, ok := v.(float64)
				if !ok {
					t.Errorf("src holds %#v, not a number", v)
					continue
				}
				out = append(out, int(f))
			}
		}
	}
	return out
}

// fakeSources is a numbered list long enough for citations() to resolve
// anything the corpus cites. Only the count matters here.
func fakeSources(n int) []Source {
	out := make([]Source, n)
	for i := range out {
		out[i] = Source{Repo: "repo", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 2, SHA: "sha"}
	}
	return out
}
