package ask

import (
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/stages"
)

// Every note has a clause for the prompt and a sentence for the reader, in
// every language: a note without one would print its identifier.
func TestReleaseNotes_everyNoteHasAClauseAndASentenceInEveryLanguage(t *testing.T) {
	pair := []string{"prod", "test"}
	notes := []string{NoteUnchanged, NoteUndeclared, NoteMissing, NoteAmbiguous, NoteDigest, NoteSnapshot,
		NoteTagUnknown, NoteOffBranch, NoteNotIndexed, NoteNoIndex, NoteDiverged, NoteBeyondDepth, NoteUnrecorded}
	for _, note := range notes {
		line := ReleaseLine{Image: "acme/x", Repo: "x", Versions: map[string]string{"prod": "1", "test": "2"},
			Note: note, Detail: "1 main", Ahead: "prod"}
		if got := noteEnglish(line); got == "" {
			t.Errorf("noteEnglish(%s) = %q", note, got)
		}
		for _, lang := range []Language{LanguageEN, LanguageDE, LanguageFR, LanguageIT} {
			got := NoRelease(lang, pair, "infra", []ReleaseLine{line})
			if !strings.Contains(got, "\nx") && !strings.Contains(got, "\nx :") {
				t.Errorf("NoRelease(%s, %s) has no line for the component: %q", lang, note, got)
			}
			if strings.Contains(got, "%!") {
				t.Errorf("NoRelease(%s, %s) has a broken format: %q", lang, note, got)
			}
		}
	}
	// A forward range: the count, and the cut once it exceeds the cap.
	fwd := ReleaseLine{Repo: "x", Ahead: "test", Commits: 1}
	if got := noteEnglish(fwd); got != "test is ahead by 1 commit" {
		t.Errorf("forward = %q", got)
	}
	fwd.Commits = 45
	if got := noteEnglish(fwd); !strings.Contains(got, "45 commits") || !strings.Contains(got, "newest 30") {
		t.Errorf("cut = %q", got)
	}
	if got := NoRelease(LanguageEN, pair, "infra", []ReleaseLine{{Image: "acme/y", Note: "made-up"}}); strings.Count(got, "\n") != 0 {
		t.Errorf("an unknown note printed a line: %q", got)
	}
}

func TestReleaseRefusals_areTemplatedInTheReadersLanguage(t *testing.T) {
	declared := stages.Set{{Repo: "infra", Name: "prod", Prefix: "prod/"}}
	if got := releaseNeedsTwoStages(LanguageDE, declared); !strings.Contains(got, "zwei") || !strings.Contains(got, "prod") {
		t.Errorf("two stages (de) = %q", got)
	}
	if got := releaseNeedsTwoStages(LanguageFR, nil); !strings.Contains(got, "-") {
		t.Errorf("two stages, none declared = %q", got)
	}
	if got := releaseNoInfra(LanguageIT, []string{"prod", "test"}, "none"); !strings.Contains(got, "Nessun") {
		t.Errorf("no infra (it) = %q", got)
	}
	if got := releaseNoInfra(LanguageEN, []string{"prod", "test"}, "a-infra, b-infra"); !strings.Contains(got, "a-infra, b-infra") || !strings.Contains(got, "name the product") {
		t.Errorf("many infra = %q", got)
	}
}

func TestInfraRepo_theOneDeclaringBothStagesInsideTheNamedProject(t *testing.T) {
	declared := stages.Set{
		{Repo: "shop-infra", Name: "prod", Prefix: "prod/"},
		{Repo: "shop-infra", Name: "test", Prefix: "test/"},
		{Repo: "crm-infra", Name: "prod", Prefix: "prod/"},
		{Repo: "crm-infra", Name: "test", Prefix: "test/"},
		{Repo: "half-infra", Name: "prod", Prefix: "prod/"},
	}
	pm := projects.Map{} // every repository its own project
	pair := []string{"prod", "test"}

	// Nothing named, two candidates: no repository, both named as the reason.
	if repo, why := infraRepo(declared, pair, nil, pm); repo != "" || why != "crm-infra, shop-infra" {
		t.Errorf("unnamed = %q, %q", repo, why)
	}
	// Named: the one inside that project. half-infra declares one stage only.
	if repo, why := infraRepo(declared, pair, []string{"shop-infra"}, pm); repo != "shop-infra" || why != "" {
		t.Errorf("named = %q, %q", repo, why)
	}
	if repo, why := infraRepo(declared, pair, []string{"half-infra"}, pm); repo != "" || why != "none" {
		t.Errorf("half = %q, %q", repo, why)
	}
	if repo, why := infraRepo(declared, []string{"prod", "uat"}, nil, pm); repo != "" || why != "none" {
		t.Errorf("no such pair = %q, %q", repo, why)
	}
}

func TestSplitImage_keepsTheSeparatorWithTheVersion(t *testing.T) {
	cases := map[string][2]string{
		"acme/shop:1.2":                 {"acme/shop", ":1.2"},
		"host:5000/acme/shop:1.2":       {"host:5000/acme/shop", ":1.2"},
		"host:5000/acme/shop":           {"host:5000/acme/shop", ""},
		"acme/shop@sha256:abc":          {"acme/shop", "@sha256:abc"},
		"registry.example.invalid/shop": {"registry.example.invalid/shop", ""},
	}
	for ref, want := range cases {
		name, version := splitImage(ref)
		if name != want[0] || version != want[1] {
			t.Errorf("splitImage(%q) = %q, %q; want %q, %q", ref, name, version, want[0], want[1])
		}
	}
	if got := shortDigest("sha256:0123456789abcdefXYZ"); got != "0123456789ab" {
		t.Errorf("shortDigest = %q", got)
	}
}
