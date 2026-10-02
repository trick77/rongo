package retrieve

import (
	"fmt"
	"reflect"
	"testing"
)

func TestSearchSubstringsIn_returnsPerTermWhatTheTwoScanRungDid(t *testing.T) {
	// Given a corpus with every case the rung decides on: code, tests and
	// documentation holding one identifier, a hub term past the floor, a
	// non-ASCII identifier, a second repository, a parked one, and a stage
	// directory
	db := testDB(t)
	addRepo(t, db, "app", "master")
	addRepo(t, db, "lib", "main")
	addRepo(t, db, "parked", "master")
	for i := range substringHubFloor + 40 {
		body := "package main // filler"
		switch {
		case i%2 == 0:
			body = "package main // licenceheader boilerplate"
		case i%7 == 0:
			body = "x.getAnzahlFahrzeuge(); y.setAnzahlfahrzeuge(1)"
		case i%11 == 0:
			body = "val größeFahrzeug = fahrzeug.getGrößeFahrzeug()"
		}
		path := fmt.Sprintf("src/f%04d.go", i)
		switch i % 5 {
		case 1:
			path = fmt.Sprintf("src/f%04d_test.go", i)
		case 2:
			path = fmt.Sprintf("docs/f%04d.md", i)
		case 3:
			path = fmt.Sprintf("intg/f%04d.yaml", i)
		}
		addChunkAt(t, db, "app", path, 0, 1, 2, "sym", body, farVec)
	}
	for i := range 30 {
		addChunkAt(t, db, "lib", fmt.Sprintf("l%02d.go", i), 0, 1, 2, "sym", "lib.AnzahlFahrzeuge + größeFahrzeug", farVec)
		addChunkAt(t, db, "parked", fmt.Sprintf("p%02d.go", i), 0, 1, 2, "sym", "parked.AnzahlFahrzeuge", farVec)
	}
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'parked'`); err != nil {
		t.Fatalf("park: %v", err)
	}
	s := NewStore(db)
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

			// Then each term's list is the one it got asked alone, two scans
			for i, term := range terms {
				want, err := referenceSubstring(s, ctx, term, n, sc.repos, sc.stage)
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

func TestSearchSubstringsIn_findsSomethingInTheFixtureItIsComparedOn(t *testing.T) {
	// The comparison above is only worth something if the lists are not all
	// empty; this pins that the fixture exercises both outcomes.
	db := testDB(t)
	addRepo(t, db, "app", "master")
	addChunkAt(t, db, "app", "a.go", 0, 1, 2, "sym", "x.getAnzahlFahrzeuge()", farVec)
	addChunkAt(t, db, "app", "b.go", 0, 1, 2, "sym", "nothing here", farVec)

	got, err := NewStore(db).SearchSubstringsIn(t.Context(), []string{"anzahlfahrzeuge", "absent"}, 10, nil, nil)

	if err != nil {
		t.Fatalf("SearchSubstringsIn: %v", err)
	}
	if len(got) != 2 || len(got[0]) != 1 || got[0][0].Path != "a.go" || got[1] != nil {
		t.Errorf("got %+v, want the one chunk for the first term and no lane for the second", got)
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
