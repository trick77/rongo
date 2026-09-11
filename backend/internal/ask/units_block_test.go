package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/units"
)

// TestDescribeProjectsAppendsWhatANarrowedRepositoryIsBuiltFrom: a question
// narrowed to one repository gets that repository's declared parts in the
// structure block, closed by the configuration sentence so the model does
// not cite it. A corpus-wide turn and a repository of one build get nothing.
func TestDescribeProjectsAppendsWhatANarrowedRepositoryIsBuiltFrom(t *testing.T) {
	r := &fakeRouter{
		units: map[string][]units.Unit{"svc": {
			{Key: "lib/persistence", Kind: units.KindMavenLibrary, Name: "claims-persistence"},
			{Key: "service/intranet", Kind: units.KindMavenService, Name: "claims-intranet-service"},
		}},
		unitDeps: map[string][]units.Dep{"svc": {{From: "service/intranet", To: "lib/persistence"}}},
	}
	p := newTestPipeline(t, func(f *pipelineFakes) { f.router = r })

	got := p.describeProjects(context.Background(), Scope{Known: []string{"svc"}})
	for _, want := range []string{
		`Repository "svc" is built from 2 parts:`,
		"claims-intranet-service uses claims-persistence.",
		"This is configuration, not code.",
	} {
		if !strings.Contains(got.Structure, want) {
			t.Errorf("structure lacks %q:\n%s", want, got.Structure)
		}
	}
	if n := strings.Count(got.Structure, "This is configuration, not code."); n != 1 {
		t.Errorf("the closing sentence appears %d times, want once", n)
	}

	if got := p.describeProjects(context.Background(), Scope{}); got.Structure != "" {
		t.Errorf("a corpus-wide turn got a structure block: %q", got.Structure)
	}
	if got := p.describeProjects(context.Background(), Scope{Known: []string{"plain"}}); got.Structure != "" {
		t.Errorf("a repository of one build got a structure block: %q", got.Structure)
	}
}
