package retrieve

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_shipsTheCodeRungOn(t *testing.T) {
	// The product has the rung; the zero value is off, the way TestDecay and
	// DocDecay read theirs, so the harness's baseline arm is a struct field
	// left alone rather than a second constructor.
	if got := New(testDB(t), fixedEmbedder{vec: queryVec}).CodeWeight; got != WeightKeywordCode {
		t.Errorf("New().CodeWeight = %v, want %v", got, WeightKeywordCode)
	}
	if got := (&Retriever{}).CodeWeight; got != 0 {
		t.Errorf("struct-literal CodeWeight = %v, want the rung off", got)
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
	r.CodeWeight = 0
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

func TestSearch_saysSoWhenTheCodeTextIsNotOneOfTheTexts(t *testing.T) {
	// The rung is found by comparing Code against the texts, so a Code built
	// separately from them turns the rung off and nothing downstream can tell.
	// Loud once, because the operator would otherwise read a table as a lane
	// that never ran.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "src/Promo.java", "send", "promoMailer dispatch of the nightly batch", nearVec)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := New(db, fixedEmbedder{vec: queryVec})
	if _, err := r.Search(context.Background(), Query{
		Texts: []string{"how is the teaser mail sent", "promoMailer dispatchRetry"},
		Code:  "promoMailer dispatchRetry",
		K:     5,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if strings.Contains(buf.String(), "code rung is off") {
		t.Errorf("warned about a code text that IS one of the texts:\n%s", buf.String())
	}

	if _, err := r.Search(context.Background(), Query{
		Texts: []string{"how is the teaser mail sent"},
		Code:  "promoMailer dispatchRetry",
		K:     5,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(buf.String(), "code rung is off") {
		t.Errorf("no warning for a code text that is not among the texts:\n%s", buf.String())
	}
}

func TestSearch_theCodeRungKeepsItsNameUnderASweptWeight(t *testing.T) {
	// The weight is a swept value. Labelling the lane by the number would make
	// a sweep that passes 0.7 report the code rung as the prefix rung — four
	// rungs appearing to move when only one did.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "src/Promo.java", "send", "promoMailer dispatch of the nightly batch", nearVec)

	code := "promoMailer dispatchRetry"
	r := New(db, fixedEmbedder{vec: queryVec})
	r.CodeWeight = WeightKeywordPrefix // 0.7, another rung's constant

	hits, err := r.Search(context.Background(), Query{
		Texts: []string{"how is the teaser mail sent", code}, Code: code, K: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits at all; the fixture does not exercise the rung")
	}
	if lanes := strings.Join(hits[0].Lanes, ","); !strings.Contains(lanes, "keyword:code") {
		t.Errorf("lanes = %s, want the rung named for what it is rather than for its weight", lanes)
	}
}
