package retrieve

import (
	"context"
	"testing"
)

func TestResolveReposExpandsAProjectNameToItsMembersAndTheLibraryTheyUse(t *testing.T) {
	// "How does checkout work in Shop?" over a product built on a shared
	// library: the product is searched with the code it is built on, so the
	// library is in the restriction — and later in the pin, where naming it
	// narrows instead of being reported as outside the thread.
	db := testDB(t)
	addMember(t, db, "shop-backend", "shop")
	addMember(t, db, "shop-ui", "shop")
	addMember(t, db, "billing-api", "billing")
	addMember(t, db, "legacy-crm", "legacy-crm")
	addMember(t, db, "acme-commons", "acme-commons")
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = 'acme-commons'`); err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"shop-backend", "acme-commons"}, {"billing-api", "acme-commons"}} {
		if _, err := db.Exec(`INSERT INTO repo_uses (repo, uses) VALUES (?, ?)`, e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	r := New(db, nil)

	known, unknown, err := r.ResolveRepos(context.Background(), []string{"shop"}, "")
	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 3 || !contains(known, "shop-backend") || !contains(known, "shop-ui") || !contains(known, "acme-commons") {
		t.Errorf("known = %v, want shop's repositories and the library", known)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v", unknown)
	}

	// A product that does not use it gets nothing extra.
	known, _, err = r.ResolveRepos(context.Background(), []string{"legacy-crm"}, "")
	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 1 || known[0] != "legacy-crm" {
		t.Errorf("known = %v, want legacy-crm alone", known)
	}

	// The library by its own name is one repository, as ever.
	known, _, err = r.ResolveRepos(context.Background(), []string{"acme-commons"}, "")
	if err != nil {
		t.Fatalf("ResolveRepos: %v", err)
	}
	if len(known) != 1 || known[0] != "acme-commons" {
		t.Errorf("known = %v, want the library alone", known)
	}
}
