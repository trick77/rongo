package retrieve

import (
	"fmt"
	"strings"
	"testing"
)

// The line that motivated the rung. The identifier "anzahlfahrzeuge" occurs in it
// only INSIDE getAnzahlFahrzeuge and setAnzahlfahrzeuge, never as a word of its own,
// so FTS5 — which tokenizes each of those whole — cannot reach it.
const converterLine = `    // Element Anzahl Fahrzeuge ist in Kernsystem optional, z.B. beim Kleinvorgang kennen wir die Anzahl nicht
    Optional.ofNullable(vertragsnehmer.getAnzahlFahrzeuge()).ifPresent(wsVertragsnehmer::setAnzahlfahrzeuge);`

// converterCodeOnly is the same mapping without its German comment. The comment
// spells "Anzahl" and "Fahrzeuge" as separate words, so a prose rung can reach the
// real chunk THROUGH THE COMMENT rather than through the mapping — which is
// luck, not retrieval: the same converter with an English comment, or none, is
// invisible to every FTS rung. Code is truth, so the rung is measured against
// the code.
const converterCodeOnly = `    Optional.ofNullable(vertragsnehmer.getAnzahlFahrzeuge()).ifPresent(wsVertragsnehmer::setAnzahlfahrzeuge);`

func TestBuildSubstringTerms_pairsAdjacentContentWords(t *testing.T) {
	// Given: the question that failed, in the reader's own German. "Anzahl"
	// and "Fahrzeuge" are adjacent content words; the code writes them as one
	// identifier, in that order.
	got := BuildSubstringTerms("wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben", nil)

	if !contains(got, "anzahlfahrzeuge") {
		t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, "anzahlfahrzeuge")
	}
}

func TestBuildSubstringTerms_takesCodeTermsAsGiven(t *testing.T) {
	// A code term is already identifier-shaped: it is folded and stripped of
	// punctuation, never split into pairs.
	got := BuildSubstringTerms("how is the value mapped", []string{"WsVertragsnehmerType", "setAnzahlfahrzeuge"})

	for _, want := range []string{"wsvertragsnehmertype", "setanzahlfahrzeuge"} {
		if !contains(got, want) {
			t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, want)
		}
	}
}

