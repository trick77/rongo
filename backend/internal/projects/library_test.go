package projects

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// libraryDB is a library two products use, a stray edge between the two
// products (which repos.Load refuses and only a hand-written row can carry),
// and a third product that does not use the library.
func libraryDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seed(t,
		[][4]string{
			{"shop-backend", "shop", "backend", ""},
			{"shop-ui", "shop", "ui", ""},
			{"billing-api", "billing", "backend", ""},
			{"legacy-crm", "legacy-crm", "", ""},
			{"acme-commons", "acme-commons", "", "Shared utilities."},
		},
		[][2]string{
			{"shop-ui", "shop-backend"},
			{"shop-backend", "acme-commons"},
			{"billing-api", "acme-commons"},
			{"shop-backend", "billing-api"},
		},
	)
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = 'acme-commons'`); err != nil {
		t.Fatal(err)
	}
	return db
}

func member(t *testing.T, m Map, project, name string) Repo {
	t.Helper()
	p, ok := m.Project(project)
	if !ok {
		t.Fatalf("Project(%s) not found", project)
	}
	for _, r := range p.Members {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("%s is not a member of %s: %v", name, project, m.Members(project))
	return Repo{}
}

func TestLoad_aLibraryIsAMemberOfEveryProductUsingIt(t *testing.T) {
	m, err := Load(context.Background(), libraryDB(t))
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}

	// The edge survives from both products, the stray one is dropped.
	if got := member(t, m, "shop", "shop-backend").Uses; !reflect.DeepEqual(got, []string{"acme-commons"}) {
		t.Errorf("shop-backend.Uses = %v, want [acme-commons] only", got)
	}
	if got := member(t, m, "billing", "billing-api").Uses; !reflect.DeepEqual(got, []string{"acme-commons"}) {
		t.Errorf("billing-api.Uses = %v, want [acme-commons]", got)
	}
	// The library is a member of both products, marked, and of nothing else.
	if got := m.Members("shop"); !reflect.DeepEqual(got, []string{"acme-commons", "shop-backend", "shop-ui"}) {
		t.Errorf("Members(shop) = %v, want the library among them", got)
	}
	if got := m.Members("billing"); !reflect.DeepEqual(got, []string{"acme-commons", "billing-api"}) {
		t.Errorf("Members(billing) = %v", got)
	}
	if got := m.Members("legacy-crm"); !reflect.DeepEqual(got, []string{"legacy-crm"}) {
		t.Errorf("Members(legacy-crm) = %v, want no library — nothing there uses it", got)
	}
	if !member(t, m, "shop", "acme-commons").Library {
		t.Error("the library is not marked Library inside shop")
	}
	if got := m.UsedBy("acme-commons"); !reflect.DeepEqual(got, []string{"billing", "shop"}) {
		t.Errorf("UsedBy = %v", got)
	}
	// Its own project of one still stands, and Of still answers itself: which
	// product a library hit belongs to is a per-turn question.
	if lib, ok := m.Project("acme-commons"); !ok || len(lib.Members) != 1 || !lib.Members[0].Library {
		t.Errorf("Project(acme-commons) = %+v, %v, want a project of one", lib, ok)
	}
	if m.Of("acme-commons") != "acme-commons" || !m.IsLibrary("acme-commons") {
		t.Errorf("Of/IsLibrary(acme-commons) = %q, %v", m.Of("acme-commons"), m.IsLibrary("acme-commons"))
	}
}

func TestLoad_followsLibraryToLibraryEdges(t *testing.T) {
	db := seed(t,
		[][4]string{
			{"shop-backend", "shop", "backend", ""},
			{"acme-http", "acme-http", "", ""},
			{"acme-commons", "acme-commons", "", ""},
		},
		[][2]string{{"shop-backend", "acme-http"}, {"acme-http", "acme-commons"}},
	)
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name IN ('acme-http', 'acme-commons')`); err != nil {
		t.Fatal(err)
	}
	m, err := Load(context.Background(), db)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if got := m.Members("shop"); !reflect.DeepEqual(got, []string{"acme-commons", "acme-http", "shop-backend"}) {
		t.Errorf("Members(shop) = %v, want both libraries — the backend is built on what its library is built on", got)
	}
}

func TestFold_aLibraryBesideAProductUsingItIsThatProducts(t *testing.T) {
	m, _ := Load(context.Background(), libraryDB(t))

	// Beside shop: shop's. Beside both products: on both. Alone, or beside
	// a product that does not use it: a project of one.
	if got := m.Distinct([]string{"shop-backend", "acme-commons"}); got != 1 {
		t.Errorf("Distinct(shop-backend, acme-commons) = %d, want 1", got)
	}
	if got := m.Fold([]string{"shop-ui", "acme-commons", "billing-api"}); !reflect.DeepEqual(got, map[string][]string{
		"shop": {"shop-ui", "acme-commons"}, "billing": {"billing-api", "acme-commons"},
	}) {
		t.Errorf("Fold = %v", got)
	}
	if got := m.Distinct([]string{"acme-commons"}); got != 1 {
		t.Errorf("Distinct(acme-commons) = %d, want 1", got)
	}
	if got := m.Fold([]string{"legacy-crm", "acme-commons"}); !reflect.DeepEqual(got, map[string][]string{
		"legacy-crm": {"legacy-crm"}, "acme-commons": {"acme-commons"},
	}) {
		t.Errorf("Fold(legacy-crm, acme-commons) = %v, want two projects", got)
	}
}

func TestCovered_aProductIsCoveredByItsOwnMembersWithOrWithoutTheLibrary(t *testing.T) {
	m, _ := Load(context.Background(), libraryDB(t))

	if got := m.Covered([]string{"shop-backend", "shop-ui"}); !reflect.DeepEqual(got, []string{"shop"}) {
		t.Errorf("Covered(members only) = %v, want [shop] — a card from before the library stored these", got)
	}
	// With the library: still one product, not a product and a library to
	// compare — the library's own project of one steps aside.
	if got := m.Covered([]string{"acme-commons", "shop-backend", "shop-ui"}); !reflect.DeepEqual(got, []string{"shop"}) {
		t.Errorf("Covered(members and library) = %v, want shop alone", got)
	}
	// One member and the library: a partial cover of shop counts for
	// nothing, and the library steps aside for the product beside it — so
	// nothing is covered, exactly as a partial card resumed before the
	// library existed. Never "a turn about the library compared with
	// shop-backend".
	if got := m.Covered([]string{"shop-backend", "acme-commons"}); len(got) != 0 {
		t.Errorf("Covered(one member and the library) = %v, want nothing", got)
	}
	// Beside a product that does not use it, the library's own project stands.
	if got := m.Covered([]string{"legacy-crm", "acme-commons"}); !reflect.DeepEqual(got, []string{"acme-commons", "legacy-crm"}) {
		t.Errorf("Covered(legacy-crm, acme-commons) = %v", got)
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
	if got := member(t, m, "shop", "shop-backend").Uses; len(got) != 0 {
		t.Errorf("shop-backend.Uses = %v, want none — a parked library answers nothing and is not drawn", got)
	}
	if got := m.Members("shop"); !reflect.DeepEqual(got, []string{"shop-backend"}) {
		t.Errorf("Members(shop) = %v, want no parked library", got)
	}
}
