package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// The seed is worth nothing unless it reaches the gatherer. These pin the
// wiring: the pipeline asks for seeds on the path the motivating question
// took, and a seeded chunk becomes a source the answer can cite.

func TestPipeline_asksForSeedsOnTheUnscopedPath(t *testing.T) {
	// The path the motivating question took: no repository named, the whole
	// corpus searched. A seed not asked for here is not asked for on the
	// question that motivated it.
	db := gatherDB(t)
	search := &fakeSearch{}
	c := twoStepUpstream(t, appleTVReply, "Nothing found.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	_, _, _ = p.Run(context.Background(), "Wie wird die Anzahl Kinder an Syrius übermittelt?", AudienceBA, LanguageEN, Thread{}, Events{})

	if len(search.seedQueries) == 0 {
		t.Fatalf("the pipeline never asked for seeds on the unscoped path")
	}
	if q := search.seedQueries[0]; len(q.Texts) == 0 && q.Question == "" {
		t.Errorf("seed query = %+v, want the question's texts so terms can be derived", q)
	}
}

func TestPipeline_aSeededChunkBecomesASource(t *testing.T) {
	// The whole point: a chunk no lane ranked highly enough still reaches the
	// answer, because a seed is taken at hop 0 the way a hit is.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "entity/VersichertePerson.java", 0, 1, 20, "VersichertePerson",
		"class VersichertePerson { Integer getAnzahlKinder() { return anzahlKinder; } }")
	seedID := seedChunk(t, db, "syrius/ConverterEreignisregistrierung.java", 0, 1, 20, "toType",
		"ws.setAnzahlkinder(versicherter.getAnzahlKinder());")
	search := &fakeSearch{
		hits:  []retrieve.Hit{hitFor(t, db, hitID)},
		seeds: []retrieve.Hit{hitFor(t, db, seedID)},
	}
	c := twoStepUpstream(t, appleTVReply, "The value is mapped [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "Wie wird die Anzahl Kinder an Syrius übermittelt?", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sourcePresent(got.Sources, "ConverterEreignisregistrierung.java") {
		t.Fatalf("sources = %v, want the seeded chunk among them", sourcePaths(got.Sources))
	}
}

func TestPipeline_aSeedAlreadyAmongTheHitsIsNotDoubled(t *testing.T) {
	// A chunk both paths found is ONE source. Two would spend the budget twice
	// and cite the same lines under two numbers.
	db := gatherDB(t)
	id := seedChunk(t, db, "syrius/Converter.java", 0, 1, 20, "toType",
		"ws.setAnzahlkinder(v.getAnzahlKinder());")
	shared := hitFor(t, db, id)
	search := &fakeSearch{hits: []retrieve.Hit{shared}, seeds: []retrieve.Hit{shared}}
	c := twoStepUpstream(t, appleTVReply, "The value is mapped [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "Wie wird die Anzahl Kinder übermittelt?", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	n := 0
	for _, s := range got.Sources {
		if strings.Contains(s.Path, "syrius/Converter.java") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the shared chunk appears %d times in %v, want once", n, sourcePaths(got.Sources))
	}
}

func TestPipeline_aSeedAloneNeverTurnsNothingFoundIntoACard(t *testing.T) {
	// The seed has no score, because the scan that found it has no ranking.
	// Handed to the router it would be the whole field on a turn the search
	// came back empty from — and the floor that keeps a zero-score candidate
	// off a card cannot apply when zero IS the lead. "Nothing found" is a true
	// answer; a card, a judge call or "narrow your question" is not.
	db := gatherDB(t)
	seedID := seedChunk(t, db, "syrius/Converter.java", 0, 1, 20, "toType",
		"ws.setAnzahlkinder(v.getAnzahlKinder());")
	search := &fakeSearch{seeds: []retrieve.Hit{hitFor(t, db, seedID)}}
	// A router that cards on anything it is given, so the assertion is about
	// what reaches it rather than about the router's own thresholds.
	router := &fakeRouter{d: Decision{Ask: true, Candidates: []Candidate{
		{Repo: "estate", ModuleKey: "m0", Title: "One"},
		{Repo: "other", ModuleKey: "m1", Title: "Two"},
	}}}
	c := twoStepUpstream(t, appleTVReply, "Nothing found.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), router)

	_, clar, err := p.Run(context.Background(), "Wie wird die Anzahl Kinder übermittelt?",
		AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(router.gotHits) != 0 {
		t.Errorf("the router was handed %d hits, want the seeds kept out of routing",
			len(router.gotHits))
	}
	_ = clar
}

func TestPipeline_aCardCarriesTheSeedIntoTheChosenCandidate(t *testing.T) {
	// A turn that ends in a card resumes from what the card STORED — Resume
	// replays the candidate's hits and searches for nothing more. So a seed
	// computed after the card would be gone by the time the reader chooses,
	// and the answer would be built without the chunk the seed exists to
	// deliver: this rung's own failure, reached through a card.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "entity/VersichertePerson.java", 0, 1, 20, "VersichertePerson",
		"class VersichertePerson { Integer getAnzahlKinder() { return anzahlKinder; } }")
	seedID := seedChunk(t, db, "syrius/Converter.java", 0, 1, 20, "toType",
		"ws.setAnzahlkinder(v.getAnzahlKinder());")
	hit, seed := hitFor(t, db, hitID), hitFor(t, db, seedID)
	search := &fakeSearch{hits: []retrieve.Hit{hit}, seeds: []retrieve.Hit{seed}}
	router := &fakeRouter{d: Decision{Ask: true, Candidates: []Candidate{
		{Repo: hit.Repo, ModuleKey: "entity", Title: "Entity", Hits: []retrieve.Hit{hit}},
	}}}
	c := twoStepUpstream(t, appleTVReply, "Answer [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), router)

	_, clar, err := p.Run(context.Background(), "Wie wird die Anzahl Kinder übermittelt?",
		AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if clar == nil {
		t.Fatal("want a clarification")
	}
	found := false
	for _, h := range clar.Candidates[0].Hits {
		if h.ChunkID == seed.ChunkID {
			found = true
		}
	}
	if !found {
		t.Errorf("the card's candidate holds %d hits without the seed, so choosing it loses the converter",
			len(clar.Candidates[0].Hits))
	}
}

func sourcePresent(sources []Source, path string) bool {
	for _, s := range sources {
		if strings.Contains(s.Path, path) {
			return true
		}
	}
	return false
}

func sourcePaths(sources []Source) []string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = s.Path
	}
	return out
}
