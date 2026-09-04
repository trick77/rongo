package retrieve

import (
	"context"
	"testing"
)

func TestResolveReposSplitsWhatTheIndexCarriesFromWhatItDoesNot(t *testing.T) {
	// Given an index carrying two of the names a question might use.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "rongo", "master")
	r := New(db, nil)

	// When, with "peeqs" repeating peeq under its possessive spelling.
	known, unknown, err := r.ResolveRepos(context.Background(), []string{"rongo", "loom", "peeq", "peeqs"}, "")

	// Then the indexed names come back once each — the repeat is one
	// repository, not two — and only the name nothing resembles is reported.
	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 2 || !contains(known, "peeq") || !contains(known, "rongo") {
		t.Errorf("known = %v, want peeq and rongo once each", known)
	}
	if len(unknown) != 1 || unknown[0] != "loom" {
		t.Errorf("unknown = %v, want only the name no repository carries", unknown)
	}
}

func TestResolveReposDoesNotReportAMisspellingAsMissing(t *testing.T) {
	// The three names in knownRepos' comment are the measured ones: "peeqs"
	// is the possessive, "Peek" a mishearing, "asg017/sqlite-vec" a name
	// carrying its owner. Reporting any of them as "not in the index" would
	// put a false sentence in front of the reader and hand the answer model a
	// rule about a repository whose code is right there in the sources.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), []string{"peeqs", "Peek", "PEEQ"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want nothing claimed about a misspelling", unknown)
	}
	// The possessive and the case are the same repository under another
	// spelling, and come back under the name the index uses. The mishearing
	// is dropped in silence: it narrows nothing, exactly as before.
	for _, n := range known {
		if n != "peeq" {
			t.Errorf("known = %v, want only the indexed spelling", known)
		}
	}
}

func TestResolveReposReportsANameThatResemblesNothing(t *testing.T) {
	// The case the notice exists for: a repository that is simply not there.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "rongo", "master")
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), []string{"loom", "rongo"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "loom" {
		t.Errorf("unknown = %v, want the missing repository named", unknown)
	}
	if len(known) != 1 || known[0] != "rongo" {
		t.Errorf("known = %v", known)
	}
}

func TestResolveReposDoesNotReportAnOwnerPrefixedNameAsMissing(t *testing.T) {
	// "asg017/sqlite-vec" is the third measured guess. The owner prefix makes
	// it match no row, and before the notice existed that cost nothing. It
	// must not now become "no repository called asg017/sqlite-vec is indexed"
	// about a repository the index has.
	db := testDB(t)
	addRepo(t, db, "sqlite-vec", "main")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(), []string{"asg017/sqlite-vec"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want the owner prefix not to make a repository missing", unknown)
	}
}

func TestResolveReposFollowsTheQuestionsOwnWords(t *testing.T) {
	// knownRepos unions the guess with what the question names as a whole
	// word, and the rung upstream keys off that union: a comparison the
	// understanding step failed to guess still reads as one, because the
	// reader typed both names.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "rongo", "master")
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), nil,
		"How do peeq and rongo differ in session handling?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 2 {
		t.Errorf("known = %v, want both repositories the question named", known)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want nothing reported when the guess named nothing", unknown)
	}
}

func TestResolveReposNamesADuplicateOnlyOnce(t *testing.T) {
	// A repeated guess must not become two notices about the same repository.
	db := testDB(t)
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), []string{"loom", "loom"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 0 {
		t.Errorf("known = %v, want none", known)
	}
	if len(unknown) != 1 {
		t.Errorf("unknown = %v, want the name once", unknown)
	}
}

func TestResolveReposOnNoNamesIsNoRestriction(t *testing.T) {
	// The ordinary question names nothing, and must not be reported as
	// naming something the index lacks.
	db := testDB(t)
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), nil, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 0 || len(unknown) != 0 {
		t.Errorf("known = %v, unknown = %v, want both empty", known, unknown)
	}
}
