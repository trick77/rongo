package ask

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// gapLLM is an upstream that answers the gap call with reply and records the
// user message it was sent. Routed by prompt content the way the reranker's
// fake is: a system message asking for "missing" is the gap call, and
// anything else is a test wiring two steps to one server.
func gapLLM(t *testing.T, reply string, saw *string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := reply
		if len(req.Messages) == 0 || !strings.Contains(req.Messages[0].Content, "missing") {
			out = "not the gap call"
		} else if saw != nil && len(req.Messages) > 1 {
			*saw = req.Messages[1].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": out}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

// gapLLMFailing is an upstream that refuses every call.
func gapLLMFailing(t *testing.T) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

// warnings collects the warnings a Gatherer logs, so a test can read what the
// gap pass said when it changed nothing.
type warnings struct {
	msgs []string
}

func (w *warnings) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnings) Handle(_ context.Context, r slog.Record) error {
	w.msgs = append(w.msgs, r.Message)
	return nil
}
func (w *warnings) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnings) WithGroup(string) slog.Handler      { return w }

// sourceOf reads a seeded chunk back as a gathered source, so a test can hand
// FillGaps the sources a walk would have produced without running the walk.
func sourceOf(t *testing.T, db *sql.DB, id int64) Source {
	t.Helper()
	h := hitInFor(t, db, id)
	return Source{
		ChunkID: h.ChunkID, Repo: h.Repo, Branch: h.Branch, Path: h.Path, Symbol: h.Symbol,
		StartLine: h.StartLine, EndLine: h.EndLine, SHA: h.SHA, Text: h.RawText, Reason: "hit",
	}
}

func gapGatherer(t *testing.T, db *sql.DB, o GatherOptions, reply string) *Gatherer {
	t.Helper()
	return NewGatherer(db, o).WithGapPass(gapLLM(t, reply, nil))
}

func missing(names ...string) string {
	return `{"missing":[` + strings.Join(names, ",") + `]}`
}

func name(n, kind string) string { return `{"name":"` + n + `","kind":"` + kind + `"}` }

// TestFillGaps_landsASymbolTheSourcesOnlyCall is the pass in one line: the
// gathered code calls unitPrice, nothing among the sources defines it, and
// the definition is fetched by name from the symbols table — deterministically,
// with no second model call.
func TestFillGaps_landsASymbolTheSourcesOnlyCall(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() int { return unitPrice() * n }")
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { return 3 }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("unitPrice", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	s, ok := sourceIn(got, "peeq", "price.go")
	if !ok {
		t.Fatalf("sources = %v, want the definition the gap pass asked for", repoPaths(got))
	}
	if s.Reason != "gap:unitPrice" {
		t.Errorf("reason = %q, want the name the pass looked up", s.Reason)
	}
	if s.Hop != 2 {
		t.Errorf("hop = %d, want MaxHops+1", s.Hop)
	}
	if report.Skipped != "" || len(report.Landed) != 1 || report.Landed[0] != "unitPrice" {
		t.Errorf("report = %+v, want one landing and nothing skipped", report)
	}
	if len(report.Asked) != 1 || report.Asked[0].Kind != "symbol" {
		t.Errorf("asked = %+v", report.Asked)
	}
}

// TestFillGaps_resolvesASymbolInAGatheredRepositoryFirst is
// TestGather_resolvesASymbolInItsOwnRepositoryFirst for the pass: peeq and
// loom define randomToken byte for byte, and a name looked up over the whole
// corpus must land in the repository the answer is already being written
// about, or the citation opens the other product.
func TestFillGaps_resolvesASymbolInAGatheredRepositoryFirst(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/auth/grant.go", 0, 1, 20, "issueGrant",
		"func issueGrant() string { return randomToken(32) }")
	seedChunkIn(t, db, "peeq", "backend/internal/auth/session.go", 0, 135, 141, "randomToken",
		"func randomToken(n int) string { return \"\" }")
	seedSymbolIn(t, db, "peeq", "backend/internal/auth/session.go", "randomToken", 135)
	// loom's identical copy, whose path sorts first.
	seedChunkIn(t, db, "loom", "backend/internal/auth/session.go", 0, 129, 141, "randomToken",
		"func randomToken(n int) string { return \"\" }")
	seedSymbolIn(t, db, "loom", "backend/internal/auth/session.go", "randomToken", 129)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("randomToken", "symbol")))

	got, _, err := g.FillGaps(context.Background(), "how is a grant issued?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !hasIn(got, "peeq", "backend/internal/auth/session.go") {
		t.Errorf("sources = %v, want peeq's own definition", repoPaths(got))
	}
	if hasIn(got, "loom", "backend/internal/auth/session.go") {
		t.Errorf("sources = %v, want no citation into loom", repoPaths(got))
	}
}

