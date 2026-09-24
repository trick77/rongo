package ask

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
)

// seedTokenIn records an integration token on a file that already exists,
// the way the indexer does for a queue name or a route literal.
func seedTokenIn(t *testing.T, db *sql.DB, repo, path, kind, value string, line int) {
	t.Helper()
	var fileID int64
	if err := db.QueryRow(`SELECT id FROM files WHERE repo=? AND path=?`, repo, path).Scan(&fileID); err != nil {
		t.Fatalf("look up file for token: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO integration_tokens (file_id, kind, value, line) VALUES (?, ?, ?, ?)`,
		fileID, kind, value, line); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func sourceIn(sources []Source, repo, path string) (Source, bool) {
	for _, s := range sources {
		if s.Repo == repo && s.Path == path {
			return s, true
		}
	}
	return Source{}, false
}

// TestGather_crossesAQueueNameIntoTheConsumersRepository is the flagship
// shape of the flow corpus: shipping sends to "shipping-task", queue-master
// listens on it, and the two files share no import, no type and no symbol.
// The symbol walk cannot cross that; the integration token can, and the
// crossing lands on the chunk holding the literal, not on the whole file.
func TestGather_crossesAQueueNameIntoTheConsumersRepository(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "shipping")
	seedRepo(t, db, "queue-master")
	hitID := seedChunkIn(t, db, "shipping", "ShippingController.java", 0, 1, 20, "post",
		`rabbitTemplate.convertAndSend("shipping-task", shipment)`)
	seedTokenIn(t, db, "shipping", "ShippingController.java", "destination", "shipping-task", 10)
	// The far side: a configuration class naming the queue on line 30, in
	// its second chunk, plus an unrelated first chunk that must NOT arrive.
	seedChunkIn(t, db, "queue-master", "ShippingConsumerConfiguration.java", 0, 1, 20, "imports",
		"import org.springframework.amqp;")
	seedChunkIn(t, db, "queue-master", "ShippingConsumerConfiguration.java", 1, 21, 40, "queue",
		`Queue queue() { return new Queue("shipping-task"); } MessageListenerAdapter adapter(ShippingTaskHandler h)`)
	seedTokenIn(t, db, "queue-master", "ShippingConsumerConfiguration.java", "destination", "shipping-task", 30)
	// One step inland on the far side: the handler the configuration wires
	// up, which carries no literal at all.
	seedChunkIn(t, db, "queue-master", "ShippingTaskHandler.java", 0, 1, 10, "ShippingTaskHandler",
		"class ShippingTaskHandler { void handleMessage(Shipment s) {} }")
	seedSymbolIn(t, db, "queue-master", "ShippingTaskHandler.java", "ShippingTaskHandler", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	cfg, ok := sourceIn(got, "queue-master", "ShippingConsumerConfiguration.java")
	if !ok {
		t.Fatalf("sources = %v, want the consumer's configuration reached across the queue name", repoPaths(got))
	}
	if cfg.StartLine != 21 {
		t.Errorf("crossing landed on lines %d-%d, want the chunk holding the literal (21-40)", cfg.StartLine, cfg.EndLine)
	}
	if !strings.HasPrefix(cfg.Reason, "edge:destination shipping-task") {
		t.Errorf("reason = %q, want it to name the queue the crossing followed", cfg.Reason)
	}
	if cfg.Hop != 1 {
		t.Errorf("hop = %d, want 1: a crossing from a search hit is one hop away", cfg.Hop)
	}
	for _, s := range got {
		if s.Repo == "queue-master" && s.Path == "ShippingConsumerConfiguration.java" && s.StartLine == 1 {
			t.Errorf("the configuration's unrelated first chunk arrived; only the chunk at the literal should")
		}
	}
	if h, ok := sourceIn(got, "queue-master", "ShippingTaskHandler.java"); !ok {
		t.Errorf("sources = %v, want the handler reached one symbol hop past the crossing", repoPaths(got))
	} else if h.Hop != 2 {
		t.Errorf("handler hop = %d, want 2", h.Hop)
	}
}

// TestGather_crossesFromAReferencedFileToo: the literal usually sits next to
// the file the search returned rather than in it. OrdersController calls
// config.getPaymentUri(); "/paymentAuth" lives in the properties class. The
// crossing has to start from what the symbol walk reached as well as from
// the hits, or the edge table is only ever consulted for files that happen
// to carry their own literal.
func TestGather_crossesFromAReferencedFileToo(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "orders")
	seedRepo(t, db, "payment")
	hitID := seedChunkIn(t, db, "orders", "OrdersController.java", 0, 1, 20, "post",
		"OrdersConfigurationProperties config; post(config.getPaymentUri())")
	seedChunkIn(t, db, "orders", "OrdersConfigurationProperties.java", 0, 1, 20, "OrdersConfigurationProperties",
		`class OrdersConfigurationProperties { String paymentUri = "/paymentAuth"; }`)
	seedSymbolIn(t, db, "orders", "OrdersConfigurationProperties.java", "OrdersConfigurationProperties", 1)
	seedTokenIn(t, db, "orders", "OrdersConfigurationProperties.java", "route", "/paymentAuth", 5)
	seedChunkIn(t, db, "payment", "transport.go", 0, 1, 20, "MakeHandler",
		`r.Methods("POST").Path("/paymentAuth").Handler(authorise)`)
	seedTokenIn(t, db, "payment", "transport.go", "route", "/paymentAuth", 3)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if _, ok := sourceIn(got, "payment", "transport.go"); !ok {
		t.Errorf("sources = %v, want payment reached through the properties class the hit references", repoPaths(got))
	}
}

// TestGather_theBudgetBoundsACrossingToo: an edge chunk is taken under the
// same budget as a reference. Search hits are never evicted; everything
// else stops when the budget is spent, crossings included.
func TestGather_theBudgetBoundsACrossingToo(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "a")
	seedRepo(t, db, "b")
	hitID := seedChunkIn(t, db, "a", "send.go", 0, 1, 10, "send", `publish("orders")`)
	seedTokenIn(t, db, "a", "send.go", "destination", "orders", 1)
	seedChunkIn(t, db, "b", "listen.go", 0, 1, 10, "listen", `subscribe("orders") `+strings.Repeat("x ", 400))
	seedTokenIn(t, db, "b", "listen.go", "destination", "orders", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 50}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "b", "listen.go") {
		t.Errorf("the crossing was taken past the budget")
	}
	if !hasIn(got, "a", "send.go") {
		t.Errorf("the search hit was evicted")
	}
}

