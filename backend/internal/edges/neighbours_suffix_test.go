package edges

import (
	"context"
	"testing"
)

// TestNeighboursMatchesAClientSuffixAgainstAServedPath: the front-end writes
// endpoints.cartsUrl + "/" + custId + "/merge"; the server serves
// /carts/{customerId}/merge. The two never spell the same string, and only
// the tail is shared. With suffix matching on, the tail is enough; with it
// off, the edge does not exist — which is what the flow corpus measures.
func TestNeighboursMatchesAClientSuffixAgainstAServedPath(t *testing.T) {
	db := edgeDB(t, "front-end", "carts")
	seedFileWithTokens(t, db, "front-end", "api/user/index.js", "merge", nil,
		[]Token{{Kind: KindRoute, Value: "/merge", Line: 283}})
	seedFileWithTokens(t, db, "carts", "CartsController.java", "serve", nil,
		[]Token{{Kind: KindRoute, Value: "/carts/{customerId}/merge", Line: 35}})
	// A destination sharing the same tail is a different namespace, never
	// matched; and a path that merely ENDS in the letters, not in the
	// segment, is not the same route.
	seedFileWithTokens(t, db, "carts", "Other.java", "other", nil,
		[]Token{{Kind: KindDestination, Value: "x/merge", Line: 1}, {Kind: KindRoute, Value: "/emerge", Line: 2}})

	exact, err := Neighbours(context.Background(), db, "front-end", "api/user/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(exact) != 0 {
		t.Errorf("exact matching found %+v, want nothing", exact)
	}
	got, err := NeighboursWith(context.Background(), db, "front-end", "api/user/index.js", Match{Suffix: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "CartsController.java" || got[0].Value != "/carts/{customerId}/merge" {
		t.Errorf("suffix matching found %+v, want the served path alone", got)
	}
	// And from the server's side the same edge, the other way round.
	back, err := NeighboursWith(context.Background(), db, "carts", "CartsController.java", Match{Suffix: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Repo != "front-end" {
		t.Errorf("from the server side: %+v, want the client", back)
	}
}
