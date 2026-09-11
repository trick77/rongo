package retrieve

import (
	"context"
	"strings"
	"testing"
)

// TestSearch_withARerankerReordersADeeperPoolAndCutsToK: the reranker sees a
// pool deeper than K, the lanes reach as deep as the pool, and what the model
// picked lands first in the K the caller asked for.
func TestSearch_withARerankerReordersADeeperPoolAndCutsToK(t *testing.T) {
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "Near1.java", "a", "irgendein anderer text", nearVec)
	addChunk(t, db, "shop", "Near2.java", "b", "yet another text", nearVec)
	addChunk(t, db, "shop", "Answer.java", "answer", "the answer sits mid distance", midVec)
	var prompt string
	r := New(db, fixedEmbedder{vec: queryVec})
	r.Candidates = 2
	// The model picks whichever result is Answer.java, by its number.
	r.Reranker = NewLLMReranker(rerankLLM(t, "", &prompt), 3)
	r.Reranker.llm = rerankLLMPicking(t, "Answer.java", &prompt)

	hits, err := r.Search(context.Background(), Query{Text: "anything", K: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want the cut to K", len(hits))
	}
	if hits[0].Path != "Answer.java" {
		t.Errorf("first hit = %s, want the model's pick ahead of the fused order", hits[0].Path)
	}
	if !strings.Contains(prompt, "Answer.java") {
		t.Errorf("the pool shown to the model did not reach the third result; lanes must fetch as deep as the pool:\n%s", prompt)
	}
}
