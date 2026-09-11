package modules

import (
	"context"
	"testing"
)

// TestCluster_cutsOnDeclaredUnitsBeforeDirectories: an nx workspace declares
// apps/claims and libs/shared. Those are the modules a person can name, so
// the cut follows the manifest wherever one exists, and the directory rule
// only for what no unit claims. Small units are not folded away: a library
// of two chunks is still the library.
func TestCluster_cutsOnDeclaredUnitsBeforeDirectories(t *testing.T) {
	db := clusterDB(t)
	for _, u := range [][3]string{{"apps/claims", "nx-app", "claims"}, {"libs/shared", "nx-lib", "shared"}} {
		if _, err := db.Exec(`INSERT INTO units (repo, key, kind, name) VALUES ('peeq', ?, ?, ?)`, u[0], u[1], u[2]); err != nil {
			t.Fatal(err)
		}
	}
	seedFile(t, db, "peeq", "apps/claims/src/app/a.ts", 5, "")
	seedFile(t, db, "peeq", "apps/claims/src/app/deep/b.ts", 5, "")
	seedFile(t, db, "peeq", "libs/shared/src/index.ts", 2, "")
	seedFile(t, db, "peeq", "tools/gen.ts", 12, "")
	seedFile(t, db, "peeq", "tools/more.ts", 12, "")

	got, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 8, MaxChunks: 150})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	claims := find(t, got, "apps/claims")
	if claims.Name != "claims" || len(claims.Paths) != 2 {
		t.Errorf("claims = %+v, want the unit's name and both of its files", claims)
	}
	shared := find(t, got, "libs/shared")
	if shared.Name != "shared" || shared.ChunkCount != 2 {
		t.Errorf("shared = %+v, want the two-chunk library kept whole", shared)
	}
	tools := find(t, got, "tools")
	if tools.Name != "" || len(tools.Paths) != 2 {
		t.Errorf("tools = %+v, want the directory rule for files no unit claims", tools)
	}
	if len(got) != 3 {
		t.Errorf("modules = %v, want exactly three", keys(got))
	}
}
