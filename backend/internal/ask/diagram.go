package ask

import (
	"encoding/json"
	"strings"
)

// A diagram in an answer is a fenced block tagged "diagram" holding a small
// JSON spec (see answerDiagram). Each node carries the markers it rests on in
// its src array, so the citation invariant holds inside the picture: the chip
// on a node is the chip in the prose, and opens the same viewer.
//
// The backend reads only the src arrays. A label such as "parts[2]" is a
// label, and scanning the fence as text would mint an entry the model never
// made — the fabrication citationsFor exists to prevent. The backend is
// deliberately more lenient than the browser (no caps, no reference checks):
// when the browser rejects a spec it falls back to showing the raw block, so
// the src arrays behind any minted citation stay visible to the reader.

// fence is one fenced block of an answer: its tag (the info string), its
// body, and whether the closing fence ever arrived.
type fence struct {
	tag    string
	body   string
	closed bool
}

// splitFences blanks every fenced block out of the answer and returns the
// blocks in order. Blanked rather than removed so the prose keeps its shape;
// an unclosed fence swallows the remainder, as it should.
func splitFences(s string) (prose string, fences []fence) {
	var b strings.Builder
	rest := s
	for {
		open := strings.Index(rest, "```")
		if open < 0 {
			break
		}
		b.WriteString(rest[:open])
		rest = rest[open+3:]
		var f fence
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			f.tag = strings.TrimSpace(rest[:nl])
			rest = rest[nl+1:]
		} else {
			f.tag = strings.TrimSpace(rest)
			rest = ""
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			f.body = rest[:end]
			f.closed = true
			rest = rest[end+3:]
		} else {
			f.body = rest
			rest = ""
		}
		fences = append(fences, f)
	}
	b.WriteString(rest)
	return b.String(), fences
}

// diagramMarkers returns the src markers of every closed, well-formed
// diagram fence. Invalid JSON contributes nothing: the browser shows such a
// block as code, and code carries no citations.
func diagramMarkers(fences []fence) []int {
	var out []int
	for _, f := range fences {
		if f.tag != "diagram" || !f.closed {
			continue
		}
		var spec struct {
			Nodes []struct{ Src []int }
			Steps []struct{ Src []int }
		}
		if err := json.Unmarshal([]byte(f.body), &spec); err != nil {
			continue
		}
		for _, n := range spec.Nodes {
			out = append(out, n.Src...)
		}
		for _, s := range spec.Steps {
			out = append(out, s.Src...)
		}
	}
	return out
}
