package ask

import (
	"context"
	"reflect"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/stages"
)

var declaredStages = stages.Set{
	{Repo: "acme-infra", Name: "prod", Prefix: "prod/", Aliases: []string{"production", "produktion"}},
	{Repo: "acme-infra", Name: "intg", Prefix: "intg/"},
}

func TestResolveStage(t *testing.T) {
	cases := []struct {
		name, question, guessed, want string
		detail                        map[string]any
	}{
		{"reader names it", "how often is the digest sent in production?", "", "prod", map[string]any{}},
		{"model names it, reader did not", "how often in the integration environment?", "intg", "intg", map[string]any{}},
		{"both agree", "in production", "prod", "prod", map[string]any{}},
		{"they disagree: no stage", "in production", "intg", "", map[string]any{"stage_conflict": []string{"prod", "intg"}}},
		{"model invents a word: dropped", "how is the digest sent", "staging", "", map[string]any{"stage_dropped": "staging"}},
		{"two stages named: both, so none", "compare intg and prod", "prod", "", map[string]any{"stages_named": []string{"intg", "prod"}}},
		{"nothing", "how is the digest sent", "", "", map[string]any{}},
		{"no stages declared at all", "in production", "prod", "", map[string]any{"stage_dropped": "prod"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			declared := declaredStages
			if c.name == "no stages declared at all" {
				declared = nil
			}
			got, detail := resolveStage(c.question, c.guessed, declared)
			if got != c.want {
				t.Errorf("stage = %q, want %q", got, c.want)
			}
			if !reflect.DeepEqual(detail, c.detail) {
				t.Errorf("detail = %v, want %v", detail, c.detail)
			}
		})
	}
}

func TestWithoutStageWords(t *testing.T) {
	got := withoutStageWords([]string{"production", "acme-service", "Prod"}, declaredStages)
	if !reflect.DeepEqual(got, []string{"acme-service"}) {
		t.Errorf("got %v, want the repository alone", got)
	}
}

// TestRunNarrowsTheSearchAndTheRecordToTheAskedStage: "in production" reaches
// the search as a path restriction on the repository declaring stages and
// nothing else, the record carries the stage, and the word is never reported
// as a repository the index lacks.
func TestRunNarrowsTheSearchAndTheRecordToTheAskedStage(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"acme-service", "acme-infra"}}
	// The understanding step guesses "production" as a repository, as it
	// does, and names the stage too.
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":["production"],"stage":"prod"}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{stages: declaredStages})

	var details []map[string]any
	got, _, err := p.Run(context.Background(), "how often is the digest sent in production?", AudienceBA, LanguageEN, Thread{},
		Events{OnDetail: func(step string, d map[string]any) {
			if step == "understanding" {
				details = append(details, d)
			}
		}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Scope.Stage != "prod" {
		t.Errorf("scope.Stage = %q, want prod", got.Scope.Stage)
	}
	if len(got.Scope.Unknown) != 0 {
		t.Errorf("scope.Unknown = %v, want none: production is a stage, not a missing repository", got.Scope.Unknown)
	}
	want := retrieve.StagePrefixes{"acme-infra": "prod/"}
	if !reflect.DeepEqual(search.got.Stage, want) {
		t.Errorf("search stage = %v, want %v", search.got.Stage, want)
	}
	if len(details) != 1 || details[0]["stage"] != "prod" {
		t.Errorf("understanding detail = %v, want the stage named", details)
	}
}

func TestRunWithoutAStageRestrictsNothing(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"stage":""}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{stages: declaredStages})

	got, _, err := p.Run(context.Background(), "how often is the digest sent?", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Scope.Stage != "" || search.got.Stage != nil {
		t.Errorf("stage = %q, search restriction = %v, want neither", got.Scope.Stage, search.got.Stage)
	}
}

func TestResumeRepoSearchesUnderTheCardsStage(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"acme-service", "acme-infra"}}
	c := twoStepUpstream(t, appleTVReply, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{stages: declaredStages})

	_, err := p.ResumeRepo(context.Background(), "q", Understanding{Terms: []string{"t"}}, []string{"acme-infra"},
		AudienceBA, LanguageEN, Scope{Known: []string{"acme-infra"}, Stage: "intg"}, Thread{}, Events{})
	if err != nil {
		t.Fatalf("ResumeRepo: %v", err)
	}
	if !reflect.DeepEqual(search.got.Stage, retrieve.StagePrefixes{"acme-infra": "intg/"}) {
		t.Errorf("search stage = %v, want the card's stage", search.got.Stage)
	}
}