// TestFillGaps_stillCrossesForASymbolNoGatheredRepositoryDefines keeps the
// other half of the rule: composition over a real dependency is the reason
// the lookup reaches past the gathered repositories at all.
func TestFillGaps_stillCrossesForASymbolNoGatheredRepositoryDefines(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "go-sqlite3")
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/store/store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return ZeroBlob(p) }")
	seedChunkIn(t, db, "go-sqlite3", "blob.go", 0, 40, 60, "ZeroBlob", "func ZeroBlob(p string) error { return nil }")
	seedSymbolIn(t, db, "go-sqlite3", "blob.go", "ZeroBlob", 40)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("ZeroBlob", "symbol")))

	got, _, err := g.FillGaps(context.Background(), "how does peeq open a store?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !hasIn(got, "go-sqlite3", "blob.go") {
		t.Errorf("sources = %v, want the definition peeq does not have itself", repoPaths(got))
	}
}

// TestFillGaps_landsARouteOnTheChunkHoldingTheLiteral: a route is resolved
// through the edge table, on the chunk at the token's line. The model spelled
// it without the leading slash, which every indexed route carries.
func TestFillGaps_landsARouteOnTheChunkHoldingTheLiteral(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "orders")
	seedRepo(t, db, "payment")
	hitID := seedChunkIn(t, db, "orders", "OrdersController.java", 0, 1, 20, "post",
		"post(config.getPaymentUri())")
	seedChunkIn(t, db, "payment", "transport.go", 0, 1, 20, "imports", "package payment")
	seedChunkIn(t, db, "payment", "transport.go", 1, 21, 40, "MakeHandler",
		`r.Methods("POST").Path("/paymentAuth").Handler(authorise)`)
	seedTokenIn(t, db, "payment", "transport.go", "route", "/paymentAuth", 30)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("paymentAuth", "route")))

	got, report, err := g.FillGaps(context.Background(), "how is a payment authorised?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	s, ok := sourceIn(got, "payment", "transport.go")
	if !ok {
		t.Fatalf("sources = %v, want the handler the route is served by", repoPaths(got))
	}
	if s.StartLine != 21 {
		t.Errorf("landed on lines %d-%d, want the chunk holding the literal", s.StartLine, s.EndLine)
	}
	if s.Reason != "gap:/paymentAuth" {
		t.Errorf("reason = %q, want the route as the index spells it", s.Reason)
	}
	if len(report.Landed) != 1 {
		t.Errorf("report = %+v", report)
	}
}

// TestFillGaps_landsADestinationAndAProperty: the other two token kinds, in
// one reply, resolved the same way and named by the index's own spelling.
func TestFillGaps_landsADestinationAndAProperty(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "queue-master")
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "peeq", "send.go", 0, 1, 10, "send", "func send() { publish(q, s) }")
	seedChunkIn(t, db, "queue-master", "listen.go", 0, 1, 10, "listen", `subscribe("shipping-task")`)
	seedTokenIn(t, db, "queue-master", "listen.go", "destination", "shipping-task", 5)
	seedChunkIn(t, db, "acme-infra", "prod/application.properties", 0, 1, 10, "", "acme.cron.send-digest=0 0 * ? * * *")
	seedTokenIn(t, db, "acme-infra", "prod/application.properties", "property", "acme.cron.send-digest", 1)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000},
		missing(name("shipping-task", "destination"), name("acme.cron.send-digest", "property")))

	got, report, err := g.FillGaps(context.Background(), "what happens after a shipment?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	q, ok := sourceIn(got, "queue-master", "listen.go")
	if !ok || q.Reason != "gap:shipping-task" {
		t.Errorf("sources = %v, want the consumer reached on the queue name", repoPaths(got))
	}
	p, ok := sourceIn(got, "acme-infra", "prod/application.properties")
	if !ok || p.Reason != "gap:acme.cron.send-digest" {
		t.Errorf("sources = %v, want the properties file reached on the key", repoPaths(got))
	}
	if len(report.Landed) != 2 {
		t.Errorf("report = %+v, want both names landed", report)
	}
}