// TestGather_readsTestFilesAfterTheMechanism: a test that references the
// service is a correct hop and a poor waypoint. Within one hop the sources
// that are not tests are taken first, so a tight budget spends itself on
// the mechanism rather than on its harness.
func TestGather_readsTestFilesAfterTheMechanism(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "handler.go", 0, 1, 10, "handle", "func handle() { Authorise(); Decline() }")
	// Alphabetically first, and a test.
	seedChunk(t, db, "a_test.go", 0, 1, 10, "TestAuthorise", "func Authorise() {}")
	seedSymbol(t, db, "a_test.go", "Authorise", 1)
	seedChunk(t, db, "service.go", 0, 1, 10, "Decline", "func Decline() {}")
	seedSymbol(t, db, "service.go", "Decline", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := []string{"handler.go", "service.go", "a_test.go"}
	if strings.Join(paths(got), ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", paths(got), want)
	}
}

// TestGather_crossesAPropertyKeyIntoEveryStageOfTheInfraRepository: the
// job reads "${acme.cron.send-digest}", the service's own defaults set it,
// and the infrastructure repository sets it once per stage. One key, three
// landings in the far repository — one chunk per stage file — which is what
// lets an answer state the value for every stage rather than for one.
func TestGather_crossesAPropertyKeyIntoEveryStageOfTheInfraRepository(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-service")
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "acme-service", "src/main/java/acme/JobSendDigest.java", 0, 1, 20, "JobSendDigest",
		`@Scheduled(cron = "${acme.cron.send-digest}") public void run() {}`)
	seedTokenIn(t, db, "acme-service", "src/main/java/acme/JobSendDigest.java", "property", "acme.cron.send-digest", 4)
	// The code's default, in the SAME repository, which no symbol reaches.
	seedChunkIn(t, db, "acme-service", "src/main/resources/application-default.properties", 0, 1, 10, "",
		"acme.cron.send-digest=0/20 * * ? * * *\nacme.mail.processor=x\n")
	seedTokenIn(t, db, "acme-service", "src/main/resources/application-default.properties", "property", "acme.cron.send-digest", 1)
	for i, stage := range []string{"syst", "intg", "prod"} {
		p := stage + "/intranet/application.properties"
		seedChunkIn(t, db, "acme-infra", p, 0, 1, 10, "",
			"# stage "+stage+"\nacme.cron.send-digest=0 "+string(rune('0'+i))+" * ? * * *\nacme.mail.processor=x\n")
		seedTokenIn(t, db, "acme-infra", p, "property", "acme.cron.send-digest", 2)
	}
	// A symbol the properties files happen to name. The far side of a
	// property landing takes no symbol hop, so it must not arrive.
	seedChunkIn(t, db, "acme-service", "src/main/java/acme/Processor.java", 0, 1, 10, "processor",
		"class processor {}")
	seedSymbolIn(t, db, "acme-service", "src/main/java/acme/Processor.java", "processor", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if d, ok := sourceIn(got, "acme-service", "src/main/resources/application-default.properties"); !ok {
		t.Errorf("sources = %v, want the repository's own defaults file reached on the key", repoPaths(got))
	} else if !strings.HasPrefix(d.Reason, "edge:property") {
		t.Errorf("defaults reason = %q", d.Reason)
	}
	if _, ok := sourceIn(got, "acme-service", "src/main/java/acme/Processor.java"); ok {
		t.Error("a property landing took a symbol hop into Processor.java")
	}

	for _, stage := range []string{"syst", "intg", "prod"} {
		s, ok := sourceIn(got, "acme-infra", stage+"/intranet/application.properties")
		if !ok {
			t.Errorf("sources = %v, want the %s stage reached across the property key", repoPaths(got), stage)
			continue
		}
		if !strings.HasPrefix(s.Reason, "edge:property acme.cron.send-digest from acme-service/") {
			t.Errorf("reason = %q, want the property key named", s.Reason)
		}
		if s.Hop != 1 {
			t.Errorf("%s hop = %d, want 1", stage, s.Hop)
		}
	}
}

