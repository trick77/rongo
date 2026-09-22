package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// TestFlagshipChunk answers the question every file-grained table leaves open.
//
// hopAndReason matches a part on repo and path, so "1/1 parts, hop 0 hit"
// says SOME window of a 269-line file reached the sources. It does not say the
// window holding the line the question is about did. A converter whose method
// header is gathered while the mapping sits in a chunk that is not would score
// a perfect part and hand the answer model nothing to answer from — and no
// measurement in this package would show it.
//
// So: of the chunks the flagship's own search returns, does any contain the
// mapping itself?
func TestFlagshipChunk(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	retriever := evalRetriever(t, db)
	expansions := loadFlowExpansions(t)

	// The flagship is private code: its question, the line it turns on and
	// the file holding it come from the corpus's own runner, never from here.
	question, needle, file := flagshipCase(t)
	e, ok := expansions[question]
	if !ok {
		t.Skipf("no frozen expansion for the flagship; run TestExpandFlowQuestions")
	}

	// The same query the gathered arms run, so this reads against their table.
	hits, err := retriever.Search(ctx, retrieve.Query{
		Texts: e.Texts, Code: e.code(t), Repos: e.Repos, Question: question, K: gatherSearchK})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var inFile, withNeedle int
	for i, h := range hits {
		if !strings.HasSuffix(h.Path, file) {
			continue
		}
		inFile++
		has := strings.Contains(h.RawText, needle)
		if has {
			withNeedle++
		}
		t.Logf("rank %2d chunk %d lines %d-%d symbol %-24s contains %s: %v",
			i, h.ChunkID, h.StartLine, h.EndLine, h.Symbol, needle, has)
	}

	t.Logf("\nthe converter contributed %d chunks to the hit list, %d containing %s",
		inFile, withNeedle, needle)
	if inFile == 0 {
		t.Fatalf("the converter is not in the hit list at all")
	}
	if withNeedle == 0 {
		t.Errorf("the converter reached the sources but NOT the window holding %s: "+
			"every file-grained table scores this question perfect while the answer "+
			"model never sees the mapping", needle)
	}
}
