package edges

import (
	"context"
	"testing"
)

func TestHoldersFindsEveryFileCarryingTheValue(t *testing.T) {
	// Given: a route named in two repositories, and an unrelated route beside
	// it. Holders is asked by VALUE — nobody hands it a near side — so both
	// files carrying the asked route are answers and the other one is not.
	db := edgeDB(t, "orders", "payment")
	seedFileWithTokens(t, db, "orders", "Config.java", "config", nil,
		[]Token{{Kind: KindRoute, Value: "/paymentAuth", Line: 5}})
	seedFileWithTokens(t, db, "payment", "transport.go", "transport", nil,
		[]Token{{Kind: KindRoute, Value: "/paymentAuth", Line: 3}})
	seedFileWithTokens(t, db, "payment", "other.go", "other", nil,
		[]Token{{Kind: KindRoute, Value: "/health", Line: 1}})

	// When
	got, err := Holders(context.Background(), db, KindRoute, "/paymentAuth")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then: repo, path, line order.
	if len(got) != 2 {
		t.Fatalf("holders = %+v, want the two files naming the route", got)
	}
	if got[0].Repo != "orders" || got[0].Path != "Config.java" || got[0].Line != 5 {
		t.Errorf("first holder = %+v", got[0])
	}
	if got[1].Repo != "payment" || got[1].Path != "transport.go" || got[1].Kind != KindRoute {
		t.Errorf("second holder = %+v", got[1])
	}
}

func TestHoldersMatchesExactlyAndNeverBySuffix(t *testing.T) {
	// Given: a longer route ending in the asked one. The gap pass resolves a
	// name the model spelled; a loose match would land it on whatever route
	// happens to end the same way, and suffix matching is measured OFF.
	db := edgeDB(t, "shop")
	seedFileWithTokens(t, db, "shop", "Carts.java", "carts", nil,
		[]Token{{Kind: KindRoute, Value: "/carts/merge", Line: 1}})

	// When
	got, err := Holders(context.Background(), db, KindRoute, "/merge")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("holders = %+v, want none: /merge is not /carts/merge", got)
	}
}

func TestHoldersIgnoresAParkedRepository(t *testing.T) {
	// Given: the only file carrying the value sits in a repository the
	// operator parked. Parked code answers nothing, here as everywhere.
	db := edgeDB(t, "live", "parked")
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'parked'`); err != nil {
		t.Fatalf("park: %v", err)
	}
	seedFileWithTokens(t, db, "parked", "Consumer.java", "receive", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 1}})

	// When
	got, err := Holders(context.Background(), db, KindDestination, "shipping-task")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("a parked repository answered: %+v", got)
	}
}

func TestHoldersDropsAValueSpreadPastTheCeiling(t *testing.T) {
	// Given: a value in four live repositories. Past the ceiling it is a
	// convention rather than a link, and the same rule has to hold whichever
	// half of the table is keyed — otherwise the gap pass would land on every
	// service's "/login".
	db := edgeDB(t, "a", "b", "c", "d")
	for _, r := range []string{"a", "b", "c", "d"} {
		seedFileWithTokens(t, db, r, "X.java", "x", nil,
			[]Token{{Kind: KindRoute, Value: "/login", Line: 1}})
	}

	// When
	got, err := Holders(context.Background(), db, KindRoute, "/login")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("holders = %+v, want none past the spread ceiling", got)
	}
}

func TestHoldersCountsOnlyEnabledRepositoriesTowardsTheCeiling(t *testing.T) {
	// Given: three live repositories and one parked, all naming the value.
	// Counting the parked one would push it over the ceiling and cost the
	// live three the landing they should have.
	db := edgeDB(t, "a", "b", "c", "parked")
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'parked'`); err != nil {
		t.Fatalf("park: %v", err)
	}
	for _, r := range []string{"a", "b", "c", "parked"} {
		seedFileWithTokens(t, db, r, "X.java", "x", nil,
			[]Token{{Kind: KindDestination, Value: "orders", Line: 1}})
	}

	// When
	got, err := Holders(context.Background(), db, KindDestination, "orders")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then
	if len(got) != 3 {
		t.Fatalf("holders = %+v, want the three live ones", got)
	}
}
