package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/usage"
)

// fixture is the shape of models.dev's api.json, cut down to what the
// resolver reads: a provider's api URL and each model's cost. Modelled on
// the real file: "openai" has no api field; the token plan lists the
// deployments at zero where "xiaomi" prices them; the vendor and its coding
// plan share a host and differ by path; two aggregators share one URL byte
// for byte; the local servers differ by port only. Everything but "xiaomi"
// is matched by URL, which is the embedding endpoint's path through the
// resolver, so those providers carry an embedding model too.
const fixture = `{
  "xiaomi": {
    "id": "xiaomi", "name": "Xiaomi", "api": "https://api.xiaomimimo.com/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0.435, "output": 0.87, "cache_read": 0.0036}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0.14, "output": 0.28}},
      "mimo-v2.5-tts": {"id": "mimo-v2.5-tts"}
    }
  },
  "xiaomi-token-plan-ams": {
    "id": "xiaomi-token-plan-ams", "api": "https://token-plan-ams.xiaomimimo.com/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0, "output": 0}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0, "output": 0}}
    }
  },
  "reseller": {
    "id": "reseller", "api": "https://llm.reseller.example/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 2.175, "output": 4.35}}
    }
  },
  "vendor": {
    "id": "vendor", "api": "https://api.vendor.example/api/paas/v4",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0.02, "output": 0}}
    }
  },
  "vendor-coding-plan": {
    "id": "vendor-coding-plan", "api": "https://api.vendor.example/api/coding/paas/v4",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0, "output": 0}}
    }
  },
  "gateway": {
    "id": "gateway", "api": "https://api.gateway.example/v1",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0.02, "output": 0}}
    }
  },
  "gateway-providers": {
    "id": "gateway-providers", "api": "https://api.gateway.example/v1",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0.05, "output": 0}}
    }
  },
  "local-a": {
    "id": "local-a", "api": "http://127.0.0.1:1337/v1",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0, "output": 0}}
    }
  },
  "local-b": {
    "id": "local-b", "api": "http://127.0.0.1:1234/v1",
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0, "output": 0}}
    }
  },
  "openai": {
    "id": "openai", "name": "OpenAI", "api": null,
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0.02, "output": 0}}
    }
  }
}`

