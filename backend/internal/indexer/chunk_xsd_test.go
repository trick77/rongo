package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

// The fictional order contracts the symbols package tests against.
func orderContract(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "symbols", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestChunkFile_xsdAnchorsOnTopLevelTypes(t *testing.T) {
	body := orderContract(t, "order.xsd")
	syms, err := symbols.ExtractXSD(body)
	if err != nil {
		t.Fatal(err)
	}
	chunks := ChunkFile("shop", "main", "src/main/resources/wsdl/order.xsd", body, syms, DefaultChunkOptions())

	var bySymbol []string
	for _, c := range chunks {
		bySymbol = append(bySymbol, c.Symbol)
	}
	want := []string{"", "placeOrderRequest", "OrderType", "OrderLineType", "OrderStatusType", "AuditGroup", "TraceAttributes"}
	if strings.Join(bySymbol, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks by symbol:\n got %q\nwant %q", bySymbol, want)
	}
	// The type carries its documentation and every field with its
	// cardinality, the nested anonymous type included: one chunk answers
	// "which fields of an order are optional".
	order := chunks[2]
	for _, s := range []string{"An order as the shop submits it.", `name="customerId"`, `name="delivery" minOccurs="0"`, `name="street"`} {
		if !strings.Contains(order.RawText, s) {
			t.Errorf("OrderType chunk lacks %q:\n%s", s, order.RawText)
		}
	}
	if !strings.Contains(order.Text, "complexType OrderType") {
		t.Errorf("breadcrumb missing from:\n%s", order.Text)
	}
}

func TestChunkFile_schemaKindsAnchorOnlyInASchema(t *testing.T) {
	// "message" and "service" are Protobuf kinds too. Outside a schema the
	// chunker's anchor set is the ctags one, unchanged by the schema reader.
	body := []byte("syntax = \"proto3\";\n\nmessage Order {\n  string id = 1;\n}\n\nservice Orders {\n  rpc Get(Order) returns (Order);\n}\n")
	syms := []symbols.Symbol{
		{Name: "Order", Kind: "message", Line: 3, End: 5},
		{Name: "Orders", Kind: "service", Line: 7, End: 9},
	}
	chunks := ChunkFile("shop", "main", "api/order.proto", body, syms, DefaultChunkOptions())
	for _, c := range chunks {
		if c.Symbol != "" {
			t.Errorf("a proto file anchored on %q", c.Symbol)
		}
	}
}

func TestIndexRepo_aSchemaIsReadByTheXSDReaderNotCtags(t *testing.T) {
	rec := &recordingSymbols{}
	h := newHarnessFiles(t, map[string]string{
		"src/main/resources/wsdl/order.xsd":  string(orderContract(t, "order.xsd")),
		"src/main/resources/wsdl/order.wsdl": string(orderContract(t, "order.wsdl")),
		"README.md":                          "# shop\n",
	}, func(inner SymbolExtractor) SymbolExtractor {
		rec.inner = inner
		return rec
	})
	st := h.stateOf(t)

	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	for _, p := range rec.paths {
		if strings.HasSuffix(p, ".xsd") || strings.HasSuffix(p, ".wsdl") {
			t.Errorf("ctags was asked to read %s", p)
		}
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.kind = 'complexType'`); n != 3 {
		t.Errorf("got %d complexType symbols, want 3", n)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE f.path LIKE '%.wsdl' AND s.kind = 'operation'`); n != 2 {
		t.Errorf("got %d operation symbols, want 2", n)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE lang IN ('xsd', 'wsdl')`); n != 2 {
		t.Errorf("got %d files recorded as xsd or wsdl, want 2", n)
	}
}
