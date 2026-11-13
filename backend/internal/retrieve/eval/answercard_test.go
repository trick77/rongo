package eval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
)

// fakeResumer records the one call a chosen card entry makes.
type fakeResumer struct {
	calls    int
	question string
	u        ask.Understanding
	repos    []string
	audience ask.Audience
	lang     ask.Language
	scope    ask.Scope
	thread   ask.Thread
	answer   ask.Answer
	err      error
}

func (f *fakeResumer) ResumeRepo(_ context.Context, question string, u ask.Understanding, repos []string,
	audience ask.Audience, lang ask.Language, scope ask.Scope, t ask.Thread, _ ask.Events) (ask.Answer, error) {
	f.calls++
	f.question, f.u, f.repos, f.audience, f.lang, f.scope, f.thread = question, u, repos, audience, lang, scope, t
	return f.answer, f.err
}

// projectCard is a card as Route draws one between two products: project
// entries with an empty module key, then the "all repositories" row.
func projectCard() *ask.Clarification {
	return &ask.Clarification{
		Understanding: ask.Understanding{Terms: []string{"template"}, Prior: "earlier"},
		Scope:         ask.Scope{Unknown: []string{"ghost"}, Intent: "how", Structure: "derived per turn", Resumed: true},
		Candidates: []ask.Candidate{
			{Repo: "templates", Members: []string{"templates-api", "templates-ui"}, Hits: []retrieve.Hit{{Repo: "templates-api"}}},
			{Repo: "migration", Branch: "main", Members: []string{"migration"}},
			{Title: "All repositories"},
		},
	}
}

func TestPickProject_onlyAProjectEntryTheCardOffers(t *testing.T) {
	moduleCard := &ask.Clarification{Candidates: []ask.Candidate{{Repo: "templates", ModuleKey: "internal/api"}}}
	tooBroad := projectCard()
	tooBroad.TooBroad = true
	for name, tc := range map[string]struct {
		clar    *ask.Clarification
		project string
		want    string
		ok      bool
	}{
		"multi-member project":      {projectCard(), "templates", "templates", true},
		"project of one":            {projectCard(), "migration", "migration", true},
		"surrounding space":         {projectCard(), " migration ", "migration", true},
		"not offered":               {projectCard(), "billing", "", false},
		"no project in the rubric":  {projectCard(), "", "", false},
		"module entry of that repo": {moduleCard, "templates", "", false},
		"case differs":              {projectCard(), "Templates", "", false},
		"titled, not named":         {&ask.Clarification{Candidates: []ask.Candidate{{Repo: "templates", Title: "Templates today"}}}, "Templates today", "", false},
		"too-broad panel row":       {tooBroad, "migration", "migration", true},
		"no card":                   {nil, "templates", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := pickProject(tc.clar, tc.project)
			if ok != tc.ok || got.Repo != tc.want {
				t.Errorf("pickProject(%q) = %q, %v; want %q, %v", tc.project, got.Repo, ok, tc.want, tc.ok)
			}
		})
	}
}

// The asked-back line says why a rubric's project went unanswered, so a
// misspelt name does not pass for a question that keeps asking back.
func TestUnansweredNote(t *testing.T) {
	moduleCard := &ask.Clarification{Candidates: []ask.Candidate{{Repo: "templates", ModuleKey: "internal/api"}, {Title: "All repositories"}}}
	for name, tc := range map[string]struct {
		clar    *ask.Clarification
		project string
		want    string
	}{
		"no project in the rubric": {projectCard(), "", ""},
		"not offered":              {projectCard(), "billing", `; project "billing" not offered, card has [templates migration]`},
		"module card":              {moduleCard, "templates", `; project "templates" wanted, but the card offers no project entry (module card): not answerable`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := unansweredNote(tc.clar, tc.project); got != tc.want {
				t.Errorf("unansweredNote = %q, want %q", got, tc.want)
			}
		})
	}
}

