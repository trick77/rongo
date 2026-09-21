package retrieve

import (
	"fmt"
	"strings"
	"testing"
)

// The line that motivated the rung. The identifier "anzahlkinder" occurs in it
// only INSIDE getAnzahlKinder and setAnzahlkinder, never as a word of its own,
// so FTS5 — which tokenizes each of those whole — cannot reach it.
const converterLine = `    // Element Anzahl Kinder ist in Syrius optional, z.B. beim Bagatellfall kennen wir die Anzahl nicht
    Optional.ofNullable(versicherter.getAnzahlKinder()).ifPresent(wsVersicherter::setAnzahlkinder);`

// converterCodeOnly is the same mapping without its German comment. The comment
// spells "Anzahl" and "Kinder" as separate words, so a prose rung can reach the
// real chunk THROUGH THE COMMENT rather than through the mapping — which is
// luck, not retrieval: the same converter with an English comment, or none, is
// invisible to every FTS rung. Code is truth, so the rung is measured against
// the code.
const converterCodeOnly = `    Optional.ofNullable(versicherter.getAnzahlKinder()).ifPresent(wsVersicherter::setAnzahlkinder);`

func TestBuildSubstringTerms_pairsAdjacentContentWords(t *testing.T) {
	// Given: the question that failed, in the reader's own German. "Anzahl"
	// and "Kinder" are adjacent content words; the code writes them as one
	// identifier, in that order.
	got := BuildSubstringTerms("wie wird die Anzahl Kinder an Syrius uebermittelt", nil)

	if !contains(got, "anzahlkinder") {
		t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, "anzahlkinder")
	}
}

func TestBuildSubstringTerms_takesCodeTermsAsGiven(t *testing.T) {
	// A code term is already identifier-shaped: it is folded and stripped of
	// punctuation, never split into pairs.
	got := BuildSubstringTerms("how is the value mapped", []string{"WsVersicherterType", "setAnzahlkinder"})

	for _, want := range []string{"wsversichertertype", "setanzahlkinder"} {
		if !contains(got, want) {
			t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, want)
		}
	}
}

func TestBuildSubstringTerms_reachesAcrossAWordStopwordsDoNotCover(t *testing.T) {
	// `stopwords` is English by construction, and rongo answers German
	// questions. "Anzahl der Kinder" keeps its article as an ordinary content
	// word, so only the one-apart sweep recovers the identifier the code
	// actually writes.
	got := BuildSubstringTerms("how is the Anzahl der Kinder sent", nil)

	if !contains(got, "anzahlkinder") {
		t.Errorf("BuildSubstringTerms = %v, want %q across the uncovered article", got, "anzahlkinder")
	}
	for _, g := range got {
		if len([]rune(g)) < minSubstringRunes {
			t.Errorf("BuildSubstringTerms returned %q, shorter than the %d-rune floor", g, minSubstringRunes)
		}
	}
}

func TestBuildSubstringTerms_dropsEnglishFunctionWordsBeforePairing(t *testing.T) {
	// Where the list DOES cover the word, it is gone before pairing: the pair
	// is of the nouns, not of a noun and "the".
	got := BuildSubstringTerms("how is the retention window applied", nil)

	if !contains(got, "retentionwindow") {
		t.Errorf("BuildSubstringTerms = %v, want %q", got, "retentionwindow")
	}
	for _, g := range got {
		if strings.Contains(g, "the") && g != "retentionwindow" {
			t.Errorf("BuildSubstringTerms = %v, paired across a dropped function word in %q", got, g)
		}
	}
}

func TestBuildSubstringTerms_capsTheCandidates(t *testing.T) {
	// A pasted paragraph must not become forty scans.
	long := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 6)
	if got := BuildSubstringTerms(long, nil); len(got) > maxSubstringTerms {
		t.Errorf("BuildSubstringTerms returned %d terms, want at most %d", len(got), maxSubstringTerms)
	}
}

