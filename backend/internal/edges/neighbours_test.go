package edges

import (
	"context"
	"testing"
)

func TestNeighboursCrossesOnASharedDestination(t *testing.T) {
	// Given: a producer and a consumer in different repositories, sharing
	// nothing but the queue name.
	db := edgeDB(t, "shipping", "queue-master")
	seedFileWithTokens(t, db, "shipping", "Controller.java", "send it", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 40}})
	seedFileWithTokens(t, db, "queue-master", "Consumer.java", "receive it", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 15}})

	// When
	got, err := Neighbours(context.Background(), db, "shipping", "Controller.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then
	if len(got) != 1 {
		t.Fatalf("expected one neighbour, got %+v", got)
	}
	if got[0].Repo != "queue-master" || got[0].Value != "shipping-task" || got[0].Line != 15 {
		t.Errorf("wrong neighbour: %+v", got[0])
	}
}

func TestNeighboursStaysOutOfItsOwnRepository(t *testing.T) {
	// Given: two files in ONE repository naming the same queue. Inside a
	// repository the symbol walk already connects code; this table exists for
	// the boundary it cannot cross.
	db := edgeDB(t, "shipping")
	seedFileWithTokens(t, db, "shipping", "A.java", "a", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 1}})
	seedFileWithTokens(t, db, "shipping", "B.java", "b", nil,
		[]Token{{Kind: KindDestination, Value: "shipping-task", Line: 2}})

	// When
	got, err := Neighbours(context.Background(), db, "shipping", "A.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("a same-repository match was returned: %+v", got)
	}
}

func TestNeighboursFollowsAPropertyKeyInsideItsOwnRepositoryToo(t *testing.T) {
	// Given: a job reading a key, the repository's own defaults file setting
	// it, and an infrastructure repository setting it per stage. A properties
	// file has no symbol, so the walk that connects code inside a repository
	// never reaches it; the key is the only link, on both sides of the
	// boundary.
	db := edgeDB(t, "acme-service", "acme-infra")
	seedFileWithTokens(t, db, "acme-service", "Job.java", "job", nil,
		[]Token{{Kind: KindProperty, Value: "acme.cron.send-digest", Line: 4}})
	seedFileWithTokens(t, db, "acme-service", "src/main/resources/application-default.properties", "defaults", nil,
		[]Token{{Kind: KindProperty, Value: "acme.cron.send-digest", Line: 28}})
	seedFileWithTokens(t, db, "acme-infra", "prod/application.properties", "prod", nil,
		[]Token{{Kind: KindProperty, Value: "acme.cron.send-digest", Line: 44}})

	// When
	got, err := Neighbours(context.Background(), db, "acme-service", "Job.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then: the defaults file and the stage file, never the job itself.
	if len(got) != 2 {
		t.Fatalf("expected the defaults file and the stage file, got %+v", got)
	}
	if got[0].Repo != "acme-infra" || got[1].Path != "src/main/resources/application-default.properties" {
		t.Errorf("neighbours = %+v", got)
	}
	for _, n := range got {
		if n.Path == "Job.java" {
			t.Errorf("the file itself came back as its own neighbour: %+v", n)
		}
	}

	// From a properties file the home exception does not apply: two stage
	// files share every key, and joining them would spend the reserve on
	// nothing. The stage file still crosses to the service's files.
	seedFileWithTokens(t, db, "acme-infra", "intg/application.properties", "intg", nil,
		[]Token{{Kind: KindProperty, Value: "acme.cron.send-digest", Line: 44}})
	got, err = Neighbours(context.Background(), db, "acme-infra", "prod/application.properties")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	for _, n := range got {
		if n.Repo == "acme-infra" {
			t.Errorf("a stage file was joined to its sibling stage: %+v", n)
		}
	}
	if len(got) != 2 {
		t.Errorf("want the two service files across the boundary, got %+v", got)
	}
}

func TestNeighboursDropsATokenTheWholeEstateShares(t *testing.T) {
	// Given: a route served by four repositories. That is a convention, and an
	// edge on it would join everything to everything.
	repos := []string{"a", "b", "c", "d"}
	db := edgeDB(t, repos...)
	for _, r := range repos {
		seedFileWithTokens(t, db, r, "X.java", "x", nil,
			[]Token{{Kind: KindRoute, Value: "/common", Line: 1}})
	}

	// When
	got, err := Neighbours(context.Background(), db, "a", "X.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("a token spanning four repositories produced edges: %+v", got)
	}
}

func TestNeighboursNeverMatchesARouteToAQueueOfTheSameName(t *testing.T) {
	// Given: the same string in both namespaces, in two repositories.
	db := edgeDB(t, "one", "two")
	seedFileWithTokens(t, db, "one", "A.java", "a", nil,
		[]Token{{Kind: KindRoute, Value: "orders", Line: 1}})
	seedFileWithTokens(t, db, "two", "B.java", "b", nil,
		[]Token{{Kind: KindDestination, Value: "orders", Line: 1}})

	// When
	got, err := Neighbours(context.Background(), db, "one", "A.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Fatalf("a route was matched to a queue: %+v", got)
	}
}

func TestNeighboursOfAnUnknownFileIsEmpty(t *testing.T) {
	db := edgeDB(t, "one")

	got, err := Neighbours(context.Background(), db, "one", "nope.java")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected nothing for an unknown file: %+v", got)
	}
}
