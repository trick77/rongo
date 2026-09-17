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
	// A sibling class reading the same key is the walk's business, not a
	// landing: at home only a properties file is a neighbour.
	seedFileWithTokens(t, db, "acme-service", "OtherJob.java", "other", nil,
		[]Token{{Kind: KindProperty, Value: "acme.cron.send-digest", Line: 9}})

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
		if n.Path == "Job.java" || n.Path == "OtherJob.java" {
			t.Errorf("a class of the same repository came back as a neighbour: %+v", n)
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
	if len(got) != 3 {
		t.Errorf("want the three service files across the boundary, got %+v", got)
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

func TestNeighboursNeverCrossOnASharedLink(t *testing.T) {
	// Given: two user interfaces linking to the same portal. That they both
	// point at a third application says nothing about each other, and a
	// crossing here would spend the reserve on it.
	db := edgeDB(t, "claims-ui", "policy-ui")
	seedFileWithTokens(t, db, "claims-ui", "nav.html", "a", nil,
		[]Token{{Kind: KindLink, Value: "https://portal.example.ch", Line: 1}})
	seedFileWithTokens(t, db, "policy-ui", "nav.html", "b", nil,
		[]Token{{Kind: KindLink, Value: "https://portal.example.ch", Line: 2}})

	// When
	got, err := Neighbours(context.Background(), db, "claims-ui", "nav.html")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	holders, err := Holders(context.Background(), db, KindLink, "https://portal.example.ch")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Errorf("a link was crossed: %+v", got)
	}
	if len(holders) != 0 {
		t.Errorf("a link was held: %+v", holders)
	}
}

func TestInRepoListsEveryLinkOfOneRepository(t *testing.T) {
	// Given: two files of one UI with links, a second UI with its own, and
	// a parked one.
	db := edgeDB(t, "claims-ui", "policy-ui", "old-ui")
	seedFileWithTokens(t, db, "claims-ui", "b/nav.html", "a", nil,
		[]Token{{Kind: KindLink, Value: "https://portal.example.ch", Line: 3}, {Kind: KindRoute, Value: "/api/x", Line: 4}})
	seedFileWithTokens(t, db, "claims-ui", "a/open.ts", "b", nil,
		[]Token{{Kind: KindLink, Value: "${environment.portalUrl}/claims", Line: 9}})
	seedFileWithTokens(t, db, "policy-ui", "nav.html", "c", nil,
		[]Token{{Kind: KindLink, Value: "https://elsewhere.example.ch", Line: 1}})
	seedFileWithTokens(t, db, "old-ui", "nav.html", "d", nil,
		[]Token{{Kind: KindLink, Value: "https://old.example.ch", Line: 1}})
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'old-ui'`); err != nil {
		t.Fatal(err)
	}

	// When
	got, err := InRepo(context.Background(), db, "claims-ui", KindLink)
	if err != nil {
		t.Fatalf("InRepo: %v", err)
	}
	parked, err := InRepo(context.Background(), db, "old-ui", KindLink)
	if err != nil {
		t.Fatalf("InRepo: %v", err)
	}

	// Then: that repository's links only, in path order, the route left out.
	if len(got) != 2 || got[0].Path != "a/open.ts" || got[0].Line != 9 || got[1].Path != "b/nav.html" || got[1].Line != 3 {
		t.Errorf("wrong census: %+v", got)
	}
	if len(parked) != 0 {
		t.Errorf("a parked repository answered: %+v", parked)
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