// A card is recorded as offered: each entry with the repositories it folds
// and the best file of each repository its hits fall in, so a run shows which
// project made the card even when a shared library outscores the product.
func TestCardEntries_recordWhatTheCardOffered(t *testing.T) {
	clar := &ask.Clarification{Candidates: []ask.Candidate{
		{Repo: "templates", Members: []string{"shared", "templates"}, Score: 0.031, Hits: []retrieve.Hit{
			{Repo: "shared", Path: "a.txt"}, {Repo: "shared", Path: "x.txt"}, {Repo: "shared", Path: "y.txt"},
			{Repo: "templates", Path: "b.txt"}, {Repo: "templates", Path: "c.txt"},
		}},
		{Repo: "billing", ModuleKey: "internal/api", Score: 0.02},
		{Title: "Alle Repositories"},
	}}
	got := cardEntries(clar)
	want := []cardEntry{
		{Name: "templates", Members: []string{"shared", "templates"}, Score: 0.031, Hits: []string{"shared/a.txt", "templates/b.txt"}},
		{Name: "billing", Module: "internal/api", Score: 0.02},
		{AllRepos: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cardEntries =\n%+v\nwant\n%+v", got, want)
	}
	if cardEntries(nil) != nil {
		t.Error("no card, no entries")
	}
	wantLine := "card: templates [shared templates] 0.0310: shared/a.txt, templates/b.txt | billing module internal/api 0.0200 | all repositories"
	if line := cardLine(got, false); line != wantLine {
		t.Errorf("cardLine =\n%q\nwant\n%q", line, wantLine)
	}
	if line := cardLine(got[:1], true); !strings.HasPrefix(line, "too-broad panel: templates ") {
		t.Errorf("too-broad cardLine = %q", line)
	}
}

// The answers file is read after the run: the card keeps its keys, and a
// turn without one carries none.
func TestAnswerRecord_keepsTheCard(t *testing.T) {
	b, err := json.Marshal(answerRecord{Asked: true, TooBroad: true, Card: []cardEntry{{Name: "templates", Score: 0.5}, {AllRepos: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"card":[{"name":"templates","score":0.5},{"all_repos":true}]`, `"too_broad":true`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("record %s lacks %s", b, key)
		}
	}
	b, err = json.Marshal(answerRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"card"`) || strings.Contains(string(b), `"too_broad"`) {
		t.Errorf("record without a card carries one: %s", b)
	}
}

// The resumed turn gets what handleAsk hands ResumeRepo: the entry's members,
// the card's scope as the record reads it back narrowed to them, and the
// thread pinned to them.
func TestAnswerCard_continuesTheWayTheProductDoes(t *testing.T) {
	clar := projectCard()
	pick, ok := pickProject(clar, "templates")
	if !ok {
		t.Fatal("templates not picked")
	}
	f := &fakeResumer{answer: ask.Answer{Text: "the answer"}}
	basis := func(context.Context) ([]ask.Source, int, error) { return nil, 0, nil }
	thread := ask.Thread{Pin: []string{"older"}, All: true, Question: "first", Answer: "first answer", ReadBasis: basis}
	a, err := answerCard(context.Background(), f, "the question", ask.AudienceBA, ask.LanguageDE, clar, pick, thread)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "the answer" || f.calls != 1 {
		t.Fatalf("answer %q after %d calls", a.Text, f.calls)
	}
	if f.question != "the question" || f.audience != ask.AudienceBA || f.lang != ask.LanguageDE {
		t.Errorf("question %q audience %q lang %q", f.question, f.audience, f.lang)
	}
	members := []string{"templates-api", "templates-ui"}
	if !reflect.DeepEqual(f.repos, members) {
		t.Errorf("repos = %v, want %v", f.repos, members)
	}
	if !reflect.DeepEqual(f.u.Terms, []string{"template"}) || f.u.Prior != "earlier" {
		t.Errorf("understanding = %+v, want the card's", f.u)
	}
	if !reflect.DeepEqual(f.scope.Known, members) || f.scope.All {
		t.Errorf("scope known %v all %v, want %v false", f.scope.Known, f.scope.All, members)
	}
	if !reflect.DeepEqual(f.scope.Unknown, []string{"ghost"}) || f.scope.Intent != "how" {
		t.Errorf("stored scope fields lost: unknown %v intent %q", f.scope.Unknown, f.scope.Intent)
	}
	// Read back from the record: what the store never keeps, a resume never has.
	if f.scope.Structure != "" || f.scope.Resumed {
		t.Errorf("unpersisted scope fields reached the resume: structure %q resumed %v", f.scope.Structure, f.scope.Resumed)
	}
	if !reflect.DeepEqual(f.thread.Pin, members) || f.thread.All {
		t.Errorf("thread pin %v all %v, want %v false", f.thread.Pin, f.thread.All, members)
	}
	if f.thread.Question != "first" || f.thread.Answer != "first answer" || f.thread.ReadBasis == nil {
		t.Errorf("thread lost what the turn follows: %q %q basis %v", f.thread.Question, f.thread.Answer, f.thread.ReadBasis != nil)
	}
}

func TestAnswerCard_projectOfOneSearchesItsRepository(t *testing.T) {
	clar := &ask.Clarification{Candidates: []ask.Candidate{{Repo: "migration", Branch: "main"}}}
	pick, _ := pickProject(clar, "migration")
	f := &fakeResumer{}
	if _, err := answerCard(context.Background(), f, "q", ask.AudienceBA, ask.LanguageEN, clar, pick, ask.Thread{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"migration"}; !reflect.DeepEqual(f.repos, want) || !reflect.DeepEqual(f.scope.Known, want) {
		t.Errorf("repos %v scope %v, want %v", f.repos, f.scope.Known, want)
	}
}

func TestAnswerCard_aFailedResumeIsTheError(t *testing.T) {
	clar := projectCard()
	pick, _ := pickProject(clar, "migration")
	boom := errors.New("boom")
	if _, err := answerCard(context.Background(), &fakeResumer{err: boom}, "q", ask.AudienceBA, ask.LanguageEN, clar, pick, ask.Thread{}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestRubric_readsTheProjectAnAnalystPicks(t *testing.T) {
	var r Rubric
	if err := json.Unmarshal([]byte(`{"question":"q","project":"templates"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Project != "templates" {
		t.Errorf("Project = %q", r.Project)
	}
}

func TestAnswerRecord_saysWhichEntryWasPicked(t *testing.T) {
	body, err := json.Marshal(answerRecord{Asked: true, Answered: true, Picked: "templates", FollowsPicked: "migration"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"asked":true`, `"answered":true`, `"picked":"templates"`, `"follows_picked":"migration"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s lacks %s", body, want)
		}
	}
	body, _ = json.Marshal(answerRecord{})
	if strings.Contains(string(body), "picked") || !strings.Contains(string(body), `"answered":false`) {
		t.Errorf("an unpicked record: %s", body)
	}
}

// Cards are answered unless the arm is switched off, which reproduces the
// numbers of the runs before it: every card asked back, nothing judged.
func TestAnswerCardsArm(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "0": false} {
		if got := answerCardsArm(func(string) string { return v }); got != want {
			t.Errorf("BACKEND_EVAL_ANSWER_CARDS=%q: %v, want %v", v, got, want)
		}
	}
}