// TestFillGaps_reportsANameTheIndexDoesNotKnowAsUnresolved: a name the
// symbols table and the edge table both miss is a gap this pass cannot fill.
// It is reported, never guessed at by matching the word in raw text: every
// landing carries the INDEX's spelling, so there is no path left that stores
// the model's.
func TestFillGaps_reportsANameTheIndexDoesNotKnowAsUnresolved(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() {}")
	// Three files mentioning the word, none defining it.
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		seedChunk(t, db, p, 0, 1, 10, "", "// the checkout is described here")
	}
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("checkout", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how does checkout work?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want nothing fetched by matching the word", paths(got))
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "checkout" {
		t.Errorf("report = %+v, want the name reported unresolved", report)
	}
}

// TestFillGaps_landsOneChunkPerDefiningFile: a name defined twice in one file
// — an overload, or a chunk window that covers the definition twice — is one
// place, and admitting both spends the reserve on the same file. Same rule as
// the token landings.
func TestFillGaps_landsOneChunkPerDefiningFile(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { Close() }")
	seedChunk(t, db, "a.go", 0, 1, 10, "Close", "func Close() {}")
	seedChunk(t, db, "a.go", 1, 20, 30, "Close", "func Close(n int) {}")
	seedSymbol(t, db, "a.go", "Close", 1)
	seedSymbol(t, db, "a.go", "Close", 25)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("Close", "symbol")))

	got, report, err := g.FillGaps(context.Background(), "how is it closed?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	landed := 0
	for _, s := range got {
		if strings.HasPrefix(s.Reason, "gap:") {
			landed++
		}
	}
	if landed != 1 {
		t.Errorf("landed %d chunks of one file, want one: %v", landed, paths(got))
	}
	if len(report.Landed) != 1 {
		t.Errorf("report = %+v", report)
	}
}

// TestFillGaps_admitsADefiningTestFileLast: a test defining the name is a
// definition and a poor one to spend the last of the reserve on, so the
// landings are ordered the way every other hop's candidates are.
func TestFillGaps_admitsADefiningTestFileLast(t *testing.T) {
	db := gatherDB(t)
	body := "func Close() { " + strings.Repeat("x ", 20) + " }"
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { Close() }")
	// a_test.go sorts FIRST by path, so an unordered admission would spend
	// the room on it and refuse a mechanism file.
	for _, p := range []string{"a_test.go", "b.go", "c.go"} {
		seedChunk(t, db, p, 0, 1, 10, "Close", body)
		seedSymbol(t, db, p, "Close", 1)
	}
	sources := []Source{sourceOf(t, db, hitID)}
	// Room for the hit and exactly two of the three landings.
	budget := estimateTokens(sources[0].Text) + 2*estimateTokens(body)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: budget}, missing(name("Close", "symbol")))

	got, _, err := g.FillGaps(context.Background(), "how is it closed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !has(got, "b.go") || !has(got, "c.go") {
		t.Errorf("sources = %v, want both mechanism files", paths(got))
	}
	if has(got, "a_test.go") {
		t.Errorf("sources = %v, want the test file left to last", paths(got))
	}
}

// TestFillGaps_reportsAHalfAdmittedNameAsRefused: the answer reads a name's
// landings together, and a report calling half of them a landing says the
// definition is in front of the model when part of it is not.
func TestFillGaps_reportsAHalfAdmittedNameAsRefused(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { Close() }")
	seedChunk(t, db, "a.go", 0, 1, 10, "Close", "func Close() {}")
	seedSymbol(t, db, "a.go", "Close", 1)
	seedChunk(t, db, "b.go", 0, 1, 10, "Close", "func Close() { "+strings.Repeat("x ", 400)+" }")
	seedSymbol(t, db, "b.go", "Close", 1)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 120}, missing(name("Close", "symbol")))

	got, report, err := g.FillGaps(context.Background(), "how is it closed?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !has(got, "a.go") || has(got, "b.go") {
		t.Errorf("sources = %v, want the first definer and not the oversized one", paths(got))
	}
	if len(report.Landed) != 0 {
		t.Errorf("landed = %v, want a half-admitted name reported refused", report.Landed)
	}
	if len(report.Refused) != 1 || report.Refused[0] != "Close" {
		t.Errorf("refused = %v, want the name whose landings did not all fit", report.Refused)
	}
}

