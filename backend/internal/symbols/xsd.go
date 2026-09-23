package symbols

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
)

// ExtractXSD reads an XML Schema or a WSDL 1.1 contract and returns one Symbol
// per named top-level definition, so the chunker anchors on "complexType
// OrderType" the way it anchors on "method run" in Java. ctags has no schema
// parser: its XML parser tags id attributes, namespace prefixes and the root
// element, which a schema has none of worth anchoring on.
//
// It is a plain XML pass like ExtractBPMN, deterministic, nothing stored the
// file does not say. Namespace prefixes are ignored: xs:, xsd: and a default
// namespace all occur, and the local name identifies the definition.
//
// What anchors:
//
//   - A schema's direct children that carry a name: element, complexType,
//     simpleType, group, attributeGroup, attribute. The elements of a
//     sequence are fields and an anonymous nested type has no name; both
//     stay inside the definition's chunk, which is where "which fields are
//     required" is answered, minOccurs beside each.
//   - A WSDL's message, portType, binding and service, and the operations of
//     a portType, scoped to it. A binding's operations repeat the port type's
//     and stay in the binding. An inline schema under types is read by the
//     schema rules.
//
// A file that does not parse as XML is an error, and the caller falls back to
// line windows the way it does for a ctags failure.
func ExtractXSD(body []byte) ([]Symbol, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	lm := newLineMap(body)

	type open struct {
		sym   int // index into out, -1 for elements that are not symbols
		local string
		name  string
	}
	var (
		stack []open
		out   []Symbol
	)
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xsd: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			o := open{sym: -1, local: t.Name.Local, name: attr(t, "name")}
			var parent open
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			if o.name != "" && xsdAnchors[parent.local][o.local] {
				s := Symbol{Name: o.name, Kind: o.local, Line: lm.tagStart(dec.InputOffset())}
				if parent.local == "portType" {
					s.Scope, s.ScopeKind = parent.name, "portType"
				}
				out = append(out, s)
				o.sym = len(out) - 1
			}
			stack = append(stack, o)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("xsd: end element without a start")
			}
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if o.sym >= 0 {
				out[o.sym].End = lm.lineOf(dec.InputOffset() - 1)
			}
		}
	}
	if len(stack) > 0 {
		// The non-strict decoder reports a truncated file as a clean EOF
		// with elements still open; that is not a schema.
		return nil, fmt.Errorf("xsd: %d elements unclosed at end of file", len(stack))
	}
	return out, nil
}

// xsdAnchors maps a parent's local name to the children of it that become
// symbols when named. The child's local name doubles as the symbol kind.
var xsdAnchors = map[string]map[string]bool{
	"schema": {
		"element": true, "complexType": true, "simpleType": true,
		"group": true, "attributeGroup": true, "attribute": true,
	},
	"definitions": {"message": true, "portType": true, "binding": true, "service": true},
	"portType":    {"operation": true},
}

// XSDStructuralKinds are the symbol kinds ExtractXSD emits. Several are also
// ctags kinds of other languages (a Protobuf message, a SQL service, a DTD
// element), so the chunker anchors on them only in a schema file.
func XSDStructuralKinds() []string {
	var kinds []string
	for _, children := range xsdAnchors {
		for k := range children {
			kinds = append(kinds, k)
		}
	}
	sort.Strings(kinds)
	return kinds
}
