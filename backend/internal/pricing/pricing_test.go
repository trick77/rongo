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
// the real file: "openai" has no api field; "reseller" prices the
// deployments differently, which is why the endpoint decides; the vendor
// and its coding plan share a host and differ by path; two aggregators
// share one URL byte for byte; three local servers differ by port only.
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
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0.6, "output": 2.2}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0.1, "output": 0.3}}
    }
  },
  "vendor-coding-plan": {
    "id": "vendor-coding-plan", "api": "https://api.vendor.example/api/coding/paas/v4",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0, "output": 0}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0, "output": 0}}
    }
  },
  "gateway": {
    "id": "gateway", "api": "https://api.gateway.example/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0.435, "output": 0.87}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0.14, "output": 0.28}}
    }
  },
  "gateway-providers": {
    "id": "gateway-providers", "api": "https://api.gateway.example/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0.435, "output": 0.87}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0.5, "output": 1}}
    }
  },
  "local-a": {
    "id": "local-a", "api": "http://127.0.0.1:1337/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0, "output": 0}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0, "output": 0}}
    }
  },
  "local-b": {
    "id": "local-b", "api": "http://127.0.0.1:1234/v1",
    "models": {
      "mimo-v2.5-pro": {"id": "mimo-v2.5-pro", "cost": {"input": 0, "output": 0}},
      "mimo-v2.5": {"id": "mimo-v2.5", "cost": {"input": 0, "output": 0}}
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

func TestResolve_pricesEveryModelFromTheProviderServingTheEndpoint(t *testing.T) {
	// Given the pay-as-you-go endpoint and OpenAI embeddings
	reg := fetchFixture(t)

	// When
	prices, warnings := Resolve(reg, xiaomiURL, openaiURL, embed)

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

func TestResolve_aTokenPlanIsPricedAtZeroNotLeftOut(t *testing.T) {
	reg := fetchFixture(t)

	// A token plan is flat rate: zero is a price, present in the table, so
	// the browser shows a real $0 rather than nothing. The trailing slash
	// is what a copied URL often carries.
	prices, warnings := Resolve(reg, "https://token-plan-ams.xiaomimimo.com/v1/", "", "")
	p, ok := prices[llm.ProDeployment]
	if !ok || p.In != 0 || p.Out != 0 {
		t.Errorf("token plan pro = %+v (present %v), want 0/0 present", p, ok)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestResolve_thePathTellsACodingPlanFromPayAsYouGo(t *testing.T) {
	reg := fetchFixture(t)

	// The vendor's two contracts share a host; the path is the difference,
	// and the flat-rate one must not be billed at the metered price.
	prices, warnings := Resolve(reg, "https://api.vendor.example/api/coding/paas/v4", "", "")
	if p, ok := prices[llm.ProDeployment]; !ok || p.In != 0 {
		t.Errorf("coding plan pro = %+v (present %v), want 0 present; warnings %v", p, ok, warnings)
	}
	prices, _ = Resolve(reg, "https://api.vendor.example/api/paas/v4", "", "")
	if p := prices[llm.ProDeployment]; p.In != 0.6 || p.Out != 2.2 {
		t.Errorf("pay-as-you-go pro = %+v, want 0.6/2.2", p)
	}
}

func TestResolve_thePortTellsLocalServersApart(t *testing.T) {
	reg := fetchFixture(t)

	// Two registry entries on 127.0.0.1; a third port is nobody's.
	if prices, _ := Resolve(reg, "http://127.0.0.1:1234/v1", "", ""); len(prices) != 2 {
		t.Errorf("the registry's own port must match, got %v", prices)
	}
	prices, warnings := Resolve(reg, "http://127.0.0.1:8000/v1", "", "")
	if len(prices) != 0 {
		t.Errorf("an unlisted port must not borrow a neighbour's price, got %v", prices)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "127.0.0.1:8000") {
		t.Errorf("warnings = %v, want one naming the endpoint with its port", warnings)
	}
}

func TestResolve_providersSharingOneEndpointMustAgreeOrNothingIsPriced(t *testing.T) {
	reg := fetchFixture(t)

	// Both gateway entries price Pro alike but the gate differently: the
	// endpoint alone does not say which contract applies.
	prices, warnings := Resolve(reg, "https://api.gateway.example/v1", "", "")
	if len(prices) != 0 {
		t.Errorf("a disagreement must price nothing, got %v", prices)
	}
	if len(warnings) < 1 || !strings.Contains(warnings[0], "differently") || !strings.Contains(warnings[0], llm.ShortGateDeployment) {
		t.Errorf("warnings = %v, want the disagreement named", warnings)
	}
}

func TestResolve_anUnknownEndpointIsTokensOnlyForTheWholeTable(t *testing.T) {
	reg := fetchFixture(t)

	// The embedding side alone would resolve, and a total made of the
	// embedding call only would read as the cost of the turn.
	prices, warnings := Resolve(reg, "http://llm.internal.example/v1", openaiURL, embed)

	if len(prices) != 0 {
		t.Errorf("an unknown LLM endpoint must empty the whole table, got %v", prices)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "llm.internal.example") || !strings.Contains(warnings[1], "tokens only") {
		t.Errorf("warnings = %v, want the endpoint named and the consequence stated", warnings)
	}
}

func TestResolve_aMatchedProviderMissingOneModelEmptiesTheTable(t *testing.T) {
	reg := fetchFixture(t)

	// The reseller lists only the Pro deployment: a total without the gate
	// calls would undercount every turn.
	prices, warnings := Resolve(reg, "https://llm.reseller.example/v1", openaiURL, "text-embedding-3-large")

	if len(prices) != 0 {
		t.Errorf("one unpriced model must empty the table, got %v", prices)
	}
	if len(warnings) != 3 || !strings.Contains(warnings[0], llm.ShortGateDeployment) || !strings.Contains(warnings[1], "text-embedding-3-large") {
		t.Errorf("warnings = %v, want one per gap and the consequence", warnings)
	}
}

func TestResolve_anEmptyEmbedEndpointIsSkippedSilently(t *testing.T) {
	reg := fetchFixture(t)
	prices, warnings := Resolve(reg, xiaomiURL, "", embed)
	if _, ok := prices[embed]; ok {
		t.Error("nothing embeds without an endpoint, so nothing is priced")
	}
	if len(prices) != 2 || len(warnings) != 0 {
		t.Errorf("prices = %v, warnings = %v: indexing off is a configured state", prices, warnings)
	}
}

func TestTable_startsFromTheOverrideAndKeepsItOverTheRegistry(t *testing.T) {
	override := usage.Prices{llm.ProDeployment: usage.Price{In: 9, Out: 9}}
	table := NewTable(override)
	if p := table.Prices()[llm.ProDeployment]; p.In != 9 {
		t.Fatalf("before any fetch the override must already apply, got %+v", p)
	}

	srv := serve(t, fixture, http.StatusOK)
	err := table.Refresh(context.Background(), Source{
		URL: srv.URL, Client: srv.Client(),
		LLMBaseURL: xiaomiURL, EmbedBaseURL: openaiURL, EmbedModel: embed,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := table.Prices()
	if p := got[llm.ProDeployment]; p.In != 9 || p.Out != 9 {
		t.Errorf("a configured pair wins over the registry, got %+v", p)
	}
	if p := got[llm.ShortGateDeployment]; p.In != 0.14 {
		t.Errorf("the rest comes from the registry, got %+v", p)
	}
	if p := got[embed]; p.In != 0.02 {
		t.Errorf("embed from the registry, got %+v", p)
	}
}

func TestTable_aNilTableIsAnEmptyOne(t *testing.T) {
	var table *Table
	if got := table.Prices(); got == nil || len(got) != 0 {
		t.Errorf("nil table prices = %v, want empty", got)
	}
}

func TestTable_aFailedRefreshLeavesTheTableUnchanged(t *testing.T) {
	table := NewTable(nil)
	good := serve(t, fixture, http.StatusOK)
	src := Source{URL: good.URL, Client: good.Client(), LLMBaseURL: xiaomiURL}
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

	table := NewTable(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		table.Run(ctx, Source{URL: srv.URL, Client: srv.Client(), LLMBaseURL: xiaomiURL})
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

func TestStart_anEmptyURLMeansNoWorkerAndOnlyTheOverride(t *testing.T) {
	var workers sync.WaitGroup
	override := usage.Prices{llm.ProDeployment: usage.Price{In: 1, Out: 2}}
	table := Start(context.Background(), &workers, override, Source{URL: ""})
	workers.Wait() // returns at once: nothing was started
	if p := table.Prices()[llm.ProDeployment]; p.In != 1 || p.Out != 2 {
		t.Errorf("override = %+v, want 1/2", p)
	}
}

func TestStart_withAURLRunsTheWorkerUntilTheContextEnds(t *testing.T) {
	srv := serve(t, fixture, http.StatusOK)
	var workers sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	table := Start(ctx, &workers, nil, Source{URL: srv.URL, Client: srv.Client(), LLMBaseURL: xiaomiURL})
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