// TestFillGaps_readsASymbolAndATokenOfTheSameNameApart: "orders" is a queue
// and a method, and a source defining the method says nothing about where the
// queue is served. The kind is part of the name, in the filter and in the
// duplicate check both.
func TestFillGaps_readsASymbolAndATokenOfTheSameNameApart(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "queue-master")
	hitID := seedChunk(t, db, "send.go", 0, 1, 10, "orders", "func orders() { publish(q) }")
	seedChunkIn(t, db, "queue-master", "listen.go", 0, 1, 10, "listen", `subscribe("orders")`)
	seedTokenIn(t, db, "queue-master", "listen.go", "destination", "orders", 5)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000},
		missing(name("orders", "symbol"), name("orders", "destination")))

	got, report, err := g.FillGaps(context.Background(), "what consumes the orders queue?",
		[]Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	// The source's symbol is "orders"; it is the method, not the queue.
	if len(report.Asked) != 1 || report.Asked[0].Kind != "destination" {
		t.Fatalf("asked = %+v, want the queue asked and the method dropped", report.Asked)
	}
	if !hasIn(got, "queue-master", "listen.go") {
		t.Errorf("sources = %v, want the consumer of the queue", repoPaths(got))
	}
}

// TestFillGaps_obeysTheSelectivityCeilings: a name half the corpus defines and
// a token every repository carries are exactly what the walk and the crossing
// refuse to follow, and a lookup by name may not smuggle them back in.
func TestFillGaps_obeysTheSelectivityCeilings(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "hit.go", 0, 1, 10, "target", "func target() {}")
	// Five files define Close, one past what the walk will follow.
	for _, p := range []string{"aa.go", "ab.go", "ac.go", "ad.go", "ae.go"} {
		seedChunk(t, db, p, 0, 1, 10, "", "func x() {}")
		seedSymbol(t, db, p, "Close", 1)
	}
	// A route in four repositories, past the spread ceiling.
	for _, r := range []string{"r1", "r2", "r3", "r4"} {
		seedRepo(t, db, r)
		seedChunkIn(t, db, r, "X.java", 0, 1, 10, "", "func y() {}")
		seedTokenIn(t, db, r, "X.java", "route", "/shared", 1)
	}
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000},
		missing(name("Close", "symbol"), name("/shared", "route")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is it closed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want nothing past the ceilings", repoPaths(got))
	}
	if len(report.Unresolved) != 2 {
		t.Errorf("report = %+v, want both names reported unresolved", report)
	}
}

// TestFillGaps_asksAtMostEightNames: the reply is the model's, and a model
// that lists forty dependencies would spend the reserve on the last thirty.
func TestFillGaps_asksAtMostEightNames(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "hit.go", 0, 1, 10, "target", "func target() {}")
	var names []string
	for _, n := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8", "n9", "n10"} {
		seedChunk(t, db, n+".go", 0, 1, 10, n, "func "+n+"() {}")
		seedSymbol(t, db, n+".go", n, 1)
		names = append(names, name(n, "symbol"))
	}
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(names...))

	got, report, err := g.FillGaps(context.Background(), "what does it use?", []Source{sourceOf(t, db, hitID)}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(report.Asked) != gapMaxNames {
		t.Fatalf("asked %d names, want %d", len(report.Asked), gapMaxNames)
	}
	if report.Asked[0].Name != "n1" || report.Asked[gapMaxNames-1].Name != "n8" {
		t.Errorf("asked = %+v, want the model's first eight in its own order", report.Asked)
	}
	if has(got, "n9.go") || has(got, "n10.go") {
		t.Errorf("sources = %v, want the names past the cap left out", paths(got))
	}
}

