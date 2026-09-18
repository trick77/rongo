package memory

import (
	"context"
	"strings"
	"testing"
)

func TestScope_aProductRuleDoesNotLeakThroughASharedLibrary(t *testing.T) {
	// shop and billing are both built on acme-commons. A rule scoped to
	// shop must fire on a shop turn and never on a billing turn, even though
	// both turns carry the library in Known.
	db := testDB(t)
	for _, r := range [][2]string{{"billing-api", "billing"}, {"acme-commons", "acme-commons"}} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, project) VALUES (?, ?, ?)`,
			r[0], "https://x/"+r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = 'acme-commons'`); err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"shop-backend", "acme-commons"}, {"billing-api", "acme-commons"}} {
		if _, err := db.Exec(`INSERT INTO repo_uses (repo, uses) VALUES (?, ?)`, e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.Add(ctx, "jan", Directive{Text: "Ignore the legacy module.", Scope: "shop"}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.Add(ctx, "jan", Directive{Text: "Prefer the v2 client.", Scope: "acme-commons"}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}
	rows, _ := s.List(ctx, "jan")
	byText := map[string]Row{}
	for _, r := range rows {
		byText[r.Text] = r
	}

	shop := byText["Ignore the legacy module."]
	if strings.Join(shop.members, ",") != "shop-backend,shop-ui" {
		t.Fatalf("shop members = %v, want the product's own repositories, not its library", shop.members)
	}
	if !Applies(shop, []string{"acme-commons", "shop-backend", "shop-ui"}) {
		t.Error("a shop rule must apply to a shop turn")
	}
	if Applies(shop, []string{"acme-commons", "billing-api"}) {
		t.Error("a shop rule must not apply to a billing turn through the shared library")
	}
	if Applies(shop, []string{"acme-commons"}) {
		t.Error("a shop rule must not apply to a turn about the library alone")
	}

	lib := byText["Prefer the v2 client."]
	if strings.Join(lib.members, ",") != "acme-commons" || !lib.ScopeLive {
		t.Fatalf("library scope = %+v, want the library alone", lib)
	}
	if !Applies(lib, []string{"acme-commons", "billing-api"}) {
		t.Error("a library rule applies wherever the library is searched")
	}
}
