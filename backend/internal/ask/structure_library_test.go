package ask

import (
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
)

func TestStructureBlockListsALibraryAsASharedMember(t *testing.T) {
	// Given a product whose backend uses a library. projects.Load made the
	// library a member of the product, so it is listed with the rest and its
	// line says it is shared: the model must not read its code as the
	// product's own, and the edge to it is an ordinary connection.
	got := StructureBlock([]projects.Project{{
		Name: "shop",
		Members: []projects.Repo{
			{Name: "acme-commons", Part: "library", Description: "Shared utilities.", Library: true},
			{Name: "shop-backend", Part: "backend", Uses: []string{"shop-contract", "acme-commons"}},
			{Name: "shop-contract", Part: "contract"},
			{Name: "shop-ui", Part: "ui"},
		},
	}})

	if !strings.Contains(got, `Project "shop" is one product in 4 repositories.`) {
		t.Errorf("the library counts as a member:\n%s", got)
	}
	if !strings.Contains(got, "acme-commons (shared library, also used by other products) — Shared utilities.") {
		t.Errorf("the library's line says it is shared:\n%s", got)
	}
	if !strings.Contains(got, "shop-backend uses acme-commons.") || !strings.Contains(got, "shop-backend uses shop-contract.") {
		t.Errorf("both edges are connections inside the project:\n%s", got)
	}
	if !strings.Contains(got, "Nothing in this project uses shop-backend, shop-ui.") {
		t.Errorf("the library is reached, so it is not among the unreached:\n%s", got)
	}
}

func TestStructureBlockCallsALibraryALibraryNotAProduct(t *testing.T) {
	// The library's own project of one, when a turn is about the library
	// itself: not a product.
	got := StructureBlock([]projects.Project{
		{Name: "acme-commons", Members: []projects.Repo{
			{Name: "acme-commons", Description: "Shared utilities.", Library: true},
		}},
	})
	if !strings.Contains(got, `Repository "acme-commons" is a shared library`) {
		t.Errorf("the library's own block names it as a library:\n%s", got)
	}
	if strings.Contains(got, `Project "acme-commons"`) || strings.Contains(got, "also used by other products") {
		t.Errorf("a library alone is neither a product nor a member of one:\n%s", got)
	}
	if !strings.Contains(got, "acme-commons — Shared utilities.") {
		t.Errorf("its description still reaches the prompt:\n%s", got)
	}
}
