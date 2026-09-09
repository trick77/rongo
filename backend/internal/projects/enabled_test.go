package projects

import (
	"context"
	"database/sql"
	"testing"
)

func park(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = ?`, name); err != nil {
		t.Fatalf("park %s: %v", name, err)
	}
}

// TestLoad_parkedRepoIsNotAMember: a card that offered it would ask the reader
// to choose a product answering nothing, and the members a chosen project
// searches must be the ones that can actually be searched.
func TestLoad_parkedRepoIsNotAMember(t *testing.T) {
	// Given
	db := seed(t, [][4]string{
		{"shop-ui", "shop", "ui", "Storefront."},
		{"shop-events", "shop", "consumer", "Kafka consumer."},
	}, nil)
	park(t, db, "shop-events")

	// When
	m, err := Load(context.Background(), db)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	got := m.Members("shop")
	if len(got) != 1 || got[0] != "shop-ui" {
		t.Errorf("Members(shop) = %v, want only shop-ui", got)
	}
	// Of never answers empty by design, so a parked repository falls back to
	// standing for itself — the same answer any name the map has not heard of
	// gets. What matters is that it is no longer grouped under shop, where it
	// would still be reachable by naming the product.
	if p := m.Of("shop-events"); p == "shop" {
		t.Error("Of(shop-events) = shop, want it out of the project entirely")
	}
}

// TestLoad_aWhollyParkedProjectDisappears: parking every member is how a whole
// product is retired, and a project name with nothing behind it is not an
// option to put in front of anybody.
func TestLoad_aWhollyParkedProjectDisappears(t *testing.T) {
	// Given
	db := seed(t, [][4]string{
		{"legacy-crm-api", "legacy-crm", "backend", "Retired."},
		{"legacy-crm-ui", "legacy-crm", "ui", "Retired."},
		{"shop-ui", "shop", "ui", "Storefront."},
	}, nil)
	park(t, db, "legacy-crm-api")
	park(t, db, "legacy-crm-ui")

	// When
	m, err := Load(context.Background(), db)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if got := m.Members("legacy-crm"); len(got) != 0 {
		t.Errorf("Members(legacy-crm) = %v, want none", got)
	}
	if got := m.Members("shop"); len(got) != 1 {
		t.Errorf("Members(shop) = %v, want the live project untouched", got)
	}
}
