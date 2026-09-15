package retrieve

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// addChunkAux is addChunk with the keyword lane's second column filled, which
// is what the indexer writes. Separate rather than folded in: every other
// fixture is about a lane that does not know aux exists, and it must keep
// measuring that lane.
func addChunkAux(t *testing.T, db *sql.DB, repo, path, symbol, raw, aux string, vec []float32) int64 {
	t.Helper()
	id := addChunk(t, db, repo, path, symbol, raw, vec)
	if _, err := db.Exec(`UPDATE chunks_fts SET aux = ? WHERE rowid = ?`, aux, id); err != nil {
		t.Fatalf("set aux: %v", err)
	}
	return id
}

func TestSearchKeywordIn_auxOffIsTheLaneAsItShipped(t *testing.T) {
	// Given: a chunk whose only "abandoned" is in the aux column, where the
	// indexer puts the words inside AbandonedCartJob.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunkAux(t, db, "shop", "src/AbandonedCartJob.java", "run",
		"class AbandonedCartJob { void run() {} }",
		"shop/src/AbandonedCartJob.java\nclass AbandonedCartJob\nabandoned cart job run", nearVec)
	s := NewStore(db)

	// When / Then: off is the baseline arm and must not see it at all ...
	hits, err := s.SearchKeywordIn(context.Background(), BuildFTSMatch("abandoned"), 10, nil, nil, 0)
	if err != nil {
		t.Fatalf("aux off: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("aux off returned %d hits, want the lane restricted to the source column", len(hits))
	}

	// ... and on, the same MATCH reaches the word inside the identifier.
	hits, err = s.SearchKeywordIn(context.Background(), BuildFTSMatch("abandoned"), 10, nil, nil, DefaultAuxWeight)
	if err != nil {
		t.Fatalf("aux on: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("aux on returned %d hits, want the chunk whose identifier holds the word", len(hits))
	}
	if hits[0].Path != "src/AbandonedCartJob.java" {
		t.Errorf("hit = %s, want the chunk with the identifier", hits[0].Path)
	}
}

func TestSearchKeywordIn_theWeightKeepsABodyMatchAhead(t *testing.T) {
	// Given: two chunks, one holding the word in its source and one only in its
	// header. The aux column is the weaker evidence by construction — a path is
	// not a statement about what the code does — so the ordering is what the
	// column weight buys, not the membership.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunkAux(t, db, "shop", "src/other/Helper.java", "help",
		"void help() { int n = 0; return; }",
		"shop/src/cart/Helper.java\nclass Helper > method help\ncart helper", nearVec)
	addChunkAux(t, db, "shop", "src/cart/Body.java", "run",
		"void run() { cart.checkout(); cart.clear(); }",
		"shop/src/cart/Body.java\nclass Body > method run\nbody run", nearVec)

	// When
	hits, err := NewStore(db).SearchKeywordIn(context.Background(), BuildFTSMatch("cart"), 10, nil, nil, DefaultAuxWeight)
	if err != nil {
		t.Fatalf("SearchKeywordIn: %v", err)
	}

	// Then
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want both the body match and the header match", len(hits))
	}
	if hits[0].Path != "src/cart/Body.java" {
		t.Errorf("first hit = %s, want the chunk that literally contains the word", hits[0].Path)
	}
}

func TestNew_shipsTheAuxColumnOnAndAStructLiteralOff(t *testing.T) {
	// The zero value is off, the way TestDecay and DocDecay read theirs: a
	// Retriever built by a struct literal keeps the lane that shipped before
	// the column existed.
	if got := New(testDB(t), fixedEmbedder{vec: queryVec}).AuxWeight; got != DefaultAuxWeight {
		t.Errorf("New().AuxWeight = %v, want %v", got, DefaultAuxWeight)
	}
	if got := (&Retriever{}).AuxWeight; got != 0 {
		t.Errorf("struct-literal AuxWeight = %v, want the column off", got)
	}
	if got := New(testDB(t), fixedEmbedder{vec: queryVec}).CodeWeight; got != 0 {
		t.Errorf("New().CodeWeight = %v, want the code rung off until it is measured", got)
	}
}

func TestLaneName_labelsTheCodeRung(t *testing.T) {
	if got := laneName(WeightKeywordCode); got != "keyword:code" {
		t.Errorf("laneName(WeightKeywordCode) = %q, want %q", got, "keyword:code")
	}
	if got := laneName(WeightKeywordAny); got != "keyword:any" {
		t.Errorf("laneName(WeightKeywordAny) = %q, want %q", got, "keyword:any")
	}
}

func TestSearch_theCodeTextsFloorCarriesItsOwnWeight(t *testing.T) {
	// Given: a chunk the OR floor of the code-terms text reaches. That rung is
	// "one of these words appears here", and the claim is stronger when the
	// words are guessed identifiers than when they are the question's prose.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "src/Promo.java", "send", "promoMailer dispatch of the nightly batch", nearVec)

	code := "promoMailer dispatchRetry"
	q := Query{Texts: []string{"how is the teaser mail sent", code}, Code: code, K: 5}

	// When: off, the floor is the prose floor ...
	r := New(db, fixedEmbedder{vec: queryVec})
	hits, err := r.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits at all; the fixture does not exercise the rung")
	}
	if lanes := strings.Join(hits[0].Lanes, ","); strings.Contains(lanes, "keyword:code") {
		t.Errorf("lanes = %s, want no code rung while CodeWeight is off", lanes)
	}

	// ... and on, the same rung over the same text becomes its own lane.
	r.CodeWeight = WeightKeywordCode
	hits, err = r.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits with the code rung on")
	}
	if lanes := strings.Join(hits[0].Lanes, ","); !strings.Contains(lanes, "keyword:code") {
		t.Errorf("lanes = %s, want the code text's floor labelled as its own rung", lanes)
	}
}