// TestGatherWithin_landsOnlyInTheAskedStage: the same fixture under "in
// production". The search was narrowed to prod/; the crossing must be too,
// or the answer reports every stage after all.
func TestGatherWithin_landsOnlyInTheAskedStage(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-service")
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "acme-service", "src/main/java/acme/JobSendDigest.java", 0, 1, 20, "JobSendDigest",
		`@Scheduled(cron = "${acme.cron.send-digest}") public void run() {}`)
	seedTokenIn(t, db, "acme-service", "src/main/java/acme/JobSendDigest.java", "property", "acme.cron.send-digest", 4)
	for _, stage := range []string{"syst", "intg", "prod"} {
		p := stage + "/intranet/application.properties"
		seedChunkIn(t, db, "acme-infra", p, 0, 1, 10, "", "acme.cron.send-digest=0 0 * ? * * *\n")
		seedTokenIn(t, db, "acme-infra", p, "property", "acme.cron.send-digest", 1)
	}

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		GatherWithin(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)}, retrieve.StagePrefixes{"acme-infra": "prod/"})
	if err != nil {
		t.Fatalf("GatherWithin: %v", err)
	}

	if _, ok := sourceIn(got, "acme-infra", "prod/intranet/application.properties"); !ok {
		t.Errorf("sources = %v, want the prod stage reached", repoPaths(got))
	}
	for _, other := range []string{"syst", "intg"} {
		if _, ok := sourceIn(got, "acme-infra", other+"/intranet/application.properties"); ok {
			t.Errorf("the %s stage arrived under a prod restriction", other)
		}
	}
}