const (
	xiaomiURL = "https://api.xiaomimimo.com/v1"
	openaiURL = "https://api.openai.com/v1"
	embed     = "text-embedding-3-small"
)

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchFixture(t *testing.T) Registry {
	t.Helper()
	srv := serve(t, fixture, http.StatusOK)
	reg, err := Fetch(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return reg
}

func TestFetch_decodesProvidersEndpointsAndCosts(t *testing.T) {
	reg := fetchFixture(t)
	if len(reg) != 10 {
		t.Fatalf("decoded %d providers, want 10", len(reg))
	}
	if reg["xiaomi"].API != xiaomiURL {
		t.Errorf("xiaomi api = %q", reg["xiaomi"].API)
	}
	if reg["openai"].API != "" {
		t.Errorf("a null api must decode as empty, got %q", reg["openai"].API)
	}
	if c := reg["xiaomi"].Models["mimo-v2.5-pro"].Cost; c == nil || c.Input != 0.435 || c.Output != 0.87 {
		t.Errorf("mimo-v2.5-pro cost = %+v", c)
	}
	if reg["xiaomi"].Models["mimo-v2.5-tts"].Cost != nil {
		t.Error("a model without a cost block must carry no cost")
	}
}

func TestFetch_reportsAFailedOrMalformedRegistry(t *testing.T) {
	srv := serve(t, "nope", http.StatusBadGateway)
	if _, err := Fetch(context.Background(), srv.URL, srv.Client()); err == nil {
		t.Fatal("a 502 must be an error")
	}
	srv2 := serve(t, "{not json", http.StatusOK)
	if _, err := Fetch(context.Background(), srv2.URL, srv2.Client()); err == nil {
		t.Fatal("malformed JSON must be an error")
	}
}

func TestResolve_pricesTheDeploymentsFromMiMosAPIAndEmbedFromItsEndpoint(t *testing.T) {
	// Given OpenAI embeddings
	reg := fetchFixture(t)

	// When
	prices, warnings := Resolve(reg, openaiURL, embed)

	// Then every model rongo calls is priced, and nothing is warned about
	if p := prices[llm.ProDeployment]; p.In != 0.435 || p.Out != 0.87 {
		t.Errorf("pro = %+v, want 0.435/0.87", p)
	}
	if p := prices[llm.ShortGateDeployment]; p.In != 0.14 || p.Out != 0.28 {
		t.Errorf("gate = %+v, want 0.14/0.28", p)
	}
	if p := prices[embed]; p.In != 0.02 || p.Out != 0 {
		t.Errorf("embed = %+v, want 0.02/0", p)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestResolve_theDeploymentsAreMiMosPriceNotTheTokenPlansZero(t *testing.T) {
	reg := fetchFixture(t)

	// The fixture lists the token plan at 0/0, the way models.dev really
	// does. Whichever endpoint the deployments are called at, they are worth
	// what MiMo charges for them: a flat plan showed a thread as $0.000,
	// which reads as broken rather than as free.
	prices, warnings := Resolve(reg, "", "")

	if p := prices[llm.ProDeployment]; p.In != 0.435 || p.Out != 0.87 {
		t.Errorf("pro = %+v, want MiMo's 0.435/0.87", p)
	}
	if p := prices[llm.ShortGateDeployment]; p.In != 0.14 || p.Out != 0.28 {
		t.Errorf("gate = %+v, want MiMo's 0.14/0.28", p)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestResolve_thePathTellsACodingPlanFromPayAsYouGo(t *testing.T) {
	reg := fetchFixture(t)

	// The vendor's two contracts share a host; the path is the difference,
	// and the flat-rate one must not be billed at the metered price.
	prices, warnings := Resolve(reg, "https://api.vendor.example/api/coding/paas/v4", embed)
	if p, ok := prices[embed]; !ok || p.In != 0 {
		t.Errorf("coding plan embed = %+v (present %v), want 0 present; warnings %v", p, ok, warnings)
	}
	prices, _ = Resolve(reg, "https://api.vendor.example/api/paas/v4", embed)
	if p := prices[embed]; p.In != 0.02 {
		t.Errorf("pay-as-you-go embed = %+v, want 0.02", p)
	}
}

func TestResolve_thePortTellsLocalServersApart(t *testing.T) {
	reg := fetchFixture(t)

	// Two registry entries on 127.0.0.1; a third port is nobody's.
	if prices, _ := Resolve(reg, "http://127.0.0.1:1234/v1", embed); len(prices) != 3 {
		t.Errorf("the registry's own port must match, got %v", prices)
	}
	prices, warnings := Resolve(reg, "http://127.0.0.1:8000/v1", embed)
	if len(prices) != 0 {
		t.Errorf("an unlisted port must not borrow a neighbour's price, got %v", prices)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "127.0.0.1:8000") {
		t.Errorf("warnings = %v, want one naming the endpoint with its port", warnings)
	}
}

func TestResolve_providersSharingOneEndpointMustAgreeOrNothingIsPriced(t *testing.T) {
	reg := fetchFixture(t)

	// The two gateway entries price the embedding model differently: the
	// endpoint alone does not say which contract applies.
	prices, warnings := Resolve(reg, "https://api.gateway.example/v1", embed)
	if len(prices) != 0 {
		t.Errorf("a disagreement must price nothing, got %v", prices)
	}
	if len(warnings) < 1 || !strings.Contains(warnings[0], "differently") || !strings.Contains(warnings[0], embed) {
		t.Errorf("warnings = %v, want the disagreement named", warnings)
	}
}

func TestResolve_anUnknownEndpointIsTokensOnlyForTheWholeTable(t *testing.T) {
	reg := fetchFixture(t)

	// The deployments alone would resolve, and a total missing the embedding
	// calls would read as the cost of the turn.
	prices, warnings := Resolve(reg, "http://embed.internal.example/v1", embed)

	if len(prices) != 0 {
		t.Errorf("an unknown embed endpoint must empty the whole table, got %v", prices)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "embed.internal.example") || !strings.Contains(warnings[1], "tokens only") {
		t.Errorf("warnings = %v, want the endpoint named and the consequence stated", warnings)
	}
}

func TestResolve_aMatchedProviderMissingTheModelEmptiesTheTable(t *testing.T) {
	reg := fetchFixture(t)

	// OpenAI lists the small embedding model, not the large one: a total
	// without the embedding calls would undercount every turn.
	prices, warnings := Resolve(reg, openaiURL, "text-embedding-3-large")

	if len(prices) != 0 {
		t.Errorf("one unpriced model must empty the table, got %v", prices)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "text-embedding-3-large") || !strings.Contains(warnings[1], "tokens only") {
		t.Errorf("warnings = %v, want the gap and the consequence", warnings)
	}
}

func TestResolve_anEmptyEmbedEndpointIsSkippedSilently(t *testing.T) {
	reg := fetchFixture(t)
	prices, warnings := Resolve(reg, "", embed)
	if _, ok := prices[embed]; ok {
		t.Error("nothing embeds without an endpoint, so nothing is priced")
	}
	if len(prices) != 2 || len(warnings) != 0 {
		t.Errorf("prices = %v, warnings = %v: indexing off is a configured state", prices, warnings)
	}
}

func TestTable_isEmptyUntilTheRegistryAnswers(t *testing.T) {
	table := NewTable()
	if got := table.Prices(); len(got) != 0 {
		t.Fatalf("before any fetch the table must be empty, got %v", got)
	}

	srv := serve(t, fixture, http.StatusOK)
	err := table.Refresh(context.Background(), Source{
		URL: srv.URL, Client: srv.Client(),
		EmbedBaseURL: openaiURL, EmbedModel: embed,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := table.Prices()
	if p := got[llm.ProDeployment]; p.In != 0.435 || p.Out != 0.87 {
		t.Errorf("pro = %+v, want the registry's 0.435/0.87", p)
	}
	if p := got[llm.ShortGateDeployment]; p.In != 0.14 {
		t.Errorf("gate = %+v, want the registry's 0.14", p)
	}
	if p := got[embed]; p.In != 0.02 {
		t.Errorf("embed = %+v, want the registry's 0.02", p)
	}
}

func TestNewFixedTable_servesWhatItWasGivenAndCopiesIt(t *testing.T) {
	given := usage.Prices{llm.ProDeployment: usage.Price{In: 1, Out: 2}}
	table := NewFixedTable(given)
	given[llm.ProDeployment] = usage.Price{In: 9, Out: 9}
	if p := table.Prices()[llm.ProDeployment]; p.In != 1 || p.Out != 2 {
		t.Errorf("pro = %+v, want the 1/2 it was built with", p)
	}
}

func TestTable_aNilTableIsAnEmptyOne(t *testing.T) {
	var table *Table
	if got := table.Prices(); got == nil || len(got) != 0 {
		t.Errorf("nil table prices = %v, want empty", got)
	}
}

func TestTable_aFailedRefreshLeavesTheTableUnchanged(t *testing.T) {
	table := NewTable()
	good := serve(t, fixture, http.StatusOK)
	src := Source{URL: good.URL, Client: good.Client()}
	if err := table.Refresh(context.Background(), src); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	bad := serve(t, "", http.StatusInternalServerError)
	src.URL, src.Client = bad.URL, bad.Client()
	if err := table.Refresh(context.Background(), src); err == nil {
		t.Fatal("a failed fetch must report")
	}
	if p := table.Prices()[llm.ProDeployment]; p.In != 0.435 {
		t.Errorf("the last good table must survive a failed refresh, got %+v", p)
	}
}

func TestTable_runFetchesOnceAndStopsWithTheContext(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()

	table := NewTable()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		table.Run(ctx, Source{URL: srv.URL, Client: srv.Client()})
		close(done)
	}()
	// Run applies the first table before it sleeps, so the goroutine can be
	// cancelled as soon as the price is visible.
	deadline := time.After(5 * time.Second)
	for table.Prices()[llm.ProDeployment].In == 0 {
		select {
		case <-done:
			t.Fatal("Run returned before the context was cancelled")
		case <-deadline:
			t.Fatal("the first fetch never priced the deployment")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done
	if hits != 1 {
		t.Errorf("registry fetched %d times, want once", hits)
	}
}

func TestStart_anEmptyURLMeansNoWorkerAndAnEmptyTable(t *testing.T) {
	var workers sync.WaitGroup
	table := Start(context.Background(), &workers, Source{URL: ""})
	workers.Wait() // returns at once: nothing was started
	if got := table.Prices(); len(got) != 0 {
		t.Errorf("prices = %v, want tokens only", got)
	}
}

func TestStart_withAURLRunsTheWorkerUntilTheContextEnds(t *testing.T) {
	srv := serve(t, fixture, http.StatusOK)
	var workers sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	table := Start(ctx, &workers, Source{URL: srv.URL, Client: srv.Client()})
	deadline := time.Now().Add(5 * time.Second)
	for table.Prices()[llm.ProDeployment].In == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the worker never priced the deployment")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	workers.Wait()
	if p := table.Prices()[llm.ProDeployment]; p.In != 0.435 {
		t.Errorf("pro = %+v, want the registry's 0.435", p)
	}
}
