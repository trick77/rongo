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