// TestGather_propertyCrossingsWaitForEveryRouteCrossing: the first gathered
// file crosses on a property key, a later one on a route, and the budget
// holds one landing. The route landing must be the one taken, whichever
// file came first — the reserve was measured for routes and destinations.
func TestGather_propertyCrossingsWaitForEveryRouteCrossing(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "a")
	seedRepo(t, db, "b")
	first := seedChunkIn(t, db, "a", "Interceptor.java", 0, 1, 10, "Interceptor", `"${spring.application.name}"`)
	seedTokenIn(t, db, "a", "Interceptor.java", "property", "spring.application.name", 1)
	second := seedChunkIn(t, db, "a", "Config.java", 0, 1, 10, "Config", `"/paymentAuth"`)
	seedTokenIn(t, db, "a", "Config.java", "route", "/paymentAuth", 1)
	seedChunkIn(t, db, "b", "application.properties", 0, 1, 10, "", "spring.application.name=b")
	seedTokenIn(t, db, "b", "application.properties", "property", "spring.application.name", 1)
	seedChunkIn(t, db, "b", "transport.go", 0, 1, 10, "MakeHandler", `r.Path("/paymentAuth")`)
	seedTokenIn(t, db, "b", "transport.go", "route", "/paymentAuth", 1)

	// The two hits cost a handful of tokens each; the budget leaves room for
	// one more chunk and no second.
	hits := []retrieve.Hit{hitInFor(t, db, first), hitInFor(t, db, second)}
	budget := estimateTokens(hits[0].RawText) + estimateTokens(hits[1].RawText) + estimateTokens(`r.Path("/paymentAuth")`)
	got, err := NewGatherer(db, GatherOptions{MaxHops: 0, TokenBudget: budget}).
		Gather(context.Background(), hits)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if _, ok := sourceIn(got, "b", "transport.go"); !ok {
		t.Errorf("sources = %v, want the route landing taken ahead of the property landing", repoPaths(got))
	}
	if _, ok := sourceIn(got, "b", "application.properties"); ok {
		t.Errorf("sources = %v: the property landing spent the reserve the route needed", repoPaths(got))
	}
}

func TestCrossings_orderRoutesAndDestinationsBeforeProperties(t *testing.T) {
	// The reserve was measured with routes and destinations alone; a file
	// naming twenty properties must not spend it before a queue's far side
	// is reached.
	db := gatherDB(t)
	seedRepo(t, db, "a")
	seedRepo(t, db, "b")
	fromID := seedChunkIn(t, db, "a", "A.java", 0, 1, 10, "A", "x")
	seedTokenIn(t, db, "a", "A.java", "property", "k.one", 1)
	seedTokenIn(t, db, "a", "A.java", "property", "k.two", 2)
	seedTokenIn(t, db, "a", "A.java", "destination", "q", 3)
	seedTokenIn(t, db, "a", "A.java", "route", "/r", 4)
	for _, f := range []struct{ path, kind, value string }{
		{"one.properties", "property", "k.one"},
		{"two.properties", "property", "k.two"},
		{"Consumer.java", "destination", "q"},
		{"Controller.java", "route", "/r"},
	} {
		seedChunkIn(t, db, "b", f.path, 0, 1, 10, "", f.value)
		seedTokenIn(t, db, "b", f.path, f.kind, f.value, 1)
	}
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})
	from := Source{ChunkID: fromID, Repo: "a", Path: "A.java"}

	far, err := g.crossings(context.Background(), from)
	if err != nil {
		t.Fatalf("crossings: %v", err)
	}

	want := []string{"Controller.java", "Consumer.java", "one.properties", "two.properties"}
	if len(far) != len(want) {
		t.Fatalf("landings = %v, want %v", paths(far), want)
	}
	for i := range want {
		if far[i].Path != want[i] {
			t.Errorf("landing %d = %s, want %s (order %v)", i, far[i].Path, want[i], paths(far))
		}
	}
}