// TestFillGaps_skipsANameTheSourcesAlreadyDefine: a dependency whose
// definition is in front of the model is not a gap, whatever the reply says.
func TestFillGaps_skipsANameTheSourcesAlreadyDefine(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { return 3 }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("unitPrice", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != 1 {
		t.Errorf("sources = %v, want the one already there", repoPaths(got))
	}
	if len(report.Asked) != 0 {
		t.Errorf("asked = %+v, want the name dropped before the lookup", report.Asked)
	}
}

// TestFillGaps_keepsTheSourcesWhenTheReplyIsNotJSON: the pass may never do
// worse than not running — same rule as the reranker's.
func TestFillGaps_keepsTheSourcesWhenTheReplyIsNotJSON(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { unitPrice() }")
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { return 3 }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	w := &warnings{}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		WithGapPass(gapLLM(t, "I could not find any missing dependency, sorry.", nil))
	g.Log = slog.New(w)
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want them unchanged", repoPaths(got))
	}
	if report.Skipped != "not json" {
		t.Errorf("skipped = %q, want the reply reported", report.Skipped)
	}
	if len(w.msgs) != 1 {
		t.Errorf("warnings = %v, want one", w.msgs)
	}
}

// TestFillGaps_keepsTheSourcesWhenTheCallFails: a gate-lane outage must not
// fail a turn that already has everything the walk gathered.
func TestFillGaps_keepsTheSourcesWhenTheCallFails(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() {}")
	w := &warnings{}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).WithGapPass(gapLLMFailing(t))
	g.Log = slog.New(w)
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) || report.Skipped != "call failed" {
		t.Errorf("sources = %v, report = %+v, want the sources kept", repoPaths(got), report)
	}
	if len(w.msgs) != 1 {
		t.Errorf("warnings = %v, want one", w.msgs)
	}
}

// TestFillGaps_returnsTheContextError: the reader left; there is no turn to
// fill a gap for, and that is not a model failure to swallow.
func TestFillGaps_returnsTheContextError(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() {}")
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := g.FillGaps(ctx, "how is the total computed?", []Source{sourceOf(t, db, hitID)}, nil)
	if err == nil {
		t.Fatal("FillGaps returned no error for a cancelled context")
	}
}

// TestFillGaps_neverExceedsTheBudget: the landing is admitted under the same
// rule everything else is, and a pass with no room says so rather than
// trimming what the answer is already built on.
func TestFillGaps_neverExceedsTheBudget(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { unitPrice() }")
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { "+strings.Repeat("x ", 400)+" }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 60}, missing(name("unitPrice", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want the oversized landing refused", repoPaths(got))
	}
	// A name the budget refused is reported as refused, not skipped: the pass
	// ran, asked and resolved, and only the room was missing.
	if report.Skipped != "" {
		t.Errorf("skipped = %q, want the pass reported as having run", report.Skipped)
	}
	if len(report.Refused) != 1 || report.Refused[0] != "unitPrice" {
		t.Errorf("report = %+v, want the name the budget refused", report)
	}
}

// TestFillGaps_reportsANameRefusedWhileAnotherLanded: the report is
// bookkeeping over the asked names, so a name refused for room while another
// one landed may not vanish from it.
func TestFillGaps_reportsANameRefusedWhileAnotherLanded(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { unitPrice(); vat() }")
	seedChunk(t, db, "vat.go", 0, 1, 10, "vat", "func vat() int { return 8 }")
	seedSymbol(t, db, "vat.go", "vat", 1)
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { "+strings.Repeat("x ", 400)+" }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	// Room for the small definition and nothing like enough for the big one.
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 120},
		missing(name("vat", "symbol"), name("unitPrice", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !has(got, "vat.go") || has(got, "price.go") {
		t.Errorf("sources = %v, want the small landing and not the big one", paths(got))
	}
	if len(report.Landed) != 1 || report.Landed[0] != "vat" {
		t.Errorf("landed = %v, want the name that fitted", report.Landed)
	}
	if len(report.Refused) != 1 || report.Refused[0] != "unitPrice" {
		t.Errorf("refused = %v, want the name the budget could not hold", report.Refused)
	}
}

// TestFillGaps_stopsLookingUpOnceTheBudgetRefused: a refusal means the
// reserve is spent. Every lookup after it queries the index for a chunk the
// admitter has already said it cannot hold, and a later name that happens to
// be small enough would be admitted out of the model's own order.
func TestFillGaps_stopsLookingUpOnceTheBudgetRefused(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() { unitPrice(); vat() }")
	seedChunk(t, db, "price.go", 0, 5, 20, "unitPrice", "func unitPrice() int { "+strings.Repeat("x ", 400)+" }")
	seedSymbol(t, db, "price.go", "unitPrice", 5)
	// Small enough to fit in what is left, and it must still not be fetched.
	seedChunk(t, db, "vat.go", 0, 1, 10, "vat", "func vat() {}")
	seedSymbol(t, db, "vat.go", "vat", 1)
	sources := []Source{sourceOf(t, db, hitID)}
	budget := estimateTokens(sources[0].Text) + estimateTokens("func vat() {}") + 1
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: budget},
		missing(name("unitPrice", "symbol"), name("vat", "symbol")))

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want nothing admitted under a spent budget", paths(got))
	}
	if len(report.Refused) != 2 || report.Refused[1] != "vat" {
		t.Errorf("refused = %v, want the name after the refusal reported too", report.Refused)
	}
	if len(report.Landed) != 0 || len(report.Unresolved) != 0 {
		t.Errorf("report = %+v, want the rest of the reply refused", report)
	}
}

