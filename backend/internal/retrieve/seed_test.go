package retrieve

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBuildAccessorTerms_prefixesTheConventionalVerbs(t *testing.T) {
	got := BuildAccessorTerms([]string{"anzahlkinder"})
	for _, want := range []string{"getanzahlkinder", "setanzahlkinder", "withanzahlkinder"} {
		if !contains(got, want) {
			t.Errorf("BuildAccessorTerms = %v, want it to contain %q", got, want)
		}
	}
}

func TestBuildAccessorTerms_dropsDuplicates(t *testing.T) {
	// Two question terms can derive the same accessor; a lane fused twice
	// double-counts, and a seed taken twice pays twice.
	got := BuildAccessorTerms([]string{"anzahlkinder", "anzahlkinder"})
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Fatalf("BuildAccessorTerms = %v, want no duplicates", got)
		}
		seen[g] = true
	}
}

func TestSeedHits_shipsAtTheMeasuredCeiling(t *testing.T) {
	// New ships the seed on, at the ceiling the Go corpus measured as
	// byte-identical to the product. A struct-literal Retriever still gets
	// zero, which is the harness's baseline arm.
	if got := New(nil, fixedEmbedder{}).SeedFiles; got != DefaultSeedFiles {
		t.Errorf("New sets SeedFiles = %d, want %d", got, DefaultSeedFiles)
	}
	if (&Retriever{}).SeedFiles != 0 {
		t.Errorf("a struct-literal Retriever seeds, want the zero value to mean off")
	}
}

func TestSeedHits_zeroCeilingIsOff(t *testing.T) {
	// Off must cost nothing and reach no database: the harness's baseline arm
	// builds a Retriever by hand and must not need a store to run.
	got, err := (&Retriever{}).SeedHits(context.Background(), Query{Text: "anything"})
	if err != nil {
		t.Fatalf("SeedHits: %v", err)
	}
	if got != nil {
		t.Errorf("SeedHits = %v with SeedFiles 0, want nothing", got)
	}
}

func TestSeedHits_takesASelectiveAccessorAndLeavesTheFieldName(t *testing.T) {
	// The measured shape: the FIELD name is in many files and says nothing,
	// while its ACCESSOR is in few and says where the value is read. Only the
	// second is a seed.
	db := testDB(t)
	addRepo(t, db, "estate", "master")
	// Twelve files merely NAME the field. Over any sane ceiling.
	for i := 0; i < 12; i++ {
		addChunk(t, db, "estate", fmt.Sprintf("dto/Named%d.java", i), "Named",
			"private Integer anzahlFahrzeuge;", nearVec)
	}
	// One file READS it through the accessor.
	mapping := addChunk(t, db, "estate", "syrius/Converter.java", "toType",
		converterCodeOnly, farVec)

	r := New(db, fixedEmbedder{})
	r.SeedFiles = 5

	got, err := r.SeedHits(context.Background(), Query{
		Texts:    []string{"wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"},
		Question: "wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben",
	})
	if err != nil {
		t.Fatalf("SeedHits: %v", err)
	}
	if !hasChunk(got, mapping) {
		t.Fatalf("seeds = %v, want the chunk the accessor reaches", hitPaths(got))
	}
	for _, h := range got {
		if strings.HasPrefix(h.Path, "dto/") {
			t.Errorf("seeds = %v, want the twelve files merely naming the field left out", hitPaths(got))
			break
		}
	}
}

func TestSeedHits_refusesATermTooManyFilesHold(t *testing.T) {
	// The ceiling is the whole defence: a wrong seed is worse than a missing
	// one, because it is paid out of the answer's budget before the walk runs.
	db := testDB(t)
	addRepo(t, db, "estate", "master")
	for i := 0; i < 12; i++ {
		addChunk(t, db, "estate", fmt.Sprintf("a/File%d.java", i), "F",
			"x.getAnzahlFahrzeuge();", nearVec)
	}

	r := New(db, fixedEmbedder{})
	r.SeedFiles = 5

	got, err := r.SeedHits(context.Background(), Query{
		Texts:    []string{"wie wird die Anzahl Fahrzeuge weitergegeben"},
		Question: "wie wird die Anzahl Fahrzeuge weitergegeben",
	})
	if err != nil {
		t.Fatalf("SeedHits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("seeds = %v, want nothing when the accessor is over the ceiling", hitPaths(got))
	}
}

func TestSeedHits_refusesAShortAccessor(t *testing.T) {
	// A short term is not specific however few files hold it: "getId" in a
	// small corpus is an accident, not a claim.
	db := testDB(t)
	addRepo(t, db, "estate", "master")
	addChunk(t, db, "estate", "a/One.java", "one", "x.getId();", nearVec)

	r := New(db, fixedEmbedder{})
	r.SeedFiles = 5

	got, err := r.SeedHits(context.Background(), Query{
		Texts:    []string{"where is the id read"},
		Question: "where is the id read",
	})
	if err != nil {
		t.Fatalf("SeedHits: %v", err)
	}
	for _, h := range got {
		if strings.Contains(h.Path, "One.java") {
			t.Errorf("seeds = %v, want a term under %d runes refused", hitPaths(got), minSeedRunes)
		}
	}
}

func hasChunk(hits []Hit, id int64) bool {
	for _, h := range hits {
		if h.ChunkID == id {
			return true
		}
	}
	return false
}

func hitPaths(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}
