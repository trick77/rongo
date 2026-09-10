package eval

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/edges"
)

// TestFlowComposedWalkReachesTheFileThatMatters is the fix for what
// TestFlowEdgeReach exposed: an edge lands on the file holding the literal, and
// that is rarely the file an answer needs. Reach takes one step inland at each
// end, which is what turns a crossing into a usable hop.
func TestFlowComposedWalkReachesTheFileThatMatters(t *testing.T) {
	requireEval(t)
	db := evalDB(t, embedDim(t))
	ctx := context.Background()

	// Given: the controller an answer about order placement actually needs. It
	// carries no route literal of its own — it asks its configuration class for
	// the payment URI.
	fromRepo := "orders"
	fromPath := "src/main/java/works/weave/socks/orders/controllers/OrdersController.java"

	// When
	got, err := edges.Reach(ctx, db, fromRepo, fromPath)
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then: the payment service's own logic is reached, in another repository
	// and another language, across a literal the start file never mentions.
	want := map[string]bool{"payment/service.go": false, "payment/transport.go": false}
	for _, r := range got {
		key := r.Repo + "/" + r.Path
		if _, ok := want[key]; ok {
			want[key] = true
			t.Logf("reached %s by %s (via %q, through %s)", key, r.Step, r.Via, r.Through)
		}
	}
	for key, ok := range want {
		if !ok {
			t.Errorf("Reach did not arrive at %s from %s/%s", key, fromRepo, fromPath)
		}
	}
}
