package ask

import (
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
)

func TestStructureBlockSaysALibraryIsSharedNotInsideTheProject(t *testing.T) {
	// Given a product whose backend uses a library, and a UI nothing declares
	// an edge for. The library is not a member: projects.Load hands the edge
	// through, so the block must name it as a separate, shared repository.
	got := StructureBlock([]projects.Project{{
		Name: "shop",
		Members: []projects.Repo{
			{Name: "shop-backend", Part: "backend", Uses: []string{"shop-contract", "acme-commons"}},
			{Name: "shop-contract", Part: "contract"},
			{Name: "shop-ui", Part: "ui"},
		},
	}})

	if !strings.Contains(got, "shop-backend uses shop-contract.") {
		t.Errorf("the in-project edge is still an in-project edge:\n%s", got)
	}
	if !strings.Contains(got, "shop-backend uses the shared library acme-commons.") {
		t.Errorf("the library edge is said as a library edge:\n%s", got)
	}
	inside := got[strings.Index(got, "Declared connections inside the project"):]
	inside = inside[:strings.Index(inside, "Shared libraries")]
	if strings.Contains(inside, "acme-commons") {
		t.Errorf("a library is not a connection inside the project:\n%s", got)
	}
	if !strings.Contains(got, "Nothing in this project uses shop-backend, shop-ui.") {
		t.Errorf("unreached is computed over members only:\n%s", got)
	}
	if strings.Contains(got, "shop-backend uses acme-commons.") {
		t.Errorf("the library edge must not read like a sibling edge:\n%s", got)
	}
}

func TestStructureBlockSpeaksForAProjectOfOneThatUsesALibrary(t *testing.T) {
	// One member, nothing else declared — except an edge to a library. That
	// edge is the one fact the model cannot see in a citation.
	got := StructureBlock([]projects.Project{{
		Name:    "billing",
		Members: []projects.Repo{{Name: "billing", Uses: []string{"acme-commons"}}},
	}})
	if !strings.Contains(got, "billing uses the shared library acme-commons.") {
		t.Errorf("StructureBlock = %q, want the library edge", got)
	}
	if strings.Contains(got, "Declared connections inside the project") {
		t.Errorf("no in-project edge means no in-project section:\n%s", got)
	}
}
