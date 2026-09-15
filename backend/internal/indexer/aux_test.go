package indexer

import (
	"context"
	"strings"
	"testing"
)

func TestChunkFile_auxCarriesTheHeaderAndTheSplitIdentifiers(t *testing.T) {
	// Given / When
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), DefaultChunkOptions())
	run := chunkFor(t, chunks, "run")

	// Then: the path and the breadcrumb, which exist nowhere in the keyword
	// lane's source column, and the words AbandonedCartJob is made of, which
	// unicode61 never splits out of it.
	lines := strings.Split(run.AuxText, "\n")
	if len(lines) != 3 {
		t.Fatalf("AuxText has %d lines, want path, breadcrumb, words:\n%s", len(lines), run.AuxText)
	}
	if lines[0] != "shop-backend/src/shop/cart/AbandonedCartJob.java" {
		t.Errorf("line 1 = %q, want repo/path", lines[0])
	}
	if lines[1] != "class AbandonedCartJob > method run" {
		t.Errorf("line 2 = %q, want the symbol breadcrumb", lines[1])
	}
	for _, word := range []string{"abandoned", "cart", "job"} {
		if !strings.Contains(lines[2], word) {
			t.Errorf("line 3 = %q, want the identifier's word %q", lines[2], word)
		}
	}
	if strings.Contains(lines[2], "AbandonedCartJob") {
		t.Errorf("line 3 = %q, want lowercased words rather than the identifier itself", lines[2])
	}
}

func TestChunkFile_auxDoesNotTouchTheEmbeddingOrTheHash(t *testing.T) {
	// Given / When: AuxText is derived, so nothing it carries may reach what is
	// embedded or what keys the embedding cache — otherwise adding a keyword
	// column would re-embed the whole corpus.
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), DefaultChunkOptions())
	run := chunkFor(t, chunks, "run")

	// Then
	chain := []chainPart{{kind: "class", name: "AbandonedCartJob"}, {kind: "method", name: "run"}}
	if want := contentHash("shop-backend", "src/shop/cart/AbandonedCartJob.java", chain, run.RawText); run.ContentHash != want {
		t.Error("ContentHash changed with AuxText; every cached vector would miss")
	}
	if strings.Contains(run.SearchText, "shop-backend/src") {
		t.Errorf("SearchText picked up the header:\n%s", run.SearchText)
	}
	if strings.Contains(run.Text, "abandoned cart job") {
		t.Errorf("the embedded text carries the split words; aux is a keyword column only:\n%s", run.Text)
	}
}

func TestReplaceFile_theAuxColumnFindsTheWordInsideAnIdentifier(t *testing.T) {
	// Given: a chunk whose only occurrence of "abandoned" is inside the class
	// name. unicode61 tokenizes AbandonedCartJob as one token, so the source
	// column alone cannot reach it.
	db := writeDB(t)
	w := NewWriter(db)
	body := "class AbandonedCartJob { void run() { sender.send(); } }"
	chunks := []Chunk{{
		Ordinal: 0, StartLine: 1, EndLine: 1, Symbol: "AbandonedCartJob",
		RawText: body, SearchText: body, Text: "enriched",
		AuxText:     auxText("shop", "src/AbandonedCartJob.java", []chainPart{{kind: "class", name: "AbandonedCartJob"}}, body),
		ContentHash: "h1",
	}}
	if err := w.ReplaceFile(context.Background(), "shop", "src/AbandonedCartJob.java", "sha", "java", 10,
		chunks, [][]float32{vec(1)}, nil, nil); err != nil {
		t.Fatalf("ReplaceFile: %v", err)
	}

	// Then: the unfiltered MATCH — the arm — finds it, and the source column on
	// its own — the baseline, exactly today's lane — does not.
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'abandoned'`); n != 1 {
		t.Errorf("the aux column matched %d rows for a word inside an identifier, want 1", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'raw_text : (abandoned)'`); n != 0 {
		t.Errorf("the source column matched %d rows, want 0 — the baseline arm must be today's lane", n)
	}
	// The path is in the lane too, which it never was: it lived only in the
	// embedded text.
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'shop'`); n != 1 {
		t.Errorf("the aux column matched %d rows for the repository name, want 1", n)
	}
}

func TestReplaceFile_theAuxColumnNeverCarriesAStrippedComment(t *testing.T) {
	// Given: a chunk whose comment was stripped, with its aux derived from the
	// SearchText the way ChunkFile derives it. Both keyword columns must be
	// free of the prose, or the principle holds for one of them and the lane
	// answers from a comment through the other.
	db := writeDB(t)
	w := NewWriter(db)
	stripped := "void run() { sender.send(); }"
	chunks := []Chunk{{
		Ordinal: 0, StartLine: 1, EndLine: 3, Symbol: "run",
		RawText:     "// sends the teaser mail\n" + stripped,
		SearchText:  stripped,
		Text:        "enriched",
		AuxText:     auxText("shop", "src/A.java", []chainPart{{kind: "method", name: "run"}}, stripped),
		ContentHash: "h1",
	}}
	if err := w.ReplaceFile(context.Background(), "shop", "src/A.java", "sha", "java", 10,
		chunks, [][]float32{vec(1)}, nil, nil); err != nil {
		t.Fatalf("ReplaceFile: %v", err)
	}

	// Then
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'teaser'`); n != 0 {
		t.Errorf("a keyword column matched the stripped comment %d times, want 0", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'sender'`); n != 1 {
		t.Errorf("the keyword lane matched the code %d times, want 1", n)
	}
}

func TestChunkFile_auxNeverCarriesAStrippedComment(t *testing.T) {
	// Given: comments stripped. RawText keeps them for citations, and the aux
	// column is a keyword column like the source one — a comment's words
	// reaching it would let the lane answer from prose through a side door.
	opts := DefaultChunkOptions()
	opts.StripComments = true

	// When
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), opts)
	run := chunkFor(t, chunks, "run")

	// Then: "pass" occurs only in the doc comment and in no identifier of the
	// body, so it could only have come from the prose.
	if strings.Contains(run.AuxText, "pass") {
		t.Errorf("AuxText carries a word of the stripped comment:\n%s", run.AuxText)
	}
	if !strings.Contains(run.RawText, "Runs one pass") {
		t.Errorf("RawText lost the comment; a citation must quote the real file:\n%s", run.RawText)
	}
}