// TestFillGaps_landsOnlyWhenSomethingNewIsAdmitted: a name whose definition
// resolves onto a chunk the sources already carry added nothing, and a report
// calling that a landing would read as the pass having earned its reserve.
func TestFillGaps_landsOnlyWhenSomethingNewIsAdmitted(t *testing.T) {
	db := gatherDB(t)
	// One chunk, holding both the caller and the definition, so the lookup
	// resolves to the chunk that is already in front of the model.
	hitID := seedChunk(t, db, "cart.go", 0, 1, 20, "total",
		"func total() int { return unitPrice() }\nfunc unitPrice() int { return 3 }")
	seedSymbol(t, db, "cart.go", "unitPrice", 2)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("unitPrice", "symbol")))
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := g.FillGaps(context.Background(), "how is the total computed?", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(got) != len(sources) {
		t.Errorf("sources = %v, want the one chunk it already had", repoPaths(got))
	}
	if len(report.Landed) != 0 {
		t.Errorf("landed = %v, want nothing: no new chunk was admitted", report.Landed)
	}
	if len(report.Refused) != 0 {
		t.Errorf("refused = %v, want nothing: the budget refused nothing", report.Refused)
	}
}

// TestFillGaps_asksForARouteACrossingOnlyLandedNear: the sources carry a
// crossing on "/orders/123/items", which is not the route "/orders" the model
// asked for. Reading the reason as a substring drops the name and the handler
// is never fetched.
func TestFillGaps_asksForARouteACrossingOnlyLandedNear(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "orders")
	hitID := seedChunk(t, db, "client.go", 0, 1, 10, "fetch", "get(ordersUrl + id + \"/items\")")
	itemsID := seedChunkIn(t, db, "orders", "Items.java", 0, 1, 10, "items", `@GetMapping("/orders/{id}/items")`)
	seedChunkIn(t, db, "orders", "Orders.java", 0, 1, 10, "orders", `@GetMapping("/orders")`)
	seedTokenIn(t, db, "orders", "Orders.java", "route", "/orders", 1)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("/orders", "route")))
	near := sourceOf(t, db, hitID)
	crossing := sourceOf(t, db, itemsID)
	crossing.Reason = "edge:route /orders/123/items from peeq/client.go"

	got, report, err := g.FillGaps(context.Background(), "what does the orders route serve?",
		[]Source{near, crossing}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !hasIn(got, "orders", "Orders.java") {
		t.Errorf("sources = %v, want the handler of the route asked for", repoPaths(got))
	}
	if len(report.Landed) != 1 {
		t.Errorf("report = %+v, want the route landed", report)
	}
}

// TestFillGaps_skipsARouteACrossingAlreadyFollowed is the other half of the
// same rule, including the leading slash every indexed route carries and the
// model leaves off.
func TestFillGaps_skipsARouteACrossingAlreadyFollowed(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "payment")
	hitID := seedChunkIn(t, db, "payment", "transport.go", 0, 1, 10, "handler", `Path("/paymentAuth")`)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("paymentAuth", "route")))
	crossing := sourceOf(t, db, hitID)
	crossing.Reason = "edge:route /paymentAuth from orders/Client.java"

	_, report, err := g.FillGaps(context.Background(), "how is a payment authorised?", []Source{crossing}, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if len(report.Asked) != 0 {
		t.Errorf("asked = %+v, want the route the crossing already followed dropped", report.Asked)
	}
}