// Chunk windows overlap, so a token's line is covered by more than one of
// them and the landing is a choice. It must follow from the ordinal — the
// chunk's position in its file — and never from the order the chunks were
// written: ids are handed out in index order, and a landing chosen on one
// moves after a re-index that changed no code.
func TestCrossings_landOnTheSameWindowWhateverTheWriteOrder(t *testing.T) {
	type window struct {
		ordinal, start, end int
	}
	early := window{0, 1, 20}
	late := window{1, 11, 30}
	for _, c := range []struct {
		name  string
		order []window
	}{
		{"the earlier window written first", []window{early, late}},
		{"the later window written first", []window{late, early}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Given: a route literal on line 15 of the far side, covered by
			// both of that file's windows.
			db := gatherDB(t)
			seedRepo(t, db, "a")
			seedRepo(t, db, "b")
			fromID := seedChunkIn(t, db, "a", "Client.java", 0, 1, 10, "call", `get("/paymentAuth")`)
			seedTokenIn(t, db, "a", "Client.java", "route", "/paymentAuth", 5)
			for _, w := range c.order {
				seedChunkIn(t, db, "b", "Routes.java", w.ordinal, w.start, w.end, "routes",
					`register("/paymentAuth")`)
			}
			seedTokenIn(t, db, "b", "Routes.java", "route", "/paymentAuth", 15)
			g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})

			// When
			far, err := g.crossings(context.Background(), Source{ChunkID: fromID, Repo: "a", Path: "Client.java"})

			// Then
			if err != nil {
				t.Fatalf("crossings: %v", err)
			}
			if len(far) != 1 {
				t.Fatalf("landings = %v, want exactly one", paths(far))
			}
			if far[0].StartLine != early.start {
				t.Errorf("landed on lines %d-%d, want the lower-ordinal window (%d-%d) whatever the write order",
					far[0].StartLine, far[0].EndLine, early.start, early.end)
			}
		})
	}
}

// definer is one repository's definition of the walked symbol, so a test can
// seed the same two definitions in either index order.
type definer struct{ repo, path string }

// seedDefiners builds the reference fixture: a caller in "src" naming a symbol
// two OTHER repositories define, written in the given order.
func seedDefiners(t *testing.T, order []definer) (*Gatherer, Source) {
	t.Helper()
	db := gatherDB(t)
	seedRepo(t, db, "src")
	from := Source{Repo: "src", Path: "Caller.java", Text: "Shipment.dispatch()"}
	seedChunkIn(t, db, "src", "Caller.java", 0, 1, 10, "call", from.Text)
	for _, d := range order {
		seedRepo(t, db, d.repo)
		seedChunkIn(t, db, d.repo, d.path, 0, 1, 10, "Shipment", "class Shipment { void dispatch() {} }")
		seedSymbolIn(t, db, d.repo, d.path, "Shipment", 1)
	}
	return NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}), from
}

// The path decides ahead of the repository. Ordering by repository first would
// regroup a multi-repository result by product, which changes which chunks the
// budget admits — a ranking change, and not what a determinism fix may do.
func TestReferenced_equalDefinersOrderByPathBeforeRepository(t *testing.T) {
	// Given: two definitions at DIFFERENT paths, where the path order and the
	// repository order disagree — ant/Zeta.java would come first if the
	// repository decided.
	first := definer{"bee", "Alpha.java"}
	second := definer{"ant", "Zeta.java"}
	for _, c := range []struct {
		name  string
		order []definer
	}{
		{"bee indexed first", []definer{first, second}},
		{"ant indexed first", []definer{second, first}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, from := seedDefiners(t, c.order)

			// When
			refs, err := g.referenced(context.Background(), from)

			// Then
			if err != nil {
				t.Fatalf("referenced: %v", err)
			}
			if len(refs) != 2 {
				t.Fatalf("references = %v, want both definitions", repoPaths(refs))
			}
			if refs[0].Path != "Alpha.java" || refs[1].Path != "Zeta.java" {
				t.Errorf("reference order = %s, %s; want the paths in order, the repository only breaking a tie",
					repoPaths(refs)[0], repoPaths(refs)[1])
			}
		})
	}
}

