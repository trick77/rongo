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

func TestReport_ofNoCallsIsEmptyNotNil(t *testing.T) {
	r := Prices{"m": Price{In: 1, Out: 1}}.Report(nil)
	if r.Calls == nil || r.Total != 0 {
		t.Fatalf("report = %+v, want an empty call list and zero totals", r)
	}
	if r.CostUSD == nil || *r.CostUSD != 0 {
		t.Fatal("with prices configured an empty turn costs zero, not nothing")
	}
}
