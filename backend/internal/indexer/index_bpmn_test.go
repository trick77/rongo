package indexer

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

// recordingSymbols notes which paths reached the ctags extractor, so the test
// can show a process model never did.
type recordingSymbols struct {
	inner SymbolExtractor
	paths []string
}

func (r *recordingSymbols) Extract(ctx context.Context, path string, body []byte) ([]symbols.Symbol, error) {
	r.paths = append(r.paths, path)
	return r.inner.Extract(ctx, path, body)
}

func TestIndexRepo_aProcessModelIsReadByTheBPMNReaderNotCtags(t *testing.T) {
	rec := &recordingSymbols{}
	h := newHarnessFiles(t, map[string]string{
		"src/main/resources/workflow/order-intake.bpmn": string(orderIntakeModel(t)),
		"README.md": "# shop\n",
	}, func(inner SymbolExtractor) SymbolExtractor {
		rec.inner = inner
		return rec
	})
	st := h.stateOf(t)

	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	for _, p := range rec.paths {
		if strings.HasSuffix(p, ".bpmn") {
			t.Errorf("ctags was asked to read %s", p)
		}
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE f.path LIKE '%.bpmn' AND s.kind = 'serviceTask'`); n != 2 {
		t.Errorf("got %d serviceTask symbols, want 2", n)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path LIKE '%.bpmn' AND c.raw_text LIKE '%dc:Bounds%'`); n != 0 {
		t.Errorf("%d chunks carry diagram coordinates", n)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE path LIKE '%.bpmn' AND lang = 'bpmn'`); n != 1 {
		t.Errorf("the model's language is not recorded as bpmn")
	}
}
