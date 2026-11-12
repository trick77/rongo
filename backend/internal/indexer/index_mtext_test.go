package indexer

import (
	"context"
	"strings"
	"testing"
)

func TestIndexRepo_anMTextFileIsReadByTheMTextReaderNotCtags(t *testing.T) {
	rec := &recordingSymbols{}
	h := newHarnessFiles(t, map[string]string{
		"Forms/Demo_Form.mtxt":       string(orderContract(t, "Demo_Form.mtxt")),
		"Blocks/#Form_T1000_de.mtxm": string(orderContract(t, "#Form_T1000_de.mtxm")),
		"README.md":                  "# letters\n",
	}, func(inner SymbolExtractor) SymbolExtractor {
		rec.inner = inner
		return rec
	})
	st := h.stateOf(t)

	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	for _, p := range rec.paths {
		if strings.HasSuffix(p, ".mtxt") || strings.HasSuffix(p, ".mtxm") {
			t.Errorf("ctags was asked to read %s", p)
		}
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE lang = 'mtext'`); n != 2 {
		t.Errorf("got %d files recorded as mtext, want 2", n)
	}
	// No M/TEXT anchor is a definition: the name walk never reaches a
	// template. An include finds the file by its folded stem instead.
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE f.lang = 'mtext'`); n != 0 {
		t.Errorf("got %d M/TEXT symbols, want none", n)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM files
		WHERE (path = 'Blocks/#Form_T1000_de.mtxm' AND stem = 'form_t1000_de')
		   OR (path = 'Forms/Demo_Form.mtxt' AND stem = 'demo_form')
		   OR (path = 'README.md' AND stem = '')`); n != 3 {
		t.Errorf("stems are not recorded as folded M/TEXT stems only")
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = 'Forms/Demo_Form.mtxt' AND c.symbol <> ''`); n != 4 {
		t.Errorf("got %d anchored chunks in the template, want 4", n)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(*) FROM integration_tokens t JOIN files f ON f.id = t.file_id
		WHERE f.path = 'Forms/Demo_Form.mtxt' AND t.kind = 'include'`); n != 4 {
		t.Errorf("got %d include tokens for the template, want 4", n)
	}
}
