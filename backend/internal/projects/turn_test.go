package projects

import (
	"context"
	"testing"
)

func TestLoad_readsTheGroupingOncePerTurn(t *testing.T) {
	// Given a turn that has read the grouping, and a repos.yaml edit landing
	// while it runs
	db := shopDB(t)
	turn := PerTurn(context.Background())
	if _, err := Load(turn, db); err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if _, err := db.Exec(`UPDATE repo_state SET project = 'crm' WHERE name = 'legacy-crm'`); err != nil {
		t.Fatalf("rename project: %v", err)
	}

	// When the same turn asks again, and the next turn asks
	same, err := Load(turn, db)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	next, err := Load(PerTurn(context.Background()), db)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}

	// Then the turn keeps the grouping it started with — routing, search and
	// the prompt agree with each other — and the edit reaches the next one
	if got := same.Of("legacy-crm"); got != "legacy-crm" {
		t.Errorf("same turn: project = %q, want the one it first read", got)
	}
	if got := next.Of("legacy-crm"); got != "crm" {
		t.Errorf("next turn: project = %q, want the renamed one", got)
	}
}

func TestLoad_outsideATurnReadsEveryTime(t *testing.T) {
	// Given no turn on the context: the Repos page, a test, a boot check
	db := shopDB(t)
	ctx := context.Background()
	if _, err := Load(ctx, db); err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if _, err := db.Exec(`UPDATE repo_state SET project = 'crm' WHERE name = 'legacy-crm'`); err != nil {
		t.Fatalf("rename project: %v", err)
	}

	// When
	m, err := Load(ctx, db)

	// Then
	if err != nil || m.Of("legacy-crm") != "crm" {
		t.Errorf("project = %q, err %v; want the edit seen at once", m.Of("legacy-crm"), err)
	}
}

func TestLoad_aFailedReadIsNotKeptForTheTurn(t *testing.T) {
	// Given a turn whose first read is cancelled
	db := shopDB(t)
	turn := PerTurn(context.Background())
	gone, cancel := context.WithCancel(turn)
	cancel()
	if _, err := Load(gone, db); err == nil {
		t.Fatal("Load() on a cancelled context succeeded")
	}

	// When the turn reads again
	m, err := Load(turn, db)

	// Then it gets the grouping, not the failure and not an empty map
	if err != nil || m.Of("shop-ui") != "shop" {
		t.Errorf("project = %q, err %v; want the grouping read afresh", m.Of("shop-ui"), err)
	}
}
