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
// resolver reads: a provider's api host and each model's cost. "openai" has
// no api field, as on the real site; "reseller" prices the deployments
// differently, which is the whole reason the host decides.
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
  "openai": {
    "id": "openai", "name": "OpenAI", "api": null,
    "models": {
      "text-embedding-3-small": {"id": "text-embedding-3-small", "cost": {"input": 0.02, "output": 0}}
    }
  }
}`

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

func TestFetch_decodesProvidersHostsAndCosts(t *testing.T) {
	reg := fetchFixture(t)
	if len(reg) != 4 {
		t.Fatalf("decoded %d providers, want 4", len(reg))
	}
	if reg["xiaomi"].API != "https://api.xiaomimimo.com/v1" {
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

func TestResolve_pricesBothDeploymentsFromTheProviderServingTheHost(t *testing.T) {
	// Given the pay-as-you-go endpoint and OpenAI embeddings
	reg := fetchFixture(t)

	// When
	prices, warnings := Resolve(reg, "https://api.xiaomimimo.com/v1", "https://api.openai.com/v1", "text-embedding-3-small")

	// Then every model rongo calls is priced, and nothing is warned about
	if p := prices[llm.ProDeployment]; p.In != 0.435 || p.Out != 0.87 {
		t.Errorf("pro = %+v, want 0.435/0.87", p)
	}
	if p := prices[llm.ShortGateDeployment]; p.In != 0.14 || p.Out != 0.28 {
		t.Errorf("gate = %+v, want 0.14/0.28", p)
	}
	if p := prices["text-embedding-3-small"]; p.In != 0.02 || p.Out != 0 {
		t.Errorf("embed = %+v, want 0.02/0", p)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestResolve_theHostDecidesNotTheModelName(t *testing.T) {
	reg := fetchFixture(t)

	// A reseller charges five times Xiaomi's price for the same name.
	prices, _ := Resolve(reg, "https://llm.reseller.example/v1", "", "")
	if p := prices[llm.ProDeployment]; p.In != 2.175 || p.Out != 4.35 {
		t.Errorf("reseller pro = %+v, want 2.175/4.35", p)
	}

	// A token plan is flat rate: zero is a price, present in the table, so
	// the browser shows a real $0 rather than nothing.
	prices, warnings := Resolve(reg, "https://token-plan-ams.xiaomimimo.com/v1/", "", "")
	p, ok := prices[llm.ProDeployment]
	if !ok || p.In != 0 || p.Out != 0 {
		t.Errorf("token plan pro = %+v (present %v), want 0/0 present", p, ok)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestResolve_anUnknownHostIsTokensOnlyWithAWarning(t *testing.T) {
	reg := fetchFixture(t)

	prices, warnings := Resolve(reg, "http://127.0.0.1:9/v1", "https://api.openai.com/v1", "text-embedding-3-small")

	if _, ok := prices[llm.ProDeployment]; ok {
		t.Error("an unknown host must not price the deployment")
	}
	if _, ok := prices["text-embedding-3-small"]; !ok {
		t.Error("the embedding side is matched independently and must still be priced")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "127.0.0.1") {
		t.Errorf("warnings = %v, want one naming the host", warnings)
	}
}

func TestResolve_aMatchedProviderMissingTheModelWarns(t *testing.T) {
	reg := fetchFixture(t)

	// The reseller lists only the Pro deployment.
	prices, warnings := Resolve(reg, "https://llm.reseller.example/v1", "https://api.openai.com/v1", "text-embedding-3-large")

	if _, ok := prices[llm.ShortGateDeployment]; ok {
		t.Error("an unlisted model must stay unpriced")
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one for the gate deployment and one for the embedding model", warnings)
	}
	if !strings.Contains(warnings[0], llm.ShortGateDeployment) || !strings.Contains(warnings[1], "text-embedding-3-large") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestResolve_anEmptyEmbedEndpointIsSkippedSilently(t *testing.T) {
	reg := fetchFixture(t)
	prices, warnings := Resolve(reg, "https://api.xiaomimimo.com/v1", "", "text-embedding-3-small")
	if _, ok := prices["text-embedding-3-small"]; ok {
		t.Error("nothing embeds without an endpoint, so nothing is priced")
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: indexing off is a configured state", warnings)
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
		LLMBaseURL: "https://api.xiaomimimo.com/v1", EmbedBaseURL: "https://api.openai.com/v1", EmbedModel: "text-embedding-3-small",
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
	if p := got["text-embedding-3-small"]; p.In != 0.02 {
		t.Errorf("embed from the registry, got %+v", p)
	}
}

func TestTable_aFailedRefreshLeavesTheTableUnchanged(t *testing.T) {
	table := NewTable(nil)
	good := serve(t, fixture, http.StatusOK)
	src := Source{URL: good.URL, Client: good.Client(), LLMBaseURL: "https://api.xiaomimimo.com/v1"}
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
		table.Run(ctx, Source{URL: srv.URL, Client: srv.Client(), LLMBaseURL: "https://api.xiaomimimo.com/v1"})
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
	table := Start(ctx, &workers, nil, Source{URL: srv.URL, Client: srv.Client(), LLMBaseURL: "https://api.xiaomimimo.com/v1"})
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