// TestFillGaps_landsInTheAskedStageWhenItSortsLast: the landing cap is per
// name, and applying it before the stage restriction spends it on the stages
// the turn did not ask about — the asked one then never lands at all.
func TestFillGaps_landsInTheAskedStageWhenItSortsLast(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-infra")
	hitID := seedChunk(t, db, "Job.java", 0, 1, 10, "Job", `@Scheduled("${acme.cron.send-digest}")`)
	for _, stage := range []string{"dev", "intg", "perf", "pre", "prod"} {
		p := stage + "/application.properties"
		seedChunkIn(t, db, "acme-infra", p, 0, 1, 10, "", "acme.cron.send-digest=0 0 * ? * * *")
		seedTokenIn(t, db, "acme-infra", p, "property", "acme.cron.send-digest", 1)
	}
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("acme.cron.send-digest", "property")))

	got, report, err := g.FillGaps(context.Background(), "when does the digest run in production?",
		[]Source{sourceOf(t, db, hitID)}, retrieve.StagePrefixes{"acme-infra": "prod/"})
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !hasIn(got, "acme-infra", "prod/application.properties") {
		t.Errorf("sources = %v, want the asked stage past the landing cap", repoPaths(got))
	}
	if len(report.Landed) != 1 {
		t.Errorf("report = %+v, want the key landed", report)
	}
}

// TestFillGaps_readsANameOnlyOtherStagesHoldAsUnresolved: the stage
// restriction is applied before a name is called empty, so a key this turn
// may not read is reported as one it could not resolve, never dropped.
func TestFillGaps_readsANameOnlyOtherStagesHoldAsUnresolved(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-infra")
	// The key is not spelled in the gathered code — this is the pass's own
	// case — so the keyword fallback has nothing at home to land on either.
	hitID := seedChunk(t, db, "Job.java", 0, 1, 10, "Job", "@Scheduled(digestCron)")
	seedChunkIn(t, db, "acme-infra", "intg/application.properties", 0, 1, 10, "", "acme.cron.send-digest=0 0 * ? * * *")
	seedTokenIn(t, db, "acme-infra", "intg/application.properties", "property", "acme.cron.send-digest", 1)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("acme.cron.send-digest", "property")))

	got, report, err := g.FillGaps(context.Background(), "when does the digest run in production?",
		[]Source{sourceOf(t, db, hitID)}, retrieve.StagePrefixes{"acme-infra": "prod/"})
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if hasIn(got, "acme-infra", "intg/application.properties") {
		t.Errorf("sources = %v, want the other stage left out", repoPaths(got))
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "acme.cron.send-digest" {
		t.Errorf("report = %+v, want the key reported unresolved", report)
	}
}

// TestFillGaps_landsOnlyInTheAskedStage: the pass lands under the same stage
// restriction the search and the crossing ran under, or a turn narrowed to
// production reports every stage after all.
func TestFillGaps_landsOnlyInTheAskedStage(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "peeq", "Job.java", 0, 1, 10, "Job", `@Scheduled("${acme.cron.send-digest}")`)
	for _, stage := range []string{"intg", "prod"} {
		p := stage + "/application.properties"
		seedChunkIn(t, db, "acme-infra", p, 0, 1, 10, "", "acme.cron.send-digest=0 0 * ? * * *")
		seedTokenIn(t, db, "acme-infra", p, "property", "acme.cron.send-digest", 1)
	}
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing(name("acme.cron.send-digest", "property")))

	got, _, err := g.FillGaps(context.Background(), "when does the digest run in production?",
		[]Source{sourceOf(t, db, hitID)}, retrieve.StagePrefixes{"acme-infra": "prod/"})
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	if !hasIn(got, "acme-infra", "prod/application.properties") {
		t.Errorf("sources = %v, want the prod stage", repoPaths(got))
	}
	if hasIn(got, "acme-infra", "intg/application.properties") {
		t.Errorf("sources = %v, want the other stage left out", repoPaths(got))
	}
}

