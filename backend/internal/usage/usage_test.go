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

func TestReport_sumsTokensAndPricesOnlyTheCallsLlmwirePriced(t *testing.T) {
	calls := []Call{
		{Step: "route", Model: "mimo-v2.5-pro", Prompt: 1000, Completion: 10},
		{Step: "embed", Model: "text-embedding-3-small", Prompt: 500},
		{Step: "answer", Model: "mimo-v2.5-pro", Prompt: 2000, Completion: 1000},
	}

	// No call priced: tokens only, no money anywhere. This is every turn
	// stored before costs were recorded, and it stays that way — tokens are
	// never re-priced against today's table.
	r := Price(calls)
	if r.Prompt != 3500 || r.Completion != 1010 || r.Total != 4510 {
		t.Fatalf("totals = %d/%d/%d, want 3500/1010/4510", r.Prompt, r.Completion, r.Total)
	}
	if r.CostUSD != nil {
		t.Fatalf("cost = %v, want none when nothing was priced", *r.CostUSD)
	}
	if len(r.Calls) != 3 || r.Calls[0].CostUSD != nil {
		t.Fatal("per-call cost must be absent when nothing was priced")
	}

	// Two of the three priced: the embed call contributes nothing, but the
	// total is still a number.
	calls[0].CostNanoUSD = Nano(1_040_000)
	calls[2].CostNanoUSD = Nano(6_000_000)
	r = Price(calls)
	if r.CostUSD == nil {
		t.Fatal("cost must be present once any call carries a price")
	}
	if got, want := *r.CostUSD, 0.00704; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	if r.Calls[1].CostUSD != nil {
		t.Fatal("an unpriced call must carry no cost, not zero")
	}
	if c := r.Calls[0].CostUSD; c == nil || *c < 0.00104-1e-9 || *c > 0.00104+1e-9 {
		t.Fatalf("route cost = %v, want 0.00104", c)
	}
}

func TestReport_theWindowComesFromLlmwiresProfile(t *testing.T) {
	// Given a call to a deployment llmwire sizes, and one to a model it has
	// never heard of
	r := Price([]Call{
		{Step: "answer", Model: "mimo-v2.5-pro", Prompt: 10, Completion: 2, Cached: Int(9)},
		{Step: "answer", Model: "nobody-knows", Prompt: 10, Completion: 2},
	})

	// Then the known one carries its window and the other none: a made-up
	// window would read as a real ceiling.
	if r.Calls[0].ContextTokens <= 0 {
		t.Fatalf("window = %d, want the profile's", r.Calls[0].ContextTokens)
	}
	if r.Calls[1].ContextTokens != 0 {
		t.Fatalf("window = %d for an unknown model, want 0", r.Calls[1].ContextTokens)
	}
	// And cached is summed over the calls that reported one, a subset of the
	// prompt and never an addition to it.
	if r.Cached == nil || *r.Cached != 9 || r.Prompt != 20 {
		t.Fatalf("cached/prompt = %v/%d, want 9/20", r.Cached, r.Prompt)
	}
}

func TestReport_aTurnThatReportedNoCachedFigureCarriesNoneAtAll(t *testing.T) {
	// Given a turn from before the figure was recorded
	r := Price([]Call{{Step: "answer", Model: "pro", Prompt: 10, Completion: 2, CostNanoUSD: Nano(1)}})

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
	r := Price(nil)
	if r.Calls == nil || r.Total != 0 {
		t.Fatalf("report = %+v, want an empty call list and zero totals", r)
	}
	if r.CostUSD != nil {
		t.Fatal("an empty turn priced nothing, so it carries no cost rather than zero")
	}
}
