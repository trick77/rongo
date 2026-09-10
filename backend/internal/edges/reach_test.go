package edges

import (
	"context"
	"testing"
)

// stepOf returns how a file was reached, or "" if it was not.
func stepOf(got []Reached, repo, path string) Step {
	for _, r := range got {
		if r.Repo == repo && r.Path == path {
			return r.Step
		}
	}
	return ""
}

func TestReachTakesOneStepInlandBeforeCrossing(t *testing.T) {
	// Given: the controller mentions its configuration class, and only the
	// configuration class carries the route.
	db := edgeDB(t, "orders", "payment")
	seedFileWithTokens(t, db, "orders", "OrdersController.java",
		"class OrdersController { OrdersConfigurationProperties config; }",
		[]string{"OrdersController"}, nil)
	seedFileWithTokens(t, db, "orders", "OrdersConfigurationProperties.java",
		"class OrdersConfigurationProperties { paymentUri }",
		[]string{"OrdersConfigurationProperties"},
		[]Token{{Kind: KindRoute, Value: "/paymentAuth", Line: 12}})
	seedFileWithTokens(t, db, "payment", "transport.go",
		"func MakeHTTPHandler() { }",
		[]string{"MakeHTTPHandler"},
		[]Token{{Kind: KindRoute, Value: "/paymentAuth", Line: 29}})

	// When
	got, err := Reach(context.Background(), db, "orders", "OrdersController.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then: the config file as an in-repo hop, and the far service across it.
	if s := stepOf(got, "orders", "OrdersConfigurationProperties.java"); s != StepInRepoBefore {
		t.Errorf("the configuration class was reached as %q, want %q", s, StepInRepoBefore)
	}
	if s := stepOf(got, "payment", "transport.go"); s != StepEdge {
		t.Errorf("the far service was reached as %q, want %q", s, StepEdge)
	}
}

func TestReachTakesOneStepInlandAfterCrossing(t *testing.T) {
	// Given: the far side's literal is in a configuration class, and the file
	// that does the work carries none — it is only named by that class.
	db := edgeDB(t, "shipping", "queue-master")
	seedFileWithTokens(t, db, "shipping", "ShippingController.java", "convertAndSend", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 40}})
	seedFileWithTokens(t, db, "queue-master", "ConsumerConfiguration.java",
		"class ConsumerConfiguration { ShippingTaskHandler handler; }",
		[]string{"ConsumerConfiguration"},
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 15}})
	seedFileWithTokens(t, db, "queue-master", "ShippingTaskHandler.java",
		"class ShippingTaskHandler { handleMessage }",
		[]string{"ShippingTaskHandler"}, nil)

	// When
	got, err := Reach(context.Background(), db, "shipping", "ShippingController.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then
	if s := stepOf(got, "queue-master", "ConsumerConfiguration.java"); s != StepEdge {
		t.Errorf("the crossing landed as %q, want %q", s, StepEdge)
	}
	if s := stepOf(got, "queue-master", "ShippingTaskHandler.java"); s != StepInRepoAfter {
		t.Errorf("the handler was reached as %q, want %q", s, StepInRepoAfter)
	}
}

func TestReachRecordsTheTokenItCrossedOn(t *testing.T) {
	// Given: one boundary.
	db := edgeDB(t, "a", "b")
	seedFileWithTokens(t, db, "a", "A.java", "a", nil,
		[]Token{{Kind: KindDestination, Value: "orders-events", Line: 3}})
	seedFileWithTokens(t, db, "b", "B.java", "b", nil,
		[]Token{{Kind: KindDestination, Value: "orders-events", Line: 9}})

	// When
	got, err := Reach(context.Background(), db, "a", "A.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	// Then: the trail says what carried it, which is what an answer has to be
	// able to explain.
	for _, r := range got {
		if r.Repo == "b" {
			if r.Via != "orders-events" || r.Kind != KindDestination || r.Through != "A.java" {
				t.Fatalf("the trail is not readable: %+v", r)
			}
			return
		}
	}
	t.Fatal("the boundary was not crossed")
}

func TestReachNeverReturnsItsOwnStart(t *testing.T) {
	db := edgeDB(t, "a", "b")
	seedFileWithTokens(t, db, "a", "A.java", "a", []string{"Alpha"},
		[]Token{{Kind: KindRoute, Value: "/x", Line: 1}})
	seedFileWithTokens(t, db, "b", "B.java", "Alpha is mentioned here", nil,
		[]Token{{Kind: KindRoute, Value: "/x", Line: 1}})

	got, err := Reach(context.Background(), db, "a", "A.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	for _, r := range got {
		if r.Repo == "a" && r.Path == "A.java" {
			t.Fatalf("the start file came back as a result: %+v", r)
		}
	}
}

func TestReachReportsEachFileOnce(t *testing.T) {
	// Given: a far file reachable both directly and through a sibling, which is
	// the ordinary case once both ends take a step inland.
	db := edgeDB(t, "a", "b")
	seedFileWithTokens(t, db, "a", "Start.java", "class Start { Sibling s; }", []string{"Start"},
		[]Token{{Kind: KindRoute, Value: "/shared", Line: 1}})
	seedFileWithTokens(t, db, "a", "Sibling.java", "class Sibling { }", []string{"Sibling"},
		[]Token{{Kind: KindRoute, Value: "/shared", Line: 1}})
	seedFileWithTokens(t, db, "b", "Far.java", "far", nil,
		[]Token{{Kind: KindRoute, Value: "/shared", Line: 1}})

	got, err := Reach(context.Background(), db, "a", "Start.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	seen := 0
	for _, r := range got {
		if r.Repo == "b" && r.Path == "Far.java" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the far file appears %d times, want once: %+v", seen, got)
	}
}

func TestReachOfAnIsolatedFileIsEmpty(t *testing.T) {
	// Given: a file naming nothing and named by nothing.
	db := edgeDB(t, "lonely")
	seedFileWithTokens(t, db, "lonely", "Alone.java", "nothing here", nil, nil)

	got, err := Reach(context.Background(), db, "lonely", "Alone.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an isolated file reached something: %+v", got)
	}
}

func TestReachOfAnUnknownFileIsEmpty(t *testing.T) {
	db := edgeDB(t, "one")

	got, err := Reach(context.Background(), db, "one", "missing.java")
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected nothing for an unknown file: %+v", got)
	}
}
