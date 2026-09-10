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
