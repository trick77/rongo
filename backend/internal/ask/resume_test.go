package ask

import (
	"context"
	"reflect"
	"testing"
)

func TestEntryRepos(t *testing.T) {
	for name, tc := range map[string]struct {
		members []string
		repo    string
		want    []string
	}{
		"project":                {[]string{"a-api", "a-ui"}, "a", []string{"a-api", "a-ui"}},
		"project of one":         {nil, "solo", []string{"solo"}},
		"all repositories entry": {nil, "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := EntryRepos(tc.members, tc.repo); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EntryRepos = %v, want %v", got, tc.want)
			}
		})
	}
}

// A repository choice IS the scope: Known becomes the choice, everything else
// the card was asked under carries over, and the thread is pinned to it.
func TestResumeUnder_aRepositoryChoiceNarrows(t *testing.T) {
	stored := Scope{Known: []string{"older"}, Unknown: []string{"ghost"}, Intent: "how", All: true}
	basis := func(context.Context) ([]Source, int, error) { return nil, 0, nil }
	prior := Thread{Pin: []string{"older"}, All: true, Question: "q0", Answer: "a0", SourcesTotal: 2, ReadBasis: basis}
	scope, th := ResumeUnder(stored, true, []string{"a-api", "a-ui"}, prior)
	if !reflect.DeepEqual(scope.Known, []string{"a-api", "a-ui"}) || scope.All {
		t.Errorf("scope known %v all %v", scope.Known, scope.All)
	}
	if !reflect.DeepEqual(scope.Unknown, []string{"ghost"}) || scope.Intent != "how" {
		t.Errorf("stored scope lost: %+v", scope)
	}
	if !reflect.DeepEqual(th.Pin, scope.Known) || th.All {
		t.Errorf("thread pin %v all %v", th.Pin, th.All)
	}
	if th.Question != "q0" || th.Answer != "a0" || th.SourcesTotal != 2 || th.ReadBasis == nil {
		t.Errorf("what the turn follows was lost: %+v", th)
	}
}

func TestResumeUnder_theAllRepositoriesEntry(t *testing.T) {
	scope, th := ResumeUnder(Scope{Known: []string{"older"}}, true, nil, Thread{})
	if !scope.All || scope.Known != nil || th.Pin != nil {
		t.Errorf("scope all %v known %v pin %v", scope.All, scope.Known, th.Pin)
	}
}

// A module card replays its hits under the scope it was asked under, untouched.
func TestResumeUnder_aModuleChoiceKeepsTheStoredScope(t *testing.T) {
	stored := Scope{Known: []string{"repo"}, All: true}
	scope, th := ResumeUnder(stored, false, nil, Thread{Pin: []string{"other"}})
	if !reflect.DeepEqual(scope, stored) {
		t.Errorf("scope = %+v, want %+v", scope, stored)
	}
	if !reflect.DeepEqual(th.Pin, []string{"repo"}) {
		t.Errorf("pin = %v", th.Pin)
	}
}
