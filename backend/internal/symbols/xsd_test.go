package symbols

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// orderSchema and orderWSDL are fictional contracts in the shape service
// schemas come in: top-level elements and types with nested fields and an
// anonymous type, an extension, a restriction, a group; and a WSDL with an
// inline schema, a message, a port type with two operations, a binding that
// repeats one of them, and a service. The indexer's chunk test reads the
// schema too.
var (
	orderSchema = readTestdata("order.xsd")
	orderWSDL   = readTestdata("order.wsdl")
)

func readTestdata(name string) string {
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return string(body)
}

type wantSym struct {
	name, kind, scope, scopeKind string
	line, end                    int
}

func checkSymbols(t *testing.T, syms []Symbol, wants []wantSym) {
	t.Helper()
	if len(syms) != len(wants) {
		var got []string
		for _, s := range syms {
			got = append(got, s.Kind+" "+s.Name)
		}
		t.Fatalf("got %d symbols, want %d:\n%s", len(syms), len(wants), strings.Join(got, "\n"))
	}
	for i, w := range wants {
		s := syms[i]
		if s.Name != w.name || s.Kind != w.kind || s.Scope != w.scope || s.ScopeKind != w.scopeKind || s.Line != w.line || s.End != w.end {
			t.Errorf("symbol %d: got %+v, want %+v", i, s, w)
		}
	}
}

// Every named top-level definition is a symbol spanning its whole body. The
// elements inside a sequence are fields and the type nested in "delivery" is
// anonymous: neither is a symbol, anchoring on them would cut OrderType apart
// from the fields a "which are required" question is about.
func TestExtractXSDAnchorsOnTopLevelDefinitions(t *testing.T) {
	syms, err := ExtractXSD([]byte(orderSchema))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"placeOrderRequest", "element", "", "", 8, 8},
		{"OrderType", "complexType", "", "", 10, 25},
		{"OrderLineType", "complexType", "", "", 27, 36},
		{"OrderStatusType", "simpleType", "", "", 38, 43},
		{"AuditGroup", "group", "", "", 45, 49},
		{"TraceAttributes", "attributeGroup", "", "", 51, 53},
	})
}

// A WSDL's inline schema is read by the schema rules; operations are scoped
// to their port type; the binding's repeat of an operation is part of the
// binding, not a second operation symbol.
func TestExtractXSDReadsWSDL(t *testing.T) {
	syms, err := ExtractXSD([]byte(orderWSDL))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"CancelOrderType", "complexType", "", "", 9, 13},
		{"placeOrderRequest", "message", "", "", 16, 18},
		{"OrderPortType", "portType", "", "", 19, 26},
		{"placeOrder", "operation", "OrderPortType", "portType", 20, 22},
		{"cancelOrder", "operation", "OrderPortType", "portType", 23, 25},
		{"OrderBinding", "binding", "", "", 27, 32},
		{"OrderService", "service", "", "", 33, 37},
	})
}

func TestExtractXSDPrefixAgnostic(t *testing.T) {
	// The same schema under the "xs" prefix and as the default namespace.
	for _, body := range []string{
		strings.ReplaceAll(strings.ReplaceAll(orderSchema, "xsd:", "xs:"), "xmlns:xsd=", "xmlns:xs="),
		strings.ReplaceAll(strings.ReplaceAll(orderSchema, "<xsd:", "<"), "</xsd:", "</"),
	} {
		syms, err := ExtractXSD([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(syms) != 6 {
			t.Errorf("got %d symbols, want 6", len(syms))
		}
	}
}

func TestExtractXSDRejectsNonXML(t *testing.T) {
	if _, err := ExtractXSD([]byte("<xsd:schema><xsd:complexType")); err == nil {
		t.Error("truncated XML produced no error")
	}
}

func TestExtractXSDReadsALatin1Schema(t *testing.T) {
	// Older contracts declare ISO-8859-1 or windows-1252. The names that
	// anchor are ASCII; the documentation around them is not, and must not
	// cost the file its anchors or shift a line.
	body := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<xsd:schema xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\">\n" +
		"\t<xsd:complexType name=\"ParcelType\">\n\t\t<xsd:annotation><xsd:documentation>Gr\xf6sse des Pakets</xsd:documentation></xsd:annotation>\n\t</xsd:complexType>\n" +
		"\t<xsd:simpleType name=\"WeightType\">\n\t\t<xsd:restriction base=\"xsd:decimal\"/>\n\t</xsd:simpleType>\n</xsd:schema>\n"
	syms, err := ExtractXSD([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"ParcelType", "complexType", "", "", 3, 5},
		{"WeightType", "simpleType", "", "", 6, 8},
	})
}

func TestExtractXSDEmpty(t *testing.T) {
	syms, err := ExtractXSD([]byte("  \n"))
	if err != nil || syms != nil {
		t.Errorf("ExtractXSD(blank) = %v, %v; want nil, nil", syms, err)
	}
}

func TestXSDStructuralKinds(t *testing.T) {
	kinds := map[string]bool{}
	for _, k := range XSDStructuralKinds() {
		kinds[k] = true
	}
	syms, err := ExtractXSD([]byte(orderSchema + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	wsdl, err := ExtractXSD([]byte(orderWSDL))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(syms, wsdl...) {
		if !kinds[s.Kind] {
			t.Errorf("kind %q is emitted but not structural: the chunker would never anchor on it", s.Kind)
		}
	}
}
