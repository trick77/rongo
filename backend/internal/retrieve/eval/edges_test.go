package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/edges"
)

// edgeCase is one boundary the corpus is known to contain, taken from
// docs/measurements/2026-09-10-flow-corpus.md. Each was read in the pinned
// source before it was written down here.
type edgeCase struct {
	name     string
	fromRepo string
	fromPath string
	wantRepo string
	wantPath string
	wantVal  string
}

// TestFlowEdgesCrossRepositories is the acceptance test the edge table was
// built for: from a file, reach the far side of the boundary it names, in
// another repository, with no symbol, import or type in common.
//
// It needs the indexed flow corpus, so it is gated like every other eval test.
func TestFlowEdgesCrossRepositories(t *testing.T) {
	requireEval(t)
	db := evalDB(t, embedDim(t))
	ctx := context.Background()

	cases := []edgeCase{
		{
			name:     "queue producer reaches its consumer",
			fromRepo: "shipping",
			fromPath: "src/main/java/works/weave/socks/shipping/controllers/ShippingController.java",
			wantRepo: "queue-master",
			wantVal:  "shipping-task",
		},
		{
			name:     "queue consumer reaches its producer",
			fromRepo: "queue-master",
			fromPath: "src/main/java/works/weave/socks/queuemaster/configuration/ShippingConsumerConfiguration.java",
			wantRepo: "shipping",
			wantVal:  "shipping-task",
		},
		{
			name:     "a Java client reaches the Go service it calls",
			fromRepo: "orders",
			fromPath: "src/main/java/works/weave/socks/orders/config/OrdersConfigurationProperties.java",
			wantRepo: "payment",
			wantPath: "transport.go",
			wantVal:  "/paymentAuth",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := edges.Neighbours(ctx, db, tc.fromRepo, tc.fromPath)
			if err != nil {
				t.Fatalf("Neighbours: %v", err)
			}
			for _, n := range got {
				if n.Repo != tc.wantRepo || n.Value != tc.wantVal {
					continue
				}
				if tc.wantPath != "" && !strings.HasSuffix(n.Path, tc.wantPath) {
					continue
				}
				t.Logf("reached %s/%s:%d via %s %q", n.Repo, n.Path, n.Line, n.Kind, n.Value)
				return
			}
			t.Errorf("no edge from %s/%s to %s on %q; got %d neighbours: %v",
				tc.fromRepo, tc.fromPath, tc.wantRepo, tc.wantVal, len(got), got)
		})
	}
}

// TestFlowEdgesDoNotConnectEverything is the other half: an edge table that
// links every repository to every other is worse than none, because retrieval
// would follow it into unrelated code and the answer would cite what it found.
func TestFlowEdgesDoNotConnectEverything(t *testing.T) {
	requireEval(t)
	db := evalDB(t, embedDim(t))
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `
		SELECT t.kind, t.value, COUNT(DISTINCT f.repo) AS repos
		FROM integration_tokens t JOIN files f ON f.id = t.file_id
		GROUP BY t.kind, t.value
		HAVING repos > 1
		ORDER BY repos DESC, t.value
		LIMIT 30`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	linked := 0
	for rows.Next() {
		var kind, value string
		var repos int
		if err := rows.Scan(&kind, &value, &repos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		linked++
		t.Logf("%-12s %-40s %d repositories", kind, value, repos)
		if repos > 4 {
			t.Errorf("%q spans %d repositories, which is a convention rather than an edge", value, repos)
		}
	}
	if linked == 0 {
		t.Error("no token is shared by two repositories, so the table links nothing")
	}
}
