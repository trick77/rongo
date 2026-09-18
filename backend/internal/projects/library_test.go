package projects

import (
	"context"
	"testing"
)

func TestLoad_keepsAUsesEdgeIntoALibraryAndDropsAStrayCrossProjectOne(t *testing.T) {
	// Given a library two products use, and one stray edge between the two
	// products, which repos.Load refuses and only a hand-written row can carry.
	db := seed(t,
		[][4]string{
			{"shop-backend", "shop", "backend", ""},
			{"billing-api", "billing", "backend", ""},
			{"acme-commons", "acme-commons", "", "Shared utilities."},
		},
		[][2]string{
			{"shop-backend", "acme-commons"},
			{"billing-api", "acme-commons"},
			{"shop-backend", "billing-api"},
		},
	)
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = 'acme-commons'`); err != nil {
		t.Fatal(err)
	}

	m, err := Load(context.Background(), db)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}

	// Then the library edges survive from both products, the stray one is
	// dropped, and the library stands as a project of one marked as such.
	for _, p := range []string{"shop", "billing"} {
		pr, _ := m.Project(p)
		if got := pr.Members[0].Uses; len(got) != 1 || got[0] != "acme-commons" {
			t.Errorf("%s member Uses = %v, want [acme-commons] only", p, got)
		}
	}
	lib, ok := m.Project("acme-commons")
	if !ok || len(lib.Members) != 1 || !lib.Members[0].Library {
		t.Errorf("Project(acme-commons) = %+v, %v, want a project of one marked Library", lib, ok)
	}
	if m.Of("acme-commons") != "acme-commons" {
		t.Errorf("Of(acme-commons) = %q, want itself — a library belongs to no product", m.Of("acme-commons"))
	}
}

func TestLoad_aParkedLibraryIsNoTarget(t *testing.T) {
	db := seed(t,
		[][4]string{
			{"shop-backend", "shop", "backend", ""},
			{"acme-commons", "acme-commons", "", ""},
		},
		[][2]string{{"shop-backend", "acme-commons"}},
	)
	if _, err := db.Exec(`UPDATE repo_state SET library = 1, enabled = 0 WHERE name = 'acme-commons'`); err != nil {
		t.Fatal(err)
	}

	m, err := Load(context.Background(), db)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	pr, _ := m.Project("shop")
	if got := pr.Members[0].Uses; len(got) != 0 {
		t.Errorf("shop-backend.Uses = %v, want none — a parked library answers nothing and is not drawn", got)
	}
}
