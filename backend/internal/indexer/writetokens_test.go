package indexer

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/edges"
)

func TestReplaceFile_writesIntegrationTokens(t *testing.T) {
	// Given
	db := writeDB(t)
	testee := NewWriter(db)

	// When
	err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil,
		[]edges.Token{
			{Kind: edges.KindDestination, Value: "shipping-task", Line: 18},
			{Kind: edges.KindRoute, Value: "/orders", Line: 50},
		})
	if err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}

	// Then
	var kind, value string
	var line int
	if err := db.QueryRow(`
		SELECT t.kind, t.value, t.line FROM integration_tokens t
		JOIN files f ON f.id = t.file_id
		WHERE f.repo = 'shop' AND t.value = 'shipping-task'`).Scan(&kind, &value, &line); err != nil {
		t.Fatalf("read the token back: %v", err)
	}
	if kind != string(edges.KindDestination) || line != 18 {
		t.Errorf("stored %s %q at line %d, want a destination at 18", kind, value, line)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM integration_tokens`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("stored %d tokens, want 2", n)
	}
}

// A token belongs to the version of the file it was read from. Re-indexing has
// to replace them with the chunks, or an edge outlives the line that declared
// it and retrieval crosses a boundary the code no longer has.
func TestReplaceFile_replacesIntegrationTokensWithTheFile(t *testing.T) {
	// Given: a file indexed with one destination.
	db := writeDB(t)
	testee := NewWriter(db)
	if err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "sha1", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil,
		[]edges.Token{{Kind: edges.KindDestination, Value: "old-queue", Line: 1}}); err != nil {
		t.Fatalf("first ReplaceFile: %v", err)
	}

	// When: the same path is indexed again, and the queue has been renamed.
	if err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "sha2", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil,
		[]edges.Token{{Kind: edges.KindDestination, Value: "new-queue", Line: 1}}); err != nil {
		t.Fatalf("second ReplaceFile: %v", err)
	}

	// Then: only the new one is left.
	var values []string
	rows, err := db.Query(`SELECT value FROM integration_tokens`)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		values = append(values, v)
	}
	if len(values) != 1 || values[0] != "new-queue" {
		t.Errorf("tokens after re-index = %v, want [new-queue]", values)
	}
}

func TestDeleteFile_removesIntegrationTokens(t *testing.T) {
	// Given
	db := writeDB(t)
	testee := NewWriter(db)
	if err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil,
		[]edges.Token{{Kind: edges.KindRoute, Value: "/orders", Line: 1}}); err != nil {
		t.Fatalf("ReplaceFile: %v", err)
	}

	// When
	if err := testee.DeleteFile(context.Background(), "shop", "src/A.java"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	// Then
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM integration_tokens`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d tokens survived the deletion, want 0", n)
	}
}