// Inside one file the walk reads in FILE order. The symbol name is the last
// resort, for two names landing on one chunk, and must not get in front of the
// ordinal: a file would then be read out of order because of what its symbols
// happen to be called.
func TestReferenced_insideOneFileTheOrdinalDecidesBeforeTheSymbolName(t *testing.T) {
	// Given: one file defining two equally selective names, the alphabetically
	// LATER one in the file's first chunk. The later chunk is written first,
	// so neither the name nor the rowid points at the answer.
	db := gatherDB(t)
	seedRepo(t, db, "src")
	seedRepo(t, db, "bee")
	from := Source{Repo: "src", Path: "Caller.java", Text: "Zebra.run(); Alpha.run()"}
	seedChunkIn(t, db, "src", "Caller.java", 0, 1, 10, "call", from.Text)
	seedChunkIn(t, db, "bee", "Shared.java", 4, 100, 110, "Alpha", "class Alpha { void run() {} }")
	seedChunkIn(t, db, "bee", "Shared.java", 0, 1, 10, "Zebra", "class Zebra { void run() {} }")
	seedSymbolIn(t, db, "bee", "Shared.java", "Alpha", 105)
	seedSymbolIn(t, db, "bee", "Shared.java", "Zebra", 5)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})

	// When
	refs, err := g.referenced(context.Background(), from)

	// Then
	if err != nil {
		t.Fatalf("referenced: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("references = %v, want both chunks of the file", repoPaths(refs))
	}
	if refs[0].StartLine != 1 || refs[1].StartLine != 100 {
		t.Errorf("reference order = lines %d then %d, want the file read in file order (1 then 100)",
			refs[0].StartLine, refs[1].StartLine)
	}
}

// Two repositories routinely hold the same path. That tie is what the
// repository breaks, so the walk does not read whichever was indexed first.
func TestReferenced_onePathInTwoRepositoriesOrdersByRepository(t *testing.T) {
	for _, c := range []struct {
		name  string
		order []definer
	}{
		{"bee indexed first", []definer{{"bee", "Shared.java"}, {"ant", "Shared.java"}}},
		{"ant indexed first", []definer{{"ant", "Shared.java"}, {"bee", "Shared.java"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Given
			g, from := seedDefiners(t, c.order)

			// When
			refs, err := g.referenced(context.Background(), from)

			// Then
			if err != nil {
				t.Fatalf("referenced: %v", err)
			}
			if len(refs) != 2 {
				t.Fatalf("references = %v, want both repositories", repoPaths(refs))
			}
			if refs[0].Repo != "ant" || refs[1].Repo != "bee" {
				t.Errorf("reference order = %s, %s; want ant then bee whatever the index order",
					refs[0].Repo, refs[1].Repo)
			}
		})
	}
}

// configToConfig is an infra properties file that the search hit, sharing a
// framework key with a service's properties file: the shape that filled a live
// turn's budget with crossings nobody asked about.
func configToConfig(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db := gatherDB(t)
	seedRepo(t, db, "acme-service")
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "acme-infra", "prod/intranet/application.properties", 0, 1, 10, "",
		"spring.h2.console.enabled=false\n")
	seedTokenIn(t, db, "acme-infra", "prod/intranet/application.properties", "property", "spring.h2.console.enabled", 1)
	seedChunkIn(t, db, "acme-service", "src/main/resources/application.properties", 0, 1, 10, "",
		"spring.h2.console.enabled=true\n")
	seedTokenIn(t, db, "acme-service", "src/main/resources/application.properties", "property", "spring.h2.console.enabled", 1)
	return db, hitID
}

// Config crosses to config only on a word of the question: a key sharing no
// word with it is not a reason to read another repository's settings.
func TestGather_configCrossesToConfigOnlyOnAQuestionWord(t *testing.T) {
	for _, c := range []struct {
		name     string
		question string
		want     bool
	}{
		{"a question about something else", "how is the pet count sent to the ledger", false},
		{"a question naming the key's word", "is the h2 console enabled in production", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, hitID := configToConfig(t)
			got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
				withTerms(c.question).Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
			if err != nil {
				t.Fatalf("Gather: %v", err)
			}
			if has := hasIn(got, "acme-service", "src/main/resources/application.properties"); has != c.want {
				t.Errorf("crossed = %v, want %v: %v", has, c.want, repoPaths(got))
			}
		})
	}
}

