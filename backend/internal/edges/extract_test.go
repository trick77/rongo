package edges

import (
	"testing"
)

// values returns the tokens of one kind, so a test can say what it means
// without walking the slice.
func values(toks []Token, kind Kind) []string {
	var out []string
	for _, t := range toks {
		if t.Kind == kind {
			out = append(out, t.Value)
		}
	}
	return out
}

func has(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

// The three edges of the pinned Sock Shop corpus, each written the way the real
// file writes it. If any of these stops being found, an answer stops being able
// to cross that boundary — so they are here as fixtures rather than as a corpus
// run that needs credentials.
func TestExtractFindsTheProducerSideOfAQueue(t *testing.T) {
	// Given: shipping's configuration and controller, verbatim in shape.
	testee := []byte(`
public class RabbitMqConfiguration {
    final static String queueName = "shipping-task";

    @Bean
    Queue queue() {
        return new Queue(queueName, false);
    }
}
`)

	// When
	got := Extract("RabbitMqConfiguration.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("the producer's queue name was not extracted: %+v", got)
	}
}

func TestExtractFindsTheConsumerSideOfAQueue(t *testing.T) {
	// Given: queue-master, which names the same queue in its own words.
	testee := []byte(`
public class ShippingConsumerConfiguration {
	protected final String queueName = "shipping-task";

	public SimpleMessageListenerContainer container() {
		container.setQueueNames(this.queueName);
	}
}
`)

	// When
	got := Extract("ShippingConsumerConfiguration.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("the consumer's queue name was not extracted: %+v", got)
	}
}

func TestExtractFindsADestinationPassedStraightToTheCall(t *testing.T) {
	// Given: the send that names its queue inline, with no constant.
	testee := []byte(`rabbitTemplate.convertAndSend("shipping-task", shipment);`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("an inline destination was not extracted: %+v", got)
	}
}

func TestExtractFindsBothEndsOfAnHTTPRoute(t *testing.T) {
	// Given: the Java client that calls the route, and the Go server that
	// serves it.
	client := []byte(`return new ServiceUri(new Hostname("payment"), new Domain(domain), "/paymentAuth").toUri();`)
	server := []byte(`r.Methods("POST").Path("/paymentAuth").Handler(httptransport.NewServer(`)

	// When
	gotClient := Extract("OrdersConfigurationProperties.java", client)
	gotServer := Extract("transport.go", server)

	// Then
	if !has(values(gotClient, KindRoute), "/paymentAuth") {
		t.Errorf("the client end of the route was not extracted: %+v", gotClient)
	}
	if !has(values(gotServer, KindRoute), "/paymentAuth") {
		t.Errorf("the server end of the route was not extracted: %+v", gotServer)
	}
}

func TestExtractFindsAnExpressRoute(t *testing.T) {
	// Given: the Node front-end, both as a server and as a client.
	testee := []byte(`
  app.post("/orders", function(req, res, next) {
    request({uri: endpoints.ordersUrl + '/orders', method: 'POST'});
  });
`)

	// When
	got := Extract("index.js", testee)

	// Then
	if !has(values(got, KindRoute), "/orders") {
		t.Fatalf("the Express route was not extracted: %+v", got)
	}
}

func TestExtractIgnoresRoutesEveryServiceHas(t *testing.T) {
	// Given: the health endpoint, which exists in all eight repositories.
	testee := []byte(`@RequestMapping(method = RequestMethod.GET, path = "/health")`)

	// When
	got := Extract("HealthCheckController.java", testee)

	// Then: no token, because an edge on /health would join every repository to
	// every other and mean nothing.
	if len(values(got, KindRoute)) != 0 {
		t.Fatalf("/health was recorded as an edge: %+v", got)
	}
}

func TestExtractIgnoresProseAndDocumentation(t *testing.T) {
	// Given: a markdown file that talks about the very same route.
	testee := []byte("The order service posts to `/paymentAuth` when checking out.\n")

	// When
	got := Extract("README.md", testee)

	// Then: documentation is a claim about the code, not the code.
	if len(got) != 0 {
		t.Fatalf("a route was extracted from prose: %+v", got)
	}
}

func TestExtractRefusesASentenceAsADestination(t *testing.T) {
	// Given: a messaging line whose literal is a log message.
	testee := []byte(`log.info("Unable to add to queue (the queue is probably down)"); rabbitTemplate.convertAndSend(q, m);`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then: a sentence is not a queue name.
	for _, v := range values(got, KindDestination) {
		if v != "" && len(v) > 40 {
			t.Fatalf("a sentence was recorded as a destination: %q", v)
		}
	}
}

func TestExtractDeduplicatesWithinAFile(t *testing.T) {
	// Given: the same route declared twice, as controllers often do.
	testee := []byte(`
	@RequestMapping(value = "/shipping", method = RequestMethod.GET)
	@RequestMapping(value = "/shipping", method = RequestMethod.POST)
`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then
	if n := len(values(got, KindRoute)); n != 1 {
		t.Fatalf("expected one route, got %d: %+v", n, got)
	}
}

func TestExtractFindsAPropertyPlaceholderInJVMCode(t *testing.T) {
	// Given: a Spring job whose schedule is a property, and a value with a
	// default. The key is the literal the deployed configuration repeats.
	testee := []byte(`
@Component
public class JobSendDigest {
    @Scheduled(cron = "${acme.cron.send-digest}")
    public void run() {}

    @Value("${acme.mail.retries:3}")
    private int retries;
}
`)

	// When
	toks := Extract("src/main/java/acme/JobSendDigest.java", testee)

	// Then
	got := values(toks, KindProperty)
	if !has(got, "acme.cron.send-digest") {
		t.Errorf("placeholder key not recorded: %v", got)
	}
	if !has(got, "acme.mail.retries") {
		t.Errorf("key with default not recorded, or recorded with its default: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want exactly the two keys", got)
	}
	for _, tk := range toks {
		if tk.Value == "acme.cron.send-digest" && tk.Line != 4 {
			t.Errorf("line = %d, want 4", tk.Line)
		}
	}
}

func TestExtractReadsThePropertyKeysOfAPropertiesFile(t *testing.T) {
	// Given: a stage's properties, with a comment, a blank, a placeholder
	// value and a redacted one. Every key is a token; comments are not.
	testee := []byte(`# Cron
acme.cron.send-digest=0 0 * ? * * *

acme.mail.retries = 5
db.password=<redacted>
! old-style comment=x
acme.mail.host: mail.example.invalid
`)

	// When
	toks := Extract("prod/intranet/application.properties", testee)

	// Then
	got := values(toks, KindProperty)
	want := []string{"acme.cron.send-digest", "acme.mail.retries", "db.password", "acme.mail.host"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, tk := range toks {
		if tk.Value == "acme.mail.retries" && tk.Line != 4 {
			t.Errorf("line = %d, want 4", tk.Line)
		}
	}
	if n := len(values(toks, KindRoute)) + len(values(toks, KindDestination)); n != 0 {
		t.Errorf("a properties file yielded %d route or destination tokens", n)
	}
}

func TestExtractDoesNotReadTemplateInterpolationAsAProperty(t *testing.T) {
	// Given: TypeScript with a template literal and a Go file with a shell
	// style placeholder. Neither is a Spring property, and recording one
	// would join a front-end to whatever repository has a "cart.id" key.
	for _, c := range []struct{ path, body string }{
		{"ui/src/cart.ts", "const url = `${base}/carts/${cart.id}/merge`;\nfetch(url);\n"},
		{"cmd/x/main.go", "s := \"${acme.cron.send-digest}\"\n"},
		{"README.md", "Set `${acme.cron.send-digest}` to change the schedule.\n"},
		{"values.yaml", "acme.cron.send-digest: 0 0 * ? * * *\n"},
	} {
		if got := values(Extract(c.path, []byte(c.body)), KindProperty); len(got) != 0 {
			t.Errorf("%s: recorded %v as properties", c.path, got)
		}
	}
}

func TestExtractIgnoresAPlaceholderWithoutADotOrDash(t *testing.T) {
	// Given: "${id}" inside a Java string is far more often a message
	// template than a property; a property key has at least one segment.
	testee := []byte(`String s = "${id}"; String t = "${PORT}";`)

	// When
	got := values(Extract("A.java", testee), KindProperty)

	// Then
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}
