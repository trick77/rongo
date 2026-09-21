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
