package retrieve

import (
	"context"
	"database/sql"
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

// addMember inserts a repository that belongs to a project.
func addMember(t *testing.T, db *sql.DB, name, project string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO repo_state (name, clone_url, branch, project) VALUES (?,?,?,?)`,
		name, "file:///"+name, "master", project); err != nil {
		t.Fatalf("insert %s: %v", name, err)
	}
}

func TestResolveReposExpandsAProjectNameToItsMembers(t *testing.T) {
	// "How does checkout work in Shop?" — the reader named the product, which
	// is the name an Analyst actually knows. It resolves to every repository
	// the product is made of.
	db := testDB(t)
	addMember(t, db, "shop-backend", "shop")
	addMember(t, db, "shop-ui", "shop")
	addMember(t, db, "legacy-crm", "legacy-crm")
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), []string{"shop"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 2 || !contains(known, "shop-backend") || !contains(known, "shop-ui") {
		t.Errorf("known = %v, want both of shop's repositories", known)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want empty — shop is a project the index carries", unknown)
	}
}

func TestResolveReposReadsAProjectNameOutOfTheQuestion(t *testing.T) {
	// The guess is allowed to miss, and the reader's own words are the
	// narrowest signal there is — the same union knownRepos already applies to
	// repository names.
	db := testDB(t)
	addMember(t, db, "shop-backend", "shop")
	addMember(t, db, "shop-ui", "shop")
	r := New(db, nil)

	known, _, err := r.ResolveRepos(context.Background(), nil, "wie funktioniert der Checkout im shop?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 2 {
		t.Errorf("known = %v, want the project's repositories", known)
	}
}

func TestResolveReposKeepsNamingAMemberNarrow(t *testing.T) {
	// A project is the default unit, not a floor. Someone who names one
	// repository asked about that repository, and a follow-up inside a
	// project-pinned thread has to be able to narrow — a thread narrows, never
	// widens.
	db := testDB(t)
	addMember(t, db, "shop-backend", "shop")
	addMember(t, db, "shop-ui", "shop")
	r := New(db, nil)

	known, _, err := r.ResolveRepos(context.Background(), []string{"shop-ui"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 1 || known[0] != "shop-ui" {
		t.Errorf("known = %v, want shop-ui alone", known)
	}
}

func TestResolveReposReportsAProjectTheIndexDoesNotCarry(t *testing.T) {
	// Said out loud, the same way an unknown repository is: dropped from the
	// search, named in the notice, and the prompt forbidden to claim anything
	// about it.
	db := testDB(t)
	addMember(t, db, "shop-backend", "shop")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(), []string{"warehouse"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "warehouse" {
		t.Errorf("unknown = %v, want the project nothing carries", unknown)
	}
}

func TestResolveReposDoesNotReadACommonWordProjectOutOfAQuestion(t *testing.T) {
	// commonWords exists so a repository called "backend" does not narrow every
	// question that says the word. A project named one of them is guess-only
	// for exactly the same reason, and the guard is the same guard.
	db := testDB(t)
	addMember(t, db, "svc-a", "backend")
	addMember(t, db, "svc-b", "backend")
	r := New(db, nil)

	known, _, err := r.ResolveRepos(context.Background(), nil, "how does the backend read its config?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 0 {
		t.Errorf("known = %v, want no restriction: 'backend' is a common word, not a narrowing", known)
	}
}

func TestResolveReposDoesNotReportHalfOfAHyphenatedNameAsMissing(t *testing.T) {
	// Given the index carrying transmission-ui and nothing called
	// transmission, and a question that only ever wrote the full name.
	db := testDB(t)
	addMember(t, db, "transmission-ui", "transmission-ui")
	r := New(db, nil)

	// When the understanding takes the name apart the way a person would: a
	// UI, and the daemon it must be a UI for. Only the first half was typed.
	known, unknown, err := r.ResolveRepos(context.Background(),
		[]string{"transmission-ui", "transmission"}, "what does transmission-ui do?")

	// Then nothing is claimed about the half the model invented. Saying "no
	// project called transmission is in the index" answers a question the
	// reader did not ask and states a fact they cannot check.
	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want nothing claimed about a name the question never wrote", unknown)
	}
	if len(known) != 1 || known[0] != "transmission-ui" {
		t.Errorf("known = %v, want transmission-ui alone", known)
	}
}

func TestResolveReposStillReportsAHyphenSegmentTheQuestionNames(t *testing.T) {
	// The other half of the same rule. Here the reader really did name two
	// systems, and one of them is genuinely not indexed - which is the case
	// the notice exists for.
	db := testDB(t)
	addMember(t, db, "transmission-ui", "transmission-ui")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(),
		[]string{"transmission-ui", "transmission"},
		"how does transmission-ui talk to transmission?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "transmission" {
		t.Errorf("unknown = %v, want the repository the reader named and the index lacks", unknown)
	}
}

func TestResolveReposStillReportsAPluralSegmentTheQuestionNames(t *testing.T) {
	// foldRepo takes a trailing "s" off, so the folded guess is "tool" while
	// the reader typed "tools". Testing only the folded spelling finds an
	// occurrence whose own s is a word rune, and the question that names the
	// missing repository outright would read as never naming it.
	db := testDB(t)
	addMember(t, db, "media-tools", "media-tools")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(),
		[]string{"media-tools", "tools"},
		"how does media-tools differ from tools?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "tools" {
		t.Errorf("unknown = %v, want the repository the reader named and the index lacks", unknown)
	}
}

func TestResolveReposDropsAHyphenSegmentWhicheverHalfCarriesThePlural(t *testing.T) {
	// The mirror of the transmission case: foldRepo strips the s from
	// "media-tools" and not from "tools-media", so the halves have to be
	// folded one by one or the same invented name is suppressed in one order
	// and reported in the other.
	db := testDB(t)
	addMember(t, db, "tools-media", "tools-media")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(),
		[]string{"tools-media", "tools"}, "what does tools-media do?")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want nothing claimed about a name the question never wrote", unknown)
	}
}

func TestResolveReposStillReportsANameThatIsNoSegment(t *testing.T) {
	// A follow-up carries its subject from the previous turn, so the guess
	// names a repository the current question does not spell. That name is
	// still reported: it resembles nothing indexed and is nobody's misreading
	// of a hyphen.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	r := New(db, nil)

	_, unknown, err := r.ResolveRepos(context.Background(), []string{"loom"}, "")

	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "loom" {
		t.Errorf("unknown = %v, want the missing repository named", unknown)
	}
}
