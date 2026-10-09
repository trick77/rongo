package xmlutil

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestAttrCollapsesWhitespace(t *testing.T) {
	el := xml.StartElement{Attr: []xml.Attr{{Name: xml.Name{Local: "name"}, Value: " Check\n  order "}}}
	if got := Attr(el, "name"); got != "Check order" {
		t.Fatalf("Attr = %q", got)
	}
	if got := Attr(el, "id"); got != "" {
		t.Fatalf("missing attribute = %q", got)
	}
}

func TestLineMapTagStart(t *testing.T) {
	body := []byte("<a>\n  <b\n    x=\"1\"/>\n</a>\n")
	lm := NewLineMap(body)
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	var lines []int
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if _, ok := tok.(xml.StartElement); ok {
			lines = append(lines, lm.TagStart(dec.InputOffset()))
		}
	}
	if len(lines) != 2 || lines[0] != 1 || lines[1] != 2 {
		t.Fatalf("tag starts = %v, want [1 2]", lines)
	}
	if got := lm.LineOf(0); got != 1 {
		t.Fatalf("LineOf(0) = %d", got)
	}
}
