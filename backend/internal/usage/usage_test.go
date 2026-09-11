package usage

import (
	"context"
	"sync"
	"testing"
)

func TestRecord_withoutAMeterOnTheContextIsANoop(t *testing.T) {
	Record(context.Background(), Call{Step: "answer", Model: "m", Prompt: 1, Completion: 1})
	// Nothing to assert beyond "did not panic": indexing runs on a context
	// that carries no meter, and its embeddings must not be counted anywhere.
}

func TestMeter_collectsEveryCallInOrderAndIsSafeForConcurrentCallers(t *testing.T) {
	m := New()
	ctx := WithMeter(context.Background(), m)
	Record(ctx, Call{Step: "understand", Model: "mimo-v2.5", Prompt: 10, Completion: 2})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Record(ctx, Call{Step: "name", Model: "mimo-v2.5", Prompt: 5, Completion: 1})
		}()
	}
	wg.Wait()

	calls := m.Calls()
	if len(calls) != 21 {
		t.Fatalf("recorded %d calls, want 21", len(calls))
	}
	if calls[0].Step != "understand" {
		t.Fatalf("first call is %q, want understand", calls[0].Step)
	}
	if MeterFrom(context.Background()) != nil {
		t.Fatal("a bare context must carry no meter")
	}
}

func TestReport_sumsTokensAndPricesOnlyWhenPricesAreConfigured(t *testing.T) {
	calls := []Call{
		{Step: "route", Model: "mimo-v2.5-pro", Prompt: 1000, Completion: 10},
		{Step: "embed", Model: "text-embedding-3-small", Prompt: 500},
		{Step: "answer", Model: "mimo-v2.5-pro", Prompt: 2000, Completion: 1000},
	}

	// No prices: tokens only, no money anywhere.
	r := Prices{}.Report(calls)
	if r.Prompt != 3500 || r.Completion != 1010 || r.Total != 4510 {
		t.Fatalf("totals = %d/%d/%d, want 3500/1010/4510", r.Prompt, r.Completion, r.Total)
	}
	if r.CostUSD != nil {
		t.Fatalf("cost = %v, want none without prices", *r.CostUSD)
	}
	if len(r.Calls) != 3 || r.Calls[0].CostUSD != nil {
		t.Fatal("per-call cost must be absent without prices")
	}

	// Prices for the Pro deployment only: the embed call is unpriced and
	// contributes nothing, but the total is still a number.
	p := Prices{"mimo-v2.5-pro": Price{In: 1.0, Out: 4.0}}
	r = p.Report(calls)
	if r.CostUSD == nil {
		t.Fatal("cost must be present once any price is configured")
	}
	// (1000+2000)/1e6 * 1.0 + (10+1000)/1e6 * 4.0 = 0.003 + 0.00404
	if got, want := *r.CostUSD, 0.00704; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	if r.Calls[1].CostUSD != nil {
		t.Fatal("an unpriced model must carry no cost, not zero")
	}
	if c := r.Calls[0].CostUSD; c == nil || *c < 0.00104-1e-9 || *c > 0.00104+1e-9 {
		t.Fatalf("route cost = %v, want 0.00104", c)
	}
}

func TestReport_aCachedPrefixIsChargedAtTheCachePrice(t *testing.T) {
	// Given a call whose prompt the upstream mostly served from its cache
	calls := []Call{{Step: "answer", Model: "pro", Prompt: 10_000, Completion: 1_000, Cached: Int(9_000)}}
	p := Prices{"pro": Price{In: 0.435, Out: 0.87, CacheRead: 0.0036, Context: 1_048_576}}

	// When
	r := p.Report(calls)

	// Then the cached part is priced at cache_read, not at the input price
	// (1000*0.435 + 9000*0.0036 + 1000*0.87) / 1e6
	want := (1_000*0.435 + 9_000*0.0036 + 1_000*0.87) / 1e6
	if got := *r.CostUSD; got < want-1e-12 || got > want+1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	// And the full-price figure is what it would have been: the whole point
	// is that this is lower.
	if full := (10_000*0.435 + 1_000*0.87) / 1e6; *r.CostUSD >= full {
		t.Fatalf("cost %v is not below the uncached %v", *r.CostUSD, full)
	}
	if r.Cached == nil || *r.Cached != 9_000 {
		t.Fatalf("cached = %v, want 9000", r.Cached)
	}
	if r.Calls[0].ContextTokens != 1_048_576 {
		t.Fatalf("window = %d, want the model's", r.Calls[0].ContextTokens)
	}
	// And the tokens are untouched: cached is a subset of prompt, never an
	// addition to it.
	if r.Prompt != 10_000 || r.Total != 11_000 {
		t.Fatalf("tokens = %d/%d, want 10000/11000", r.Prompt, r.Total)
	}
}

func TestReport_withoutACachePriceNothingIsDiscounted(t *testing.T) {
	// Given a model whose registry entry lists no cache price
	calls := []Call{{Step: "rerank", Model: "gate", Prompt: 10_000, Completion: 100, Cached: Int(9_000)}}
	p := Prices{"gate": Price{In: 0.14, Out: 0.28}}

	// When
	r := p.Report(calls)

	// Then the old arithmetic stands: no discount is invented for a contract
	// that does not say there is one.
	want := (10_000*0.14 + 100*0.28) / 1e6
	if got := *r.CostUSD; got < want-1e-12 || got > want+1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestReport_aTurnThatReportedNoCachedFigureCarriesNoneAtAll(t *testing.T) {
	// Given a turn from before the figure was recorded
	r := Prices{"pro": Price{In: 1, Out: 1}}.Report([]Call{{Step: "answer", Model: "pro", Prompt: 10, Completion: 2}})

	// Then absent, not zero: the two say different things.
	if r.Cached != nil {
		t.Fatalf("cached = %v, want absent", *r.Cached)
	}
}

func TestMeter_totalIsEveryTokenTheTurnPaidFor(t *testing.T) {
	m := New()
	if m.Total() != 0 {
		t.Fatalf("an empty meter totals %d, want 0", m.Total())
	}
	m.Record(Call{Step: "understand", Prompt: 100, Completion: 20})
	m.Record(Call{Step: "embed", Prompt: 30})
	m.Record(Call{Step: "answer", Prompt: 5000, Completion: 800})
	if got := m.Total(); got != 5950 {
		t.Fatalf("total %d, want 5950", got)
	}
}

func TestReport_ofNoCallsIsEmptyNotNil(t *testing.T) {
	r := Prices{"m": Price{In: 1, Out: 1}}.Report(nil)
	if r.Calls == nil || r.Total != 0 {
		t.Fatalf("report = %+v, want an empty call list and zero totals", r)
	}
	if r.CostUSD == nil || *r.CostUSD != 0 {
		t.Fatal("with prices configured an empty turn costs zero, not nothing")
	}
}
