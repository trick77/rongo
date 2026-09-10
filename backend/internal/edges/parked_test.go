package edges

import (
	"context"
	"testing"
)

func TestNeighboursNeverCrossesIntoAParkedRepository(t *testing.T) {
	// Given: the far side of a real edge, in a repository the operator has
	// disabled. Its files stay in the index; that is not permission to cite
	// them.
	db := edgeDB(t, "shipping", "queue-master")
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'queue-master'`); err != nil {
		t.Fatalf("park queue-master: %v", err)
	}
	seedFileWithTokens(t, db, "shipping", "Controller.java", "send", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 1}})
	seedFileWithTokens(t, db, "queue-master", "Consumer.java", "receive", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 1}})

	// When
	got, err := Neighbours(context.Background(), db, "shipping", "Controller.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("a parked repository was used as a hop target: %+v", got)
	}
}

func TestParkedRepositoriesDoNotConsumeTheSpreadCeiling(t *testing.T) {
	// Given: a token in two live repositories and two parked ones. Counting the
	// parked pair would push it over the ceiling and cost the live pair the
	// edge they should have — a parked repository must influence nothing.
	db := edgeDB(t, "live-a", "live-b", "parked-a", "parked-b")
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name LIKE 'parked-%'`); err != nil {
		t.Fatalf("park: %v", err)
	}
	for _, r := range []string{"live-a", "live-b", "parked-a", "parked-b"} {
		seedFileWithTokens(t, db, r, "X.java", "x", nil,
			[]Token{{Kind: KindRoute, Value: "/shared", Line: 1}})
	}

	// When
	got, err := Neighbours(context.Background(), db, "live-a", "X.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then: the live sibling, and only it.
	if len(got) != 1 || got[0].Repo != "live-b" {
		t.Fatalf("expected only live-b, got %+v", got)
	}
}

func TestInRepoHopMatchesWholeIdentifiersOnly(t *testing.T) {
	// Given: a start file mentioning ItemsController, and a sibling defining
	// the shorter name Item. A substring rule linked the two; ask's rule does
	// not, and this walk claims to be ask's rule.
	db := edgeDB(t, "shop", "other")
	seedFileWithTokens(t, db, "shop", "Start.java",
		"class Start { ItemsController controller; }", []string{"Start"},
		[]Token{{Kind: KindRoute, Value: "/start", Line: 1}})
	seedFileWithTokens(t, db, "shop", "Item.java", "class Item { }", []string{"Item"}, nil)
	seedFileWithTokens(t, db, "other", "Far.java", "far", nil,
		[]Token{{Kind: KindRoute, Value: "/start", Line: 1}})

	// When
	got, err := Reach(context.Background(), db, "shop", "Start.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then
	if s := stepOf(got, "shop", "Item.java"); s != "" {
		t.Errorf("Item.java was reached as %q on a substring match", s)
	}
}

func TestInRepoHopStillMatchesTheWholeName(t *testing.T) {
	// Given: the same shape, with the name actually mentioned.
	db := edgeDB(t, "shop")
	seedFileWithTokens(t, db, "shop", "Start.java",
		"class Start { ItemsController controller; }", []string{"Start"}, nil)
	seedFileWithTokens(t, db, "shop", "ItemsController.java",
		"class ItemsController { }", []string{"ItemsController"}, nil)

	// When
	got, err := Reach(context.Background(), db, "shop", "Start.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then
	if s := stepOf(got, "shop", "ItemsController.java"); s != StepInRepoBefore {
		t.Fatalf("the named class was reached as %q, want %q", s, StepInRepoBefore)
	}
}