func TestBuildSubstringTerms_isDeterministic(t *testing.T) {
	// Two calls on one question must agree: the lane's order feeds fusion,
	// and a set iteration would reshuffle the arm between runs.
	q := "wie wird die Anzahl Kinder an Syrius uebermittelt"
	a := BuildSubstringTerms(q, []string{"WsVersicherterType"})
	b := BuildSubstringTerms(q, []string{"WsVersicherterType"})

	if len(a) != len(b) {
		t.Fatalf("lengths differ across calls: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("order differs across calls at %d: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestBuildSubstringTerms_noDuplicates(t *testing.T) {
	// The same term reaching the lane twice would fuse the same rows in
	// twice, double-counting them against the other lanes — the reason
	// BuildFTSQueries drops its redundant rungs.
	got := BuildSubstringTerms("anzahl kinder anzahl kinder", []string{"anzahlKinder"})

	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Errorf("BuildSubstringTerms = %v, contains %q twice", got, g)
		}
		seen[g] = true
	}
}

func TestBuildSubstringTerms_emptyWhenNothingUsable(t *testing.T) {
	// Every word a function word, or all too short: no lane rather than a
	// scan of the whole corpus for "the".
	if got := BuildSubstringTerms("how does it do this", nil); len(got) != 0 {
		t.Errorf("BuildSubstringTerms = %v, want none", got)
	}
}

// TestSubstringFindsWhatFTSCannot is the measurement the rung exists for,
// asserted rather than described: over the real converter line, the FTS lane
// returns nothing for the bare identifier and the substring lane returns the
// chunk.
func TestSubstringFindsWhatFTSCannot(t *testing.T) {
	db := testDB(t)
	addRepo(t, db, "schadenmeldung", "master")
	addChunk(t, db, "schadenmeldung", "src/ConverterEreignisregistrierung.java", "toVersicherterType",
		converterLine, farVec)

	s := NewStore(db)
	ctx := t.Context()

	// The keyword lane, given the exact identifier: nothing. getAnzahlKinder
	// and setAnzahlkinder are each ONE token to unicode61.
	fts, err := s.SearchKeywordIn(ctx, BuildFTSMatch("anzahlkinder"), 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchKeywordIn: %v", err)
	}
	if len(fts) != 0 {
		t.Fatalf("SearchKeywordIn found %d hits for the bare identifier, want 0 — "+
			"if this now passes, FTS tokenization changed and the rung's reason needs re-reading", len(fts))
	}

	// The same term as a substring: the chunk.
	sub, err := s.SearchSubstringIn(ctx, "anzahlkinder", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(sub) != 1 {
		t.Fatalf("SearchSubstringIn found %d hits, want 1", len(sub))
	}
	if !strings.Contains(sub[0].RawText, "setAnzahlkinder") {
		t.Errorf("SearchSubstringIn returned the wrong chunk: %q", sub[0].RawText)
	}
}

func TestSearchSubstringIn_foldsCase(t *testing.T) {
	// The corpus writes setAnzahlkinder; the reader may type any casing.
	db := testDB(t)
	addRepo(t, db, "schadenmeldung", "master")
	addChunk(t, db, "schadenmeldung", "src/C.java", "sym", converterLine, farVec)

	s := NewStore(db)
	for _, term := range []string{"anzahlkinder", "AnzahlKinder", "ANZAHLKINDER"} {
		hits, err := s.SearchSubstringIn(t.Context(), term, 10, nil, nil)
		if err != nil {
			t.Fatalf("SearchSubstringIn(%q): %v", term, err)
		}
		if len(hits) != 1 {
			t.Errorf("SearchSubstringIn(%q) found %d hits, want 1", term, len(hits))
		}
	}
}

func TestSearchSubstringIn_honoursTheRepoFilter(t *testing.T) {
	// Parked repositories and a narrowed scope must filter here exactly as
	// they do in the keyword lane, or the rung answers from a repo the turn
	// excluded.
	db := testDB(t)
	addRepo(t, db, "kept", "master")
	addRepo(t, db, "parked", "master")
	addChunk(t, db, "kept", "a.java", "sym", converterLine, farVec)
	addChunk(t, db, "parked", "b.java", "sym", converterLine, farVec)

	s := NewStore(db)
	hits, err := s.SearchSubstringIn(t.Context(), "anzahlkinder", 10, []string{"kept"}, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("found %d hits, want 1", len(hits))
	}
	if hits[0].Repo != "kept" {
		t.Errorf("hit came from %q, want the only repo in scope", hits[0].Repo)
	}
}

func TestSearchSubstringIn_emptyTermIsNoLane(t *testing.T) {
	db := testDB(t)
	addRepo(t, db, "r", "master")
	addChunk(t, db, "r", "a.java", "sym", converterLine, farVec)

	hits, err := NewStore(db).SearchSubstringIn(t.Context(), "   ", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("a blank term returned %d hits, want none", len(hits))
	}
}

func TestSearchSubstringIn_ordersDeterministically(t *testing.T) {
	// No bm25 here: a scan has no ranking of its own. Address order is what
	// keeps two runs over one database identical, so a one-part move in a
	// measurement is real rather than row order.
	db := testDB(t)
	addRepo(t, db, "r", "master")
	addChunk(t, db, "r", "b/second.java", "sym", converterLine, farVec)
	addChunk(t, db, "r", "a/first.java", "sym", converterLine, farVec)

	s := NewStore(db)
	hits, err := s.SearchSubstringIn(t.Context(), "anzahlkinder", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("found %d hits, want 2", len(hits))
	}
	if hits[0].Path != "a/first.java" {
		t.Errorf("first hit is %q, want the lowest address", hits[0].Path)
	}
}

func TestSearchSubstringIn_skipsAHubTerm(t *testing.T) {
	// A term inside a large share of the corpus is not evidence. The guard
	// only applies past substringHubFloor: a share over a handful of chunks
	// says nothing, and the one chunk that legitimately holds an identifier
	// IS a large share of a fixture.
	db := testDB(t)
	addRepo(t, db, "r", "master")
	for i := range substringHubFloor + 10 {
		body := "package main // filler"
		if i%2 == 0 {
			body = "package main // licenceheader boilerplate"
		}
		addChunkAt(t, db, "r", fmt.Sprintf("f%04d.go", i), 0, 1, 2, "sym", body, farVec)
	}

	s := NewStore(db)
	// Half the corpus: skipped, and reported as no lane rather than as an error.
	hits, err := s.SearchSubstringIn(t.Context(), "licenceheader", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("a term in half the corpus returned %d hits, want it skipped as a hub", len(hits))
	}
}

func TestNew_shipsTheSubstringRungOn(t *testing.T) {
	// Same shape as the code rung: the product has it, a struct-literal
	// Retriever does not, so the harness's baseline arm is a field left alone
	// rather than a second constructor.
	if got := New(testDB(t), fixedEmbedder{vec: queryVec}).SubstringWeight; got != WeightKeywordSubstring {
		t.Errorf("New().SubstringWeight = %v, want %v", got, WeightKeywordSubstring)
	}
	if got := (&Retriever{}).SubstringWeight; got != 0 {
		t.Errorf("struct-literal SubstringWeight = %v, want the rung off", got)
	}
}

func TestSearch_theSubstringRungReachesInsideAToken(t *testing.T) {
	// The end-to-end claim, through Search rather than the store: a question
	// whose identifier exists only inside a larger token reaches the chunk
	// with the rung on, and does not without it.
	db := testDB(t)
	addRepo(t, db, "schadenmeldung", "master")
	// The CODE line alone, without the German comment that happens to sit
	// above it in the real file. That comment spells "Anzahl" and "Kinder" as
	// separate words, so a prose rung can reach the real chunk through it —
	// by luck of a comment, not through the mapping. Code is truth: a
	// converter whose comment is in English, or absent, is the ordinary case,
	// and this fixture is that case.
	addChunk(t, db, "schadenmeldung", "src/Converter.java", "toVersicherterType", converterCodeOnly, farVec)

	q := Query{
		Texts:    []string{"wie wird die Anzahl Kinder an Syrius uebermittelt"},
		Question: "wie wird die Anzahl Kinder an Syrius uebermittelt",
		K:        5,
	}

	// Off: the chunk is unreachable. Its vector is far, and no FTS rung can
	// see a word that is only ever part of a longer token.
	off := &Retriever{store: NewStore(db), embedder: fixedEmbedder{vec: queryVec},
		MaxDistance: DefaultMaxDistance, Candidates: defaultCandidates}
	hits, err := off.Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search (rung off): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("with the rung off Search found %d hits, want 0", len(hits))
	}

	// On: it is found, and the hit says which rung reached it.
	on := New(db, fixedEmbedder{vec: queryVec})
	hits, err = on.Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search (rung on): %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("with the rung on Search found %d hits, want 1", len(hits))
	}
	if !contains(hits[0].Lanes, "keyword:substring") {
		t.Errorf("hit reports lanes %v, want the substring rung named among them", hits[0].Lanes)
	}
}
