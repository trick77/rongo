package ask

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
	for i, stage := range []string{"syst", "intg", "prod"} {
		p := stage + "/intranet/application.properties"
		seedChunkIn(t, db, "acme-infra", p, 0, 1, 10, "",
			"# stage "+stage+"\nacme.cron.send-digest=0 "+string(rune('0'+i))+" * ? * * *\n")
		seedTokenIn(t, db, "acme-infra", p, "property", "acme.cron.send-digest", 2)
	}

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
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