// Code reading a key crosses as before, whatever the question says: that is
// the measured digest case, and the code is the reason.
func TestGather_codeReadingAKeyCrossesWithoutAQuestionWord(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "acme-service")
	seedRepo(t, db, "acme-infra")
	hitID := seedChunkIn(t, db, "acme-service", "src/main/java/acme/Job.java", 0, 1, 20, "Job",
		`@Scheduled(cron = "${acme.cron.send-digest}") public void run() {}`)
	seedTokenIn(t, db, "acme-service", "src/main/java/acme/Job.java", "property", "acme.cron.send-digest", 1)
	seedChunkIn(t, db, "acme-infra", "prod/intranet/application.properties", 0, 1, 10, "",
		"acme.cron.send-digest=0 5 * ? * * *\n")
	seedTokenIn(t, db, "acme-infra", "prod/intranet/application.properties", "property", "acme.cron.send-digest", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		withTerms("how is the pet count sent to the ledger").Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(got, "acme-infra", "prod/intranet/application.properties") {
		t.Errorf("sources = %v, want the stage file the code reads", repoPaths(got))
	}
}

// eightKeys is a near side reading or setting eight keys, each set in its own
// infra stage file of about 300 tokens: more than the crossing reserve holds.
func eightKeys(t *testing.T, nearPath string, asConfig bool) (*sql.DB, int64) {
	t.Helper()
	db := gatherDB(t)
	seedRepo(t, db, "acme-service")
	seedRepo(t, db, "acme-infra")
	var body strings.Builder
	for i := 0; i < 8; i++ {
		if asConfig {
			fmt.Fprintf(&body, "acme.key%d=1\n", i)
		} else {
			fmt.Fprintf(&body, `"${acme.key%d}" `, i)
		}
	}
	hitID := seedChunkIn(t, db, "acme-service", nearPath, 0, 1, 20, "", body.String())
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("acme.key%d", i)
		seedTokenIn(t, db, "acme-service", nearPath, "property", key, 1)
		path := fmt.Sprintf("stage%d/application.properties", i)
		seedChunkIn(t, db, "acme-infra", path, 0, 1, 10, "", key+"="+strings.Repeat("x ", 300)+"\n")
		seedTokenIn(t, db, "acme-infra", path, "property", key, 1)
	}
	return db, hitID
}

// propertySpent is what the property landings among got cost.
func propertySpent(got []Source) int {
	spent := 0
	for _, s := range got {
		if isPropertyEdge(s.Reason) {
			spent += estimateTokens(s.Text)
		}
	}
	return spent
}

// Config-to-config landings spend at most the crossing reserve: on a live
// turn they took 22.9k of 24k tokens.
func TestGather_configToConfigLandingsStopAtTheCrossingReserve(t *testing.T) {
	const budget = 6000
	db, hitID := eightKeys(t, "src/main/resources/application.properties", true)
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: budget}).
		withTerms("which acme key").Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	spent := propertySpent(got)
	if spent == 0 {
		t.Fatal("no property landing at all; the fixture no longer crosses")
	}
	if limit := budget / crossingReserve; spent > limit {
		t.Errorf("config-to-config landings spent %d tokens, want at most the reserve %d", spent, limit)
	}
}

// Code reading keys is not capped: every stage the code reads is reported by
// name, and that is the measured case.
func TestGather_codeToConfigLandingsAreNotCapped(t *testing.T) {
	const budget = 6000
	db, hitID := eightKeys(t, "src/main/java/acme/Settings.java", false)
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: budget}).
		withTerms("which acme key").Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if spent := propertySpent(got); spent <= budget/crossingReserve {
		t.Errorf("code-to-config landings spent %d tokens, want past the reserve: they are not capped", spent)
	}
}

// Without question words (the harness, a bare Gather) nothing changes: no
// filter and no cap, so measured numbers stay comparable.
func TestGather_withoutQuestionWordsNothingIsCapped(t *testing.T) {
	const budget = 6000
	db, hitID := eightKeys(t, "src/main/resources/application.properties", true)
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: budget}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if spent := propertySpent(got); spent <= budget/crossingReserve {
		t.Errorf("spent %d tokens, want past the reserve: a bare Gather is not capped", spent)
	}
}

