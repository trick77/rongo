package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
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

// TestDescribeProjectsClosesTheBlockOnceAfterTheParts: with a declared
// project above the units paragraph, the never-cite sentence is still the
// last thing in the block, and appears once. Left where StructureBlock put
// it, the parts list would sit after the rule that covers it.
func TestDescribeProjectsClosesTheBlockOnceAfterTheParts(t *testing.T) {
	r := &fakeRouter{
		projects: shopMap(t),
		units: map[string][]units.Unit{"shop-backend": {
			{Key: "api", Kind: units.KindMavenService, Name: "api"},
			{Key: "worker", Kind: units.KindMavenService, Name: "worker"},
		}},
		unitDeps: map[string][]units.Dep{"shop-backend": {{From: "api", To: "worker"}}},
	}
	p := newTestPipeline(t, func(f *pipelineFakes) { f.router = r })

	got := p.describeProjects(context.Background(), Scope{Known: []string{"shop-ui", "shop-backend", "shop-events"}})
	closer := "This is configuration, not code."
	if n := strings.Count(got.Structure, closer); n != 1 {
		t.Errorf("the closing sentence appears %d times, want once:\n%s", n, got.Structure)
	}
	parts := strings.Index(got.Structure, `Repository "shop-backend" is built from 2 parts:`)
	if parts < 0 {
		t.Fatalf("structure lacks the parts paragraph:\n%s", got.Structure)
	}
	if strings.Index(got.Structure, closer) < parts {
		t.Errorf("the closing sentence sits before the parts it has to cover:\n%s", got.Structure)
	}
	if !strings.Contains(got.Structure, "api uses worker.") {
		t.Errorf("structure lacks the unit edge:\n%s", got.Structure)
	}
}

// TestWithPartsClosesTwoProjectsAndTheirPartsOnce: two declared projects in
// scope and a units paragraph after them are one block under one sentence,
// which comes last.
func TestWithPartsClosesTwoProjectsAndTheirPartsOnce(t *testing.T) {
	block := StructureBlock([]projects.Project{
		{Name: "shop", Members: []projects.Repo{{Name: "shop-ui", Part: "ui"}, {Name: "shop-backend", Part: "backend"}}},
		{Name: "crm", Members: []projects.Repo{{Name: "crm-ui", Part: "ui"}, {Name: "crm-api", Part: "backend"}}},
	})
	parts := units.Describe("shop-backend", []units.Unit{
		{Key: "api", Kind: units.KindMavenService, Name: "api"},
		{Key: "worker", Kind: units.KindMavenService, Name: "worker"},
	}, []units.Dep{{From: "api", To: "worker"}})

	got := withParts(block, parts)
	closer := "This is configuration, not code."
	if n := strings.Count(got, closer); n != 1 {
		t.Errorf("the closing sentence appears %d times, want once:\n%s", n, got)
	}
	if !strings.HasSuffix(got, structureIsConfiguration) {
		t.Errorf("the block does not end with the closing sentence:\n%s", got)
	}
	for _, want := range []string{`Project "shop"`, `Project "crm"`, `Repository "shop-backend" is built from 2 parts:`} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	if withParts(block, "") != block {
		t.Errorf("no parts changed the block")
	}
}