// TestFillGaps_cutsTheExcerptToTheNumberOfSources: the prompt is one short
// call whatever the walk gathered, so each source's share of it shrinks as
// there are more of them, and a handful of sources are shown whole.
func TestFillGaps_cutsTheExcerptToTheNumberOfSources(t *testing.T) {
	body := strings.Repeat("x", 3000)
	many := make([]Source, 150)
	for i := range many {
		many[i] = Source{ChunkID: int64(i + 1), Repo: "peeq", Path: "a.go", StartLine: 1, EndLine: 9, Text: body}
	}
	few := many[:10]

	var wide, narrow string
	db := gatherDB(t)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).WithGapPass(gapLLM(t, missing(), &wide))
	if _, _, err := g.FillGaps(context.Background(), "q", many, nil); err != nil {
		t.Fatalf("FillGaps: %v", err)
	}
	g2 := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).WithGapPass(gapLLM(t, missing(), &narrow))
	if _, _, err := g2.FillGaps(context.Background(), "q", few, nil); err != nil {
		t.Fatalf("FillGaps: %v", err)
	}

	limit := gapPromptChars / len(many)
	if strings.Contains(wide, strings.Repeat("x", limit+1)) {
		t.Errorf("an excerpt over %d runes reached the prompt with %d sources", limit, len(many))
	}
	if !strings.Contains(wide, strings.Repeat("x", limit-1)) {
		t.Errorf("the excerpt was cut far below its %d-rune share", limit)
	}
	if !strings.Contains(narrow, body) {
		t.Error("ten sources did not reach the prompt whole")
	}
	if !strings.Contains(narrow, "[1] peeq a.go:1-9") {
		t.Errorf("the prompt does not carry the source header: %.120s", narrow)
	}
}

// TestFillGaps_isOffWithoutAClient: the pass ships off, and off changes
// nothing and calls nothing.
func TestFillGaps_isOffWithoutAClient(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "cart.go", 0, 1, 10, "total", "func total() {}")
	sources := []Source{sourceOf(t, db, hitID)}

	got, report, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		FillGaps(context.Background(), "q", sources, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}
	if len(got) != 1 || report.Skipped != "off" {
		t.Errorf("sources = %v, report = %+v", repoPaths(got), report)
	}
}

// TestFillGaps_saysSoWithNoSources: "no hit means no hit" — a turn that
// gathered nothing has no gap to read, and no call is worth making.
func TestFillGaps_saysSoWithNoSources(t *testing.T) {
	db := gatherDB(t)
	g := gapGatherer(t, db, GatherOptions{MaxHops: 1, TokenBudget: 10000}, missing())

	got, report, err := g.FillGaps(context.Background(), "q", nil, nil)
	if err != nil {
		t.Fatalf("FillGaps: %v", err)
	}
	if len(got) != 0 || report.Skipped != "no sources" {
		t.Errorf("sources = %v, report = %+v", repoPaths(got), report)
	}
}

// TestGather_reservesForTheGapPassOnlyWhenItIsOn: the reserve is taken out of
// the walk's budget when the pass can spend it, and not otherwise — the same
// bargain crossingReserve strikes, one twelfth instead of one sixth.
func TestGather_reservesForTheGapPassOnlyWhenItIsOn(t *testing.T) {
	seed := func(t *testing.T) (*sql.DB, retrieve.Hit) {
		db := gatherDB(t)
		hitID := seedChunk(t, db, "hit.go", 0, 1, 10, "target", "func target() { helper() }")
		seedChunk(t, db, "helper.go", 0, 1, 10, "helper", "func helper() { "+strings.Repeat("x ", 2300)+" }")
		seedSymbol(t, db, "helper.go", "helper", 1)
		return db, hitFor(t, db, hitID)
	}
	opts := GatherOptions{MaxHops: 1, TokenBudget: 1200, NoCrossings: true}

	db, hit := seed(t)
	off, err := NewGatherer(db, opts).Gather(context.Background(), []retrieve.Hit{hit})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !has(off, "helper.go") {
		t.Fatalf("sources = %v, want the reference inside the full budget", paths(off))
	}

	db2, hit2 := seed(t)
	on, err := gapGatherer(t, db2, opts, missing()).Gather(context.Background(), []retrieve.Hit{hit2})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if has(on, "helper.go") {
		t.Errorf("sources = %v, want the gap reserve held back from the walk", paths(on))
	}
}
