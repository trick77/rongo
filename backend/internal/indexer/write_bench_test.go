package indexer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/store/storetest"
	"github.com/trick77/rongo/internal/symbols"
)

// BenchmarkReplaceFile measures the write half of indexing one file: its
// chunks into the three mirrored tables, its symbols and its tokens, in one
// transaction. Vectors at the production width, because the vec0 insert is
// part of what is measured.
func BenchmarkReplaceFile(b *testing.B) {
	const benchDim = 1536
	db := storetest.Open(b, benchDim)
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES ('shop', 'file:///x', 'master')`); err != nil {
		b.Fatalf("seed repo_state: %v", err)
	}
	body := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) { serve(w, r) }\n", 30)
	const perFile = 12
	chunks := make([]Chunk, perFile)
	vecs := make([][]float32, perFile)
	for i := range chunks {
		chunks[i] = Chunk{Ordinal: i, StartLine: i*30 + 1, EndLine: i*30 + 30, Symbol: "handler",
			Text: "path: a.go\n" + body, RawText: body, SearchText: body, ContentHash: fmt.Sprintf("h%d", i)}
		vecs[i] = make([]float32, benchDim)
	}
	syms := make([]symbols.Symbol, 40)
	for i := range syms {
		syms[i] = symbols.Symbol{Name: fmt.Sprintf("handler%d", i), Kind: "function", Line: i + 1}
	}
	toks := make([]edges.Token, 8)
	for i := range toks {
		toks[i] = edges.Token{Kind: edges.KindRoute, Value: fmt.Sprintf("/api/%d", i), Line: i + 1}
	}
	w := NewWriter(db)

	n := 0
	for b.Loop() {
		path := fmt.Sprintf("src/f%05d.go", n%500)
		n++
		if err := w.ReplaceFile(b.Context(), "shop", path, "sha", "Go", len(body)*perFile, chunks, vecs, syms, toks); err != nil {
			b.Fatalf("ReplaceFile: %v", err)
		}
	}
}
