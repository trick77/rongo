package retrieve

import (
	"fmt"
	"reflect"
	"testing"
)

// equivalenceCorpus holds every case the rung decides on, sized so that the
// cases are actually reached: the identifiers sit in about 1.6% of the corpus
// — under the hub share, so they return hits — across code, tests,
// documentation and a stage directory, more of them than any cut below; one
// term is in half of it; one identifier is non-ASCII in both the spellings
// the rung tries; a second repository and a parked one hold the identifier
// too.
func equivalenceCorpus(t *testing.T) *Store {
	t.Helper()
	db := testDB(t)
	addRepo(t, db, "app", "master")
	addRepo(t, db, "lib", "main")
	addRepo(t, db, "parked", "master")
	for i := range 1500 {
		body := "package main // filler"
		switch {
		case i%2 == 0:
			body = "package main // licenceheader boilerplate"
		case i%100 == 7:
			body = "x.getAnzahlFahrzeuge(); y.setAnzahlfahrzeuge(1)"
		case i%100 == 13:
			body = "val größefahrzeug = fahrzeug.getGrößefahrzeug()"
		}
		// The kind changes every hundred chunks, so each identifier — one
		// chunk per hundred — lands in every kind.
		path := fmt.Sprintf("src/f%04d.go", i)
		switch (i / 100) % 4 {
		case 1:
			path = fmt.Sprintf("src/f%04d_test.go", i)
		case 2:
			path = fmt.Sprintf("docs/f%04d.md", i)
		case 3:
			path = fmt.Sprintf("intg/f%04d.yaml", i)
		}
		addChunkAt(t, db, "app", path, 0, 1, 2, "sym", body, farVec)
	}
	for i := range 10 {
		addChunkAt(t, db, "lib", fmt.Sprintf("l%02d.go", i), 0, 1, 2, "sym", "lib.AnzahlFahrzeuge + new Größefahrzeug()", farVec)
		addChunkAt(t, db, "parked", fmt.Sprintf("p%02d.go", i), 0, 1, 2, "sym", "parked.AnzahlFahrzeuge", farVec)
	}
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'parked'`); err != nil {
		t.Fatalf("park: %v", err)
	}
	return NewStore(db)
}

func TestSearchSubstringsIn_returnsPerTermWhatThePerTermRungDid(t *testing.T) {
	// Given
	s := equivalenceCorpus(t)
	ctx := t.Context()
	terms := []string{"anzahlfahrzeuge", "licenceheader", "größefahrzeug", "  ", "nowhere", "AnzahlFahrzeuge", "filler", "getanzahl"}

	scopes := []struct {
		name  string
		repos []string
		stage StagePrefixes
	}{
		{"whole corpus", nil, nil},
		{"one repository", []string{"lib"}, nil},
		{"two repositories", []string{"app", "lib"}, nil},
		{"a stage", nil, StagePrefixes{"app": "intg/"}},
		{"a repository and a stage", []string{"app"}, StagePrefixes{"app": "intg/"}},
	}
	for _, sc := range scopes {
		for _, n := range []int{0, 3, 40, 1000} {
			// When all terms are asked at once
			got, err := s.SearchSubstringsIn(ctx, terms, n, sc.repos, sc.stage)
			if err != nil {
				t.Fatalf("%s n=%d: SearchSubstringsIn: %v", sc.name, n, err)
			}

			// Then each term's list is the one it got asked alone
			for i, term := range terms {
				want, err := s.SearchSubstringIn(ctx, term, n, sc.repos, sc.stage)
				if err != nil {
					t.Fatalf("%s n=%d %q: reference: %v", sc.name, n, term, err)
				}
				if !reflect.DeepEqual(got[i], want) {
					t.Errorf("%s n=%d %q: %d hits, want %d — or the same hits in another order",
						sc.name, n, term, len(got[i]), len(want))
				}
			}
		}
	}
}

func TestSearchSubstringsIn_theComparisonIsOverListsThatHoldSomething(t *testing.T) {
	// The comparison above proves nothing over lists that are all empty. This
	// pins what the corpus was sized to produce: hits under the hub share that
	// the cut bites into, a hub that is skipped, a non-ASCII term that
	// matches, and code ahead of tests ahead of documentation.
	s := equivalenceCorpus(t)
	ctx := t.Context()
	terms := []string{"anzahlfahrzeuge", "licenceheader", "größefahrzeug"}

	whole, err := s.SearchSubstringsIn(ctx, terms, 1000, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringsIn: %v", err)
	}
	// 15 chunks in app and 10 in lib; the parked repository's are not counted.
	if len(whole[0]) != 25 {
		t.Errorf("identifier: %d hits over the whole corpus, want 25", len(whole[0]))
	}
	if len(whole[1]) != 0 {
		t.Errorf("hub term: %d hits, want it skipped", len(whole[1]))
	}
	// Both spellings: lower-case as asked in app, the leading capital in lib.
	if len(whole[2]) != 15+10 {
		t.Errorf("non-ASCII identifier: %d hits, want 25", len(whole[2]))
	}
	rank := -1
	for _, h := range whole[0] {
		k := substringKindRank(h.Path)
		if k < rank {
			t.Fatalf("%s comes after a later kind: want code, then tests, then documentation", h.Path)
		}
		rank = k
	}
	if rank != 2 {
		t.Errorf("the identifier's hits end at kind %d, want them to reach documentation", rank)
	}

	cut, err := s.SearchSubstringsIn(ctx, terms, 3, nil, nil)
	if err != nil {
		t.Fatalf("SearchSubstringsIn: %v", err)
	}
	if len(cut[0]) != 3 || substringKindRank(cut[0][2].Path) != 0 {
		t.Errorf("cut to 3: got %d hits ending in %q, want three, all code", len(cut[0]), cut[0][len(cut[0])-1].Path)
	}

	// Under the floor the share says nothing, and the term in half of the
	// stage directory is a lane again — long enough for the cut to bite.
	staged, err := s.SearchSubstringsIn(ctx, terms, 40, nil, StagePrefixes{"app": "intg/"})
	if err != nil {
		t.Fatalf("SearchSubstringsIn: %v", err)
	}
	if len(staged[1]) != 40 {
		t.Errorf("hub term inside the stage: %d hits, want the cut of 40", len(staged[1]))
	}
}

func TestSearchSubstringsIn_aClosedDatabaseIsAnErrorNotAnEmptyLane(t *testing.T) {
	db := testDB(t)
	addRepo(t, db, "app", "master")
	s := NewStore(db)
	_ = db.Close()

	if _, err := s.SearchSubstringsIn(t.Context(), []string{"anzahlfahrzeuge"}, 10, nil, nil); err == nil {
		t.Error("a search over a closed database returned no error")
	}
	if _, err := s.SearchSubstringIn(t.Context(), "anzahlfahrzeuge", 10, nil, nil); err == nil {
		t.Error("a single-term search over a closed database returned no error")
	}
}