// A question of stopwords alone carries no words, which keeps every crossing
// rather than blocking every one.
func TestGather_aQuestionWithoutWordsKeepsTheCrossing(t *testing.T) {
	db, hitID := configToConfig(t)
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		withTerms("and it?").Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(got, "acme-service", "src/main/resources/application.properties") {
		t.Errorf("sources = %v, want the crossing kept when the question has no words", repoPaths(got))
	}
}

// Run hands the question's words to the gatherer: a live turn's config-to-
// config crossings stop without anyone setting terms by hand.
func TestPipeline_configDoesNotCrossToConfigOnAnUnrelatedQuestion(t *testing.T) {
	db, hitID := configToConfig(t)
	if _, err := db.Exec(`UPDATE repo_state SET project = 'acme'`); err != nil {
		t.Fatalf("set project: %v", err)
	}
	pm, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load: %v", err)
	}
	namesNothing := strings.Replace(appleTVReply, `"repos": ["peeq"]`, `"repos": []`, 1)
	p := NewPipeline(twoStepUpstream(t, namesNothing, "So [1]."),
		&fakeSearch{hits: []retrieve.Hit{hitInFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{projects: pm})

	answer, _, err := p.Run(context.Background(), "How is the pet count sent to the ledger?",
		AudienceDev, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, s := range answer.Sources {
		if s.Repo == "acme-service" {
			t.Errorf("crossed into %s %s on a key the question never names", s.Repo, s.Path)
		}
	}
}

// The words come from the search texts, not the raw question alone: a German
// question never shares a word with an English key, and the understanding's
// code terms do.
func TestPipeline_theUnderstandingsWordsReachTheCrossing(t *testing.T) {
	db, hitID := configToConfig(t)
	if _, err := db.Exec(`UPDATE repo_state SET project = 'acme'`); err != nil {
		t.Fatalf("set project: %v", err)
	}
	pm, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load: %v", err)
	}
	reply := strings.Replace(appleTVReply, `"repos": ["peeq"]`, `"repos": []`, 1)
	reply = strings.Replace(reply, `"AirPlay"`, `"consoleEnabled"`, 1)
	p := NewPipeline(twoStepUpstream(t, reply, "So [1]."),
		&fakeSearch{hits: []retrieve.Hit{hitInFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{projects: pm})

	answer, _, err := p.Run(context.Background(), "Ist die Konsole in Produktion aktiviert?",
		AudienceDev, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	crossed := false
	for _, s := range answer.Sources {
		if s.Repo == "acme-service" {
			crossed = true
		}
	}
	if !crossed {
		t.Error("the code terms name the key's words, and the crossing was still refused")
	}
}

// TestGather_readsTestFilesAfterTheMechanismAcrossTheWholeHop: the ordering
// is per HOP, not per source. Two hits in one hop, the first referencing a
// test and the second the mechanism: with room for one, the mechanism is the
// one taken, whichever hit referenced it.
func TestGather_readsTestFilesAfterTheMechanismAcrossTheWholeHop(t *testing.T) {
	db := gatherDB(t)
	first := seedChunk(t, db, "handler.go", 0, 1, 10, "handle", "func handle() { Authorise() }")
	second := seedChunk(t, db, "other.go", 0, 1, 10, "other", "func other() { Decline() }")
	body := strings.Repeat("x ", 400)
	seedChunk(t, db, "a_test.go", 0, 1, 10, "TestAuthorise", "func Authorise() { "+body+" }")
	seedSymbol(t, db, "a_test.go", "Authorise", 1)
	seedChunk(t, db, "service.go", 0, 1, 10, "Decline", "func Decline() { "+body+" }")
	seedSymbol(t, db, "service.go", "Decline", 1)

	// Room for the two hits and ONE reference under the symbol walk's share.
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 400}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, first), hitFor(t, db, second)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !has(got, "service.go") {
		t.Errorf("sources = %v, want the mechanism taken before the test file of the hop", paths(got))
	}
	if has(got, "a_test.go") {
		t.Errorf("sources = %v, want the test file left for lack of room", paths(got))
	}
}
