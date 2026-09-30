package retrieve

import (
	"context"
	"testing"
)

// TestSearch_aQuestionThatNamesARepositorySearchesIt is the defect the card
// showed: "was schickt loom im header an das llm?" named loom, the index
// carries loom, and the turn searched the whole corpus anyway because the
// understanding step's guess came back without it. The restriction was a model
// guess and nothing else; now the question's own words are read too.
func TestSearch_aQuestionThatNamesARepositorySearchesIt(t *testing.T) {
	// Given: two repositories that both answer the question's words.
	db := testDB(t)
	addRepo(t, db, "rongo", "master")
	addRepo(t, db, "loom", "master")
	addChunk(t, db, "rongo", "a.go", "A", "sender dispatch", nearVec)
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When: the question names loom and the understanding step guessed nothing.
	hits, err := r.Search(context.Background(), Query{
		Text:     "sender",
		Question: "was schickt Loom im Header an das llm?",
		K:        5,
	})

	// Then: loom only.
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search() found nothing in loom")
	}
	for _, h := range hits {
		if h.Repo != "loom" {
			t.Errorf("hit from %s: the question named loom", h.Repo)
		}
	}
}

func TestSearch_theQuestionsNamesJoinTheGuessedOnes(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "rongo", "master")
	addRepo(t, db, "loom", "master")
	addRepo(t, db, "peeq", "master")
	addChunk(t, db, "rongo", "a.go", "A", "sender dispatch", nearVec)
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", nearVec)
	addChunk(t, db, "peeq", "c.go", "C", "sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When: the model named rongo, the question named loom.
	hits, err := r.Search(context.Background(), Query{
		Text:     "sender",
		Question: "does loom do this the way it is done here?",
		Repos:    []string{"rongo"},
		K:        5,
	})

	// Then: both, and nothing else. A union, because a guess that misses the
	// repository the reader typed must not be able to exclude it.
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Repo] = true
	}
	if !seen["loom"] || !seen["rongo"] {
		t.Errorf("want loom and rongo, got %v", seen)
	}
	if seen["peeq"] {
		t.Error("peeq was neither guessed nor named")
	}
}

func TestSearch_aRepositoryNameMustBeAWholeWord(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "loom", "master")
	addRepo(t, db, "rongo", "master")
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", nearVec)
	addChunk(t, db, "rongo", "a.go", "A", "sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When: the question contains loom only inside another word.
	hits, err := r.Search(context.Background(), Query{
		Text:     "sender",
		Question: "how do the looms and the heirlooms differ?",
		K:        5,
	})

	// Then: no restriction at all — a substring is not a mention, and reading
	// one as a mention would silence the rest of the corpus.
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Repo] = true
	}
	if !seen["rongo"] {
		t.Errorf("a substring narrowed the search to %v", seen)
	}
}

func TestSearch_aRepositoryNameGluedToAGermanWordIsNotAMention(t *testing.T) {
	// Given: the questions are German, and a boundary test that counts bytes
	// reads the leading byte of an umlaut as a word break.
	db := testDB(t)
	addRepo(t, db, "loom", "master")
	addRepo(t, db, "rongo", "master")
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", nearVec)
	addChunk(t, db, "rongo", "a.go", "A", "sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{
		Text:     "sender",
		Question: "gibt es hier etwas loomähnliches?",
		K:        5,
	})

	// Then: no restriction — "loomähnliches" is one word, not a mention.
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Repo] = true
	}
	if !seen["rongo"] {
		t.Errorf("an umlaut was read as a word boundary and narrowed the search to %v", seen)
	}
}

// TestSearch_aNameOtherRepositoriesUseAsAWordIsNotAMention is the eval miss:
// "In that flow, where is the shipping cost added?" narrowed the turn to a
// repository called shipping, and the 4.99 in orders was never searched. A
// fixed list of common words cannot know a corpus's domain words; the index
// can. A plain name other repositories use as a word is vocabulary, and only
// the understanding step's guess — which reads the sentence — narrows to it.
func TestSearch_aNameOtherRepositoriesUseAsAWordIsNotAMention(t *testing.T) {
	db := testDB(t)
	addRepo(t, db, "shipping", "master")
	addRepo(t, db, "orders", "master")
	addRepo(t, db, "queue-master", "master")
	addChunk(t, db, "shipping", "a.go", "A", "sender dispatch", nearVec)
	addChunk(t, db, "orders", "b.go", "B", "float shipping sender dispatch", nearVec)
	addChunk(t, db, "queue-master", "c.go", "C", "queue-master sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})
	repos := func(question string, guess ...string) map[string]bool {
		t.Helper()
		hits, err := r.Search(context.Background(), Query{Text: "sender", Question: question, Repos: guess, K: 5})
		if err != nil {
			t.Fatalf("Search() err = %v", err)
		}
		seen := map[string]bool{}
		for _, h := range hits {
			seen[h.Repo] = true
		}
		return seen
	}

	// No guess: "shipping" is a word orders uses, so nothing is narrowed.
	if seen := repos("In that flow, where is the shipping cost added?"); !seen["orders"] {
		t.Errorf("an ordinary word narrowed the search to %v", seen)
	}
	// The guess still narrows: the understanding step read the sentence.
	if seen := repos("how does shipping queue a shipment?", "shipping"); seen["orders"] || !seen["shipping"] {
		t.Errorf("the guess named shipping, got %v", seen)
	}
	// A name-shaped name is a name wherever else it appears.
	if seen := repos("what does queue-master do with it?"); seen["orders"] || !seen["queue-master"] {
		t.Errorf("queue-master was named, got %v", seen)
	}
	// And orders, whose name no other repository uses, is a mention.
	if seen := repos("how do orders get saved?"); seen["shipping"] || !seen["orders"] {
		t.Errorf("orders was named and nobody else says it, got %v", seen)
	}
}

// TestSearch_anOrdinaryWordIsNotAMention keeps a repository named after a
// common word from swallowing every question that contains it. A restriction
// is invisible from the outside: the turn would report "nothing found" plus
// its terms, which reads as a vocabulary miss rather than as a corpus nobody
// asked to narrow.
func TestSearch_anOrdinaryWordIsNotAMention(t *testing.T) {
	// Given: a repository actually called "search".
	db := testDB(t)
	addRepo(t, db, "search", "master")
	addRepo(t, db, "loom", "master")
	addChunk(t, db, "search", "a.go", "A", "sender dispatch", nearVec)
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{
		Text:     "sender",
		Question: "how does search work?",
		K:        5,
	})

	// Then: both repositories, because nothing was narrowed.
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Repo] = true
	}
	if !seen["loom"] {
		t.Errorf("a common word narrowed the search to %v", seen)
	}

	// And the model's guess still reaches it: only the reader's wording is
	// refused as evidence, not the repository itself.
	hits, err = r.Search(context.Background(), Query{
		Text: "sender", Question: "how does search work?", Repos: []string{"search"}, K: 5,
	})
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	for _, h := range hits {
		if h.Repo != "search" {
			t.Errorf("hit from %s: the guess named search", h.Repo)
		}
	}
}
