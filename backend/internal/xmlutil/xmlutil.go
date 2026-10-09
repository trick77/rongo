// Package xmlutil holds what every encoding/xml reader here needs and the
// decoder does not give: an attribute by local name, and the line a token
// sits on. A leaf, so the BPMN parser and the symbol extractors can share it
// without one importing the other.
package xmlutil

import (
	"bytes"
	"encoding/xml"
	"sort"
	"strings"
)

// Attr returns one attribute with its whitespace collapsed: a modeller breaks
// a long label across the canvas with &#10;, which the decoder turns into a
// newline, and a newline has no place in a name or a breadcrumb line.
func Attr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name {
			return strings.Join(strings.Fields(a.Value), " ")
		}
	}
	return ""
}

// LineMap maps an XML decoder's byte offsets back to 1-based lines.
type LineMap struct {
	body   []byte
	starts []int // byte offset each line begins at
}

// NewLineMap indexes body's line starts once.
func NewLineMap(body []byte) LineMap {
	starts := []int{0}
	for i, b := range body {
		if b == '\n' && i+1 < len(body) {
			starts = append(starts, i+1)
		}
	}
	return LineMap{body: body, starts: starts}
}

// LineOf is the line holding the byte at offset.
func (m LineMap) LineOf(offset int64) int {
	return sort.Search(len(m.starts), func(i int) bool { return m.starts[i] > int(offset) })
}

// TagStart is the line a start tag opens on. InputOffset after a start tag is
// the byte past its ">"; the tag's own line is where its "<" sits. A "<"
// cannot occur inside a tag, so the last one before end is it.
func (m LineMap) TagStart(end int64) int {
	i := bytes.LastIndexByte(m.body[:end], '<')
	if i < 0 {
		return m.LineOf(end - 1)
	}
	return m.LineOf(int64(i))
}
