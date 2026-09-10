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
