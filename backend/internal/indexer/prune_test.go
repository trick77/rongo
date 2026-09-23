package indexer

import (
	"context"
	"log/slog"
	"testing"
)

// orphanedEmbeddings counts cached vectors no chunk uses any more.
const orphanedEmbeddings = `
	SELECT COUNT(*) FROM embed_cache e
	WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.content_hash = e.content_hash)`

func TestIndexRepo_prunesEmbeddingsNoChunkUsesAnyMore(t *testing.T) {
	// Given: a fully indexed repository.
	h := newHarness(t, nil)
	logs := &capture{}
	h.ix.log = slog.New(logs)
	st := h.stateOf(t)
	firstSHA := h.head(t)
	if _, err := h.ix.IndexRepo(context.Background(), st, firstSHA, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	cached := countOf(t, h.db, `SELECT COUNT(*) FROM embed_cache`)

	// When: the README is rewritten, so its old chunks go.
	write(t, h.src, "README.md", "# shop backend\n\nNew text about the cart.\n")
	git(t, h.src, "add", "-A")
	git(t, h.src, "commit", "-qm", "rewrite readme")
	st.LastSHA = firstSHA
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), []string{"README.md"}); err != nil {
		t.Fatalf("incremental IndexRepo() err = %v", err)
	}

	// Then: the old README vectors are gone, everything a chunk uses stays.
	if n := countOf(t, h.db, orphanedEmbeddings); n != 0 {
		t.Errorf("%d cached vectors belong to no chunk", n)
	}
	if n := countOf(t, h.db, `
		SELECT COUNT(DISTINCT c.content_hash) FROM chunks c
		WHERE NOT EXISTS (SELECT 1 FROM embed_cache e WHERE e.content_hash = c.content_hash)`); n != 0 {
		t.Errorf("%d chunks lost their cached vector", n)
	}
	r, ok := logs.find("embedding cache pruned")
	if !ok {
		t.Fatalf("no prune line logged; got %q", logs.messages())
	}
	if v, _ := attr(r, "repo"); v.String() != "shop" {
		t.Errorf("repo = %q, want shop", v.String())
	}
	removed, _ := attr(r, "removed")
	kept, _ := attr(r, "kept")
	if removed.Int64() < 1 || removed.Int64()+kept.Int64() < int64(cached) {
		t.Errorf("removed=%d kept=%d, want at least one removed out of %d", removed.Int64(), kept.Int64(), cached)
	}
}

func TestIndexRepo_logsNothingWhenNothingWasPruned(t *testing.T) {
	// A run that orphaned nothing stays quiet: every poll would say it otherwise.
	h := newHarness(t, nil)
	logs := &capture{}
	h.ix.log = slog.New(logs)
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	if _, ok := logs.find("embedding cache pruned"); ok {
		t.Error("a first index logged a prune")
	}
}

func TestPruneEmbedCache_leavesTheEvalQueryVectorsAlone(t *testing.T) {
	// The eval harness caches its questions' vectors under a "query:" key so a
	// rerun embeds nothing and cannot drift. No chunk carries such a key, and
	// pruning them would make every measurement after an index run re-embed.
	h := newHarness(t, nil)
	if _, err := h.db.Exec(`INSERT INTO embed_cache (content_hash, model, dim, embedding) VALUES ('query:abc', 'm', 1, x'00')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PruneEmbedCache(context.Background(), h.db); err != nil {
		t.Fatalf("PruneEmbedCache() err = %v", err)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM embed_cache WHERE content_hash = 'query:abc'`); n != 1 {
		t.Error("an eval query vector was pruned")
	}
}

func TestPruneEmbedCache_afterAPurgeRemovesTheRepositorysVectors(t *testing.T) {
	// Given: an indexed repository, then its index purged.
	h := newHarness(t, nil)
	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	cached := countOf(t, h.db, `SELECT COUNT(*) FROM embed_cache`)
	if err := h.state.ResetRepo(context.Background(), "shop"); err != nil {
		t.Fatalf("ResetRepo() err = %v", err)
	}

	// When
	removed, kept, err := PruneEmbedCache(context.Background(), h.db)

	// Then
	if err != nil {
		t.Fatalf("PruneEmbedCache() err = %v", err)
	}
	if removed != int64(cached) || kept != 0 {
		t.Errorf("removed=%d kept=%d, want %d and 0", removed, kept, cached)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM embed_cache`); n != 0 {
		t.Errorf("%d vectors of a purged repository survived", n)
	}
}