func TestBuildSubstringTerms_reachesAcrossAWordStopwordsDoNotCover(t *testing.T) {
	// `stopwords` is English by construction, and rongo answers German
	// questions. "Anzahl der Fahrzeuge" keeps its article as an ordinary content
	// word, so only the one-apart sweep recovers the identifier the code
	// actually writes.
	got := BuildSubstringTerms("how is the Anzahl der Fahrzeuge sent", nil)

	if !contains(got, "anzahlfahrzeuge") {
		t.Errorf("BuildSubstringTerms = %v, want %q across the uncovered article", got, "anzahlfahrzeuge")
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

func TestBuildSubstringTerms_takesASingleContentWord(t *testing.T) {
	// A question that IS one identifier — the shape the corpus's
	// identifier-kind questions use — has no pair to form. Without the word
	// itself as a candidate the rung never runs on exactly the questions
	// written to measure it.
	got := BuildSubstringTerms("CanChoose", nil)

	if !contains(got, "canchoose") {
		t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, "canchoose")
	}
}

func TestBuildSubstringTerms_keepsAProsePairWhenCodeTermsFillTheCap(t *testing.T) {
	// The question that motivated the rung, with the terms the understanding
	// step actually guessed for it. Those guesses are the failure: none of
	// them is the identifier, and if they spend the whole budget the pair
	// that IS the identifier never reaches the lane.
	got := BuildSubstringTerms(
		"Im Policenantrag Backend, wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben",
		[]string{"Policenantrag", "Backend", "Kernsystem", "FahrzeugAnzahl", "Weitergabe", "API", "Datenweitergabe"})

	if !contains(got, "anzahlfahrzeuge") {
		t.Errorf("BuildSubstringTerms = %v, want %q: the guessed code terms must not starve the prose pairs", got, "anzahlfahrzeuge")
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
	q := "wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"
	a := BuildSubstringTerms(q, []string{"WsVertragsnehmerType"})
	b := BuildSubstringTerms(q, []string{"WsVertragsnehmerType"})

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
	got := BuildSubstringTerms("anzahl fahrzeuge anzahl fahrzeuge", []string{"anzahlFahrzeuge"})

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
	addRepo(t, db, "policenantrag", "master")
	addChunk(t, db, "policenantrag", "src/ConverterVorgangsregistrierung.java", "toVertragsnehmerType",
		converterLine, farVec)

	s := NewStore(db)
	ctx := t.Context()

	// The keyword lane, given the exact identifier: nothing. getAnzahlFahrzeuge
	// and setAnzahlfahrzeuge are each ONE token to unicode61.
	fts, err := s.SearchKeywordIn(ctx, BuildFTSMatch("anzahlfahrzeuge"), 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchKeywordIn: %v", err)
	}
	if len(fts) != 0 {
		t.Fatalf("SearchKeywordIn found %d hits for the bare identifier, want 0 — "+
			"if this now passes, FTS tokenization changed and the rung's reason needs re-reading", len(fts))
	}

	// The same term as a substring: the chunk.
	sub, err := s.SearchSubstringIn(ctx, "anzahlfahrzeuge", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(sub) != 1 {
		t.Fatalf("SearchSubstringIn found %d hits, want 1", len(sub))
	}
	if !strings.Contains(sub[0].RawText, "setAnzahlfahrzeuge") {
		t.Errorf("SearchSubstringIn returned the wrong chunk: %q", sub[0].RawText)
	}
}

func TestSearchSubstringIn_foldsCase(t *testing.T) {
	// The corpus writes setAnzahlfahrzeuge; the reader may type any casing.
	db := testDB(t)
	addRepo(t, db, "policenantrag", "master")
	addChunk(t, db, "policenantrag", "src/C.java", "sym", converterLine, farVec)

	s := NewStore(db)
	for _, term := range []string{"anzahlfahrzeuge", "AnzahlFahrzeuge", "ANZAHLFAHRZEUGE"} {
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
	hits, err := s.SearchSubstringIn(t.Context(), "anzahlfahrzeuge", 10, []string{"kept"}, nil)
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
	hits, err := s.SearchSubstringIn(t.Context(), "anzahlfahrzeuge", 10, nil, nil)
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

func TestSearchSubstringIn_putsCodeAheadOfDocsAndTests(t *testing.T) {
	// Address order alone ranks a repository's own layout, not its relevance.
	// In the corpus that motivated the rung the converter sat at position 35
	// of a 40-row limit, behind 35 chunks of .puml entity diagrams and
	// persistence fixtures that merely NAME the field — five slots from being
	// cut by a limit it never chose.
	//
	// A scan has no bm25 to lean on, so the one ordering it can justify is
	// the kind of file: code, then tests, then documentation, and address
	// within each. Code is truth; a diagram naming the field is context.
	db := testDB(t)
	addRepo(t, db, "r", "master")
	addChunk(t, db, "r", "aaa/doc/Diagramm-Entities.puml", "sym", converterCodeOnly, farVec)
	addChunk(t, db, "r", "bbb/src/test/java/ConverterTest.java", "sym", converterCodeOnly, farVec)
	addChunk(t, db, "r", "zzz/src/main/java/Converter.java", "sym", converterCodeOnly, farVec)

	hits, err := NewStore(db).SearchSubstringIn(t.Context(), "anzahlfahrzeuge", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("found %d hits, want 3", len(hits))
	}
	if hits[0].Path != "zzz/src/main/java/Converter.java" {
		t.Errorf("first hit is %q, want the code file despite its address sorting last", hits[0].Path)
	}
	if hits[2].Path != "aaa/doc/Diagramm-Entities.puml" {
		t.Errorf("last hit is %q, want the document despite its address sorting first", hits[2].Path)
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

func TestSearch_severalTermsHittingOneChunkScoreItOnce(t *testing.T) {
	// get<X>, set<X> and <X> routinely all match the same chunk — it is what
	// code terms plus word pairing PRODUCE, not an edge case. As one lane per
	// term that chunk would be scored three times at SubstringWeight each, an
	// effective 2.55: above WeightKeywordStrict, from a rung deliberately
	// below it. Worse, the fused hit would report one lane while carrying the
	// score of three, so the trace and any lane-based measurement would
	// misattribute the movement.
	db := testDB(t)
	addRepo(t, db, "r", "master")
	addChunk(t, db, "r", "src/Converter.java", "sym", converterCodeOnly, farVec)

	// The question names both accessors, so several generated terms reach the
	// one chunk. One text and no Code: the substring rung is then the ONLY
	// lane that can reach it — the vector is far, and no FTS rung sees a word
	// that is only ever part of a longer token — so the fused score is the
	// rung's own contribution and nothing else's.
	question := "wie wird getAnzahlFahrzeuge und setAnzahlfahrzeuge an Kernsystem weitergegeben"
	q := Query{Texts: []string{question}, Question: question, K: 5}

	// Several of the generated terms match this one chunk — the overlap the
	// test exists to price.
	s := NewStore(db)
	matching := 0
	for _, term := range BuildSubstringTerms(question, nil) {
		hits, err := s.SearchSubstringIn(t.Context(), term, 40, nil, nil)
		if err != nil {
			t.Fatalf("SearchSubstringIn(%q): %v", term, err)
		}
		if len(hits) > 0 {
			matching++
		}
	}
	if matching < 2 {
		t.Fatalf("only %d terms matched the chunk; the test no longer exercises the overlap", matching)
	}

	// The invariant, priced directly: the rung's contribution is what the
	// chunk's score LOSES when the rung is switched off. Comparing the two
	// arms isolates it from the other lanes, which this question does reach —
	// it spells the accessors out, so the keyword rungs see them as tokens.
	//
	// Hit.Lanes cannot answer this: it dedups the NAME, so one lane and three
	// lanes both report "keyword:substring" once. That mismatch between the
	// reported lane and the carried score is half of why the terms are fused
	// into one lane in the first place.
	on, err := New(db, fixedEmbedder{vec: queryVec}).Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search (rung on): %v", err)
	}
	if len(on) != 1 {
		t.Fatalf("Search found %d hits, want 1", len(on))
	}
	off, err := (&Retriever{store: NewStore(db), embedder: fixedEmbedder{vec: queryVec},
		MaxDistance: DefaultMaxDistance, Candidates: defaultCandidates}).Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search (rung off): %v", err)
	}
	if len(off) != 1 {
		t.Fatalf("Search with the rung off found %d hits, want 1", len(off))
	}

	// Rank 0 in the substring lane — it is the only chunk there — so one
	// lane's contribution is weight/(rrfK+0).
	got := on[0].Score - off[0].Score
	want := WeightKeywordSubstring / float64(rrfK)
	if got > want*1.01 {
		t.Errorf("the substring rung added %.6f to the score, want one lane's %.6f — "+
			"%d matching terms are being fused as separate lanes", got, want, matching)
	}
}

func TestSearch_aWideTermDoesNotEvictANarrowOnesHits(t *testing.T) {
	// Lane rank is what fusion weighs. Taking each term's hits in turn and
	// cutting at `candidates` gives the FIRST term every slot near the top —
	// and the first terms are the guessed code terms, which are by
	// construction the guesses that MISSED when this rung is needed at all.
	//
	// Measured on the motivating question before the fix: the guessed term
	// "policenantrag" returned 40 hits on its own, the whole lane, while
	// staying under the hub share. "anzahlfahrzeuge" and its single chunk — the
	// mapping the rung exists to recover — were cut before fusion saw them.
	db := testDB(t)
	addRepo(t, db, "policenantrag", "master")
	for i := range 200 {
		addChunkAt(t, db, "policenantrag", fmt.Sprintf("lib/persistence/F%04d.java", i), 0, 1, 2,
			"sym", "package ch.example.policenantrag; // persistence entity filler", farVec)
	}
	// The mapping, on a path that sorts LAST so nothing but the term's own
	// narrowness can carry it.
	addChunkAt(t, db, "policenantrag", "zzz/Converter.java", 0, 1, 2, "sym", converterCodeOnly, farVec)

	question := "Im Policenantrag Backend, wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"
	code := "Policenantrag Datenweitergabe"
	terms := BuildSubstringTerms(question, strings.Fields(code))

	// The premise: one early, wide term fills the lane by itself.
	s := NewStore(db)
	wide, err := s.SearchSubstringIn(t.Context(), terms[0], defaultCandidates, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn(%q): %v", terms[0], err)
	}
	if len(wide) < defaultCandidates {
		t.Fatalf("the first term returned %d hits, want it to saturate %d — the test no longer "+
			"exercises eviction", len(wide), defaultCandidates)
	}

	hits, err := New(db, fixedEmbedder{vec: queryVec}).Search(t.Context(),
		Query{Texts: []string{question, code}, Question: question, Code: code, K: 60})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits {
		if h.Path == "zzz/Converter.java" {
			return
		}
	}
	t.Errorf("the converter is absent from %d fused hits: a wide early term evicted the narrow "+
		"term's only chunk", len(hits))
}

func TestBuildSubstringTerms_keepsASeparatorCodeTermInItsOwnSpelling(t *testing.T) {
	// The haystack is raw source, so folding a guess that was RIGHT destroys
	// it: set_anzahl_fahrzeuge becomes setanzahlfahrzeuge, which cannot occur in a
	// source that writes the underscores. Without both spellings the rung is
	// dead exactly when the model guessed correctly in a separator language.
	for _, c := range []string{"set_anzahl_fahrzeuge", "max-retry-count"} {
		got := BuildSubstringTerms("how is it set", []string{c})
		if !contains(got, strings.ToLower(c)) {
			t.Errorf("BuildSubstringTerms(%q) = %v, want the separator spelling kept", c, got)
		}
		if !contains(got, fold(c)) {
			t.Errorf("BuildSubstringTerms(%q) = %v, want the folded spelling too", c, got)
		}
	}
}

func TestSearchSubstringIn_weighsTheHubShareAgainstTheScopedCorpus(t *testing.T) {
	// The share must be computed over the population the TURN can see. With
	// the denominator taken from the whole chunks table, one large parked or
	// out-of-scope repository permanently disarms the guard for every live
	// one — the same mistake the vec lane's rowid subquery rule exists to
	// stop.
	db := testDB(t)
	addRepo(t, db, "kept", "master")
	addRepo(t, db, "big", "master")
	for i := range substringHubFloor + 10 {
		body := "package main // filler"
		if i%2 == 0 {
			body = "package main // licenceheader boilerplate"
		}
		addChunkAt(t, db, "kept", fmt.Sprintf("k%04d.go", i), 0, 1, 2, "sym", body, farVec)
	}
	// A large repository OUT of scope, holding none of the term.
	for i := range 5000 {
		addChunkAt(t, db, "big", fmt.Sprintf("b%04d.go", i), 0, 1, 2, "sym", "unrelated", farVec)
	}

	hits, err := NewStore(db).SearchSubstringIn(t.Context(), "licenceheader", 40, []string{"kept"}, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("a term in half the SCOPED corpus returned %d hits; the denominator is not being filtered", len(hits))
	}
}

func TestSearchSubstringIn_reachesASnakeCaseSpelling(t *testing.T) {
	// The haystack is raw source: a corpus writing set_anzahl_fahrzeuge contains
	// no run of letters spelling "anzahlfahrzeuge", so no case variant of the
	// glued needle can find it. Without the separator spelling the rung is
	// silently dead over Python, Rust, C and Ruby.
	db := testDB(t)
	addRepo(t, db, "r", "master")
	addChunk(t, db, "r", "a.py", "sym", "ws.set_anzahl_fahrzeuge(v)", farVec)

	s := NewStore(db)
	if hits, _ := s.SearchSubstringIn(t.Context(), "anzahlfahrzeuge", 10, nil, nil); len(hits) != 0 {
		t.Errorf("the glued needle found %d hits in snake_case source; the test's premise is stale", len(hits))
	}
	hits, err := s.SearchSubstringIn(t.Context(), "anzahl_fahrzeuge", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringIn: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("the snake_case needle found %d hits, want 1", len(hits))
	}

	// And the builder emits that spelling for a question written in prose.
	if got := BuildSubstringTerms("how is anzahl fahrzeuge set", nil); !contains(got, "anzahl_fahrzeuge") {
		t.Errorf("BuildSubstringTerms = %v, want it to contain %q", got, "anzahl_fahrzeuge")
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
	addRepo(t, db, "policenantrag", "master")
	// The CODE line alone, without the German comment that happens to sit
	// above it in the real file. That comment spells "Anzahl" and "Fahrzeuge" as
	// separate words, so a prose rung can reach the real chunk through it —
	// by luck of a comment, not through the mapping. Code is truth: a
	// converter whose comment is in English, or absent, is the ordinary case,
	// and this fixture is that case.
	addChunk(t, db, "policenantrag", "src/Converter.java", "toVertragsnehmerType", converterCodeOnly, farVec)

	q := Query{
		Texts:    []string{"wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"},
		Question: "wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben",
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
