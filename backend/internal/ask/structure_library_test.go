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

func TestStructureBlockCallsALibraryALibraryNotAProduct(t *testing.T) {
	// A turn about shop that hopped into the library covers both: the shop
	// block just said acme-commons is a shared library, so its own block
	// must not turn around and call it a product.
	got := StructureBlock([]projects.Project{
		{Name: "shop", Members: []projects.Repo{
			{Name: "shop-backend", Part: "backend", Uses: []string{"acme-commons"}},
		}},
		{Name: "acme-commons", Members: []projects.Repo{
			{Name: "acme-commons", Description: "Shared utilities.", Library: true},
		}},
	})
	if !strings.Contains(got, `Repository "acme-commons" is a shared library`) {
		t.Errorf("the library's own block names it as a library:\n%s", got)
	}
	if strings.Contains(got, `Project "acme-commons"`) {
		t.Errorf("a library is not a product:\n%s", got)
	}
	if !strings.Contains(got, "acme-commons — Shared utilities.") {
		t.Errorf("its description still reaches the prompt:\n%s", got)
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
