package edges

import "testing"

// The two false-destination shapes a review found in this corpus. Both produced
// tokens that would have crossed a repository boundary on a coincidence.
func TestExtractRefusesAResponseBodyAsAQueueName(t *testing.T) {
	// Given: Express writing a response, which is not a message send.
	testee := []byte(`
  app.post("/orders", function(req, res) {
    res.send("done");
    res.send("error");
  });
`)

	// When
	got := Extract("index.js", testee)

	// Then: two services doing this sit under the spread ceiling and would
	// otherwise link on "done".
	for _, v := range values(got, KindDestination) {
		if v == "done" || v == "error" {
			t.Fatalf("a response body was recorded as a destination: %q", v)
		}
	}
}

func TestExtractRefusesANameThatMerelyContainsAKeyword(t *testing.T) {
	// Given: a variable whose name contains "exchange" without being one.
	testee := []byte(`
	String exchangeRate = "USD";
	String queueingPolicy = "fifo";
`)

	// When
	got := Extract("Rates.java", testee)

	// Then
	if len(values(got, KindDestination)) != 0 {
		t.Fatalf("a non-destination variable produced tokens: %+v", got)
	}
}

func TestExtractStillReadsANameThatEndsWithTheKeyword(t *testing.T) {
	// Given: the real shapes, which must keep working after the tightening.
	for _, in := range []string{
		`final static String queueName = "shipping-task";`,
		`private String topic = "orders-events";`,
		`val exchangeName = "shipping-task-exchange"`,
		`String routingKey = "order.created";`,
	} {
		got := Extract("X.java", []byte(in))
		if len(values(got, KindDestination)) == 0 {
			t.Errorf("a real destination was lost: %q", in)
		}
	}
}
