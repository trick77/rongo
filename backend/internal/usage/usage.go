// Package usage meters what one turn costs: every model and embedding call
// the turn made, with the tokens the upstream reported for each.
//
// The meter travels on the request context. A client that finds one records
// into it; a client that finds none records nothing. That is how indexing
// stays out of the count without a switch: the poller's context never carries
// a meter, a turn's always does.
package usage

import (
	"context"
	"sync"
)

// Call is one paid request, as the upstream reported it.
type Call struct {
	// Step names what the call was for: understand, route, name, embed,
	// answer, title. It is the label a reader sees in the breakdown.
	Step string `json:"step"`
	// Model is the deployment or embedding model the call went to, and the
	// key the price table is looked up by.
	Model      string `json:"model"`
	Prompt     int    `json:"prompt_tokens"`
	Completion int    `json:"completion_tokens"`
	// Cached is the part of Prompt the upstream served from its prompt cache,
	// as prompt_tokens_details.cached_tokens. A SUBSET of Prompt, never an
	// addition: total_tokens equals prompt plus completion whether anything
	// was cached or not. It matters because a cached token is priced at
	// cache_read, which the MiMo listing puts fifty to a hundred times below
	// the input price — a thread's second turn repeats the prefix of its
	// first, and charging that at full price overstates what it cost.
	//
	// Reasoning is completion_tokens_details.reasoning_tokens: the part of
	// Completion the model spent thinking rather than writing. Also a subset.
	//
	// Ms is how long the call took, wall clock, request to last byte.
	//
	// All three are pointers for one reason: a turn answered before these
	// were recorded must not read as a call that cached nothing, reasoned
	// about nothing and took no time. Absent is absent — the same rule
	// Report.CostUSD has kept since the price table could be empty.
	Cached    *int `json:"cached_tokens,omitempty"`
	Reasoning *int `json:"reasoning_tokens,omitempty"`
	Ms        *int `json:"ms,omitempty"`
}

// Int is the pointer form of n, for the three optional counts on Call. The
// clients know their figures; only a row read back from before the columns
// existed does not.
func Int(n int) *int { return &n }

// Meter collects the calls of one turn. Safe for concurrent use: candidate
// naming fires one call per candidate from separate goroutines.
type Meter struct {
	mu    sync.Mutex
	calls []Call
}

// New makes an empty meter.
func New() *Meter { return &Meter{} }

// Record appends one call.
func (m *Meter) Record(c Call) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
}

// Total is every token recorded so far, prompt and completion alike. It is
// what the per-turn ceiling in internal/llm reads before a call; the meter
// is short, so summing beats keeping a running figure in step.
func (m *Meter) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		n += c.Prompt + c.Completion
	}
	return n
}

// Calls returns what was recorded so far, in order. Never nil.
func (m *Meter) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Call, len(m.calls))
	copy(out, m.calls)
	return out
}

type meterKey struct{}

// WithMeter attaches a meter to the context. context.WithoutCancel keeps
// values, so a background job derived from a turn's context still writes into
// the turn's meter — give such a job its own meter when its calls should be
// accounted separately.
func WithMeter(ctx context.Context, m *Meter) context.Context {
	return context.WithValue(ctx, meterKey{}, m)
}

// MeterFrom returns the meter on the context, or nil.
func MeterFrom(ctx context.Context) *Meter {
	m, _ := ctx.Value(meterKey{}).(*Meter)
	return m
}

// Record writes one call into the context's meter, if there is one.
func Record(ctx context.Context, c Call) {
	if m := MeterFrom(ctx); m != nil {
		m.Record(c)
	}
}

// Price is what a model charges, in USD per million tokens, and how much of
// it the model can hold. Embedding models have no output side; their Out
// stays zero.
//
// The window rides along with the price because the registry ships both in
// the same entry: models.dev carries cost and limit side by side, and a
// second map keyed the same way would be two lookups and two chances for
// them to disagree about which models are known.
type Price struct {
	In  float64
	Out float64
	// CacheRead is what a prompt token the upstream served from its cache
	// costs. Zero means the registry does not list one, and then a cached
	// token is charged at In — no discount is invented for a model whose
	// contract does not say there is one.
	CacheRead float64
	// Context is how many tokens the model can hold, prompt and completion
	// together. Zero means the registry does not say, and then nothing is
	// shown: a made-up window would read as a real ceiling.
	Context int
}

// Prices maps a model name to its price. Empty means nothing is priced and a
// report carries tokens only.
type Prices map[string]Price

// CallReport is one call with its cost, when the model is priced, and the
// window of the model it went to, when the registry sizes it.
type CallReport struct {
	Call
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// ContextTokens is the window of this call's model. Per call rather than
	// per turn: a turn calls two deployments and an embedding model, and only
	// the call itself says which window its prompt was measured against.
	ContextTokens int `json:"context_tokens,omitempty"`
}

// Report is what one turn cost, as the browser sees it. total_tokens keeps its
// name from the days when the usage event carried nothing else.
type Report struct {
	Calls      []CallReport `json:"calls"`
	Prompt     int          `json:"prompt_tokens"`
	Completion int          `json:"completion_tokens"`
	Total      int          `json:"total_tokens"`
	// Cached is how much of Prompt the upstream served from its cache, summed
	// over the calls that reported one. Absent when no call did — a turn from
	// before the figure was recorded must not read as a turn that cached
	// nothing.
	Cached *int `json:"cached_tokens,omitempty"`
	// CostUSD is present as soon as any price is configured, even when every
	// call went to an unpriced model. Absent means "not priced here", zero
	// means "priced, and this turn cost nothing" — the two must not merge.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// Report sums the calls and prices the ones whose model has a price. A call
// to an unpriced model carries no cost rather than zero; the total counts it
// as nothing, which is the honest number for "we do not know".
func (p Prices) Report(calls []Call) Report {
	r := Report{Calls: make([]CallReport, 0, len(calls))}
	var cost float64
	cached, anyCached := 0, false
	for _, c := range calls {
		cr := CallReport{Call: c}
		if price, ok := p[c.Model]; ok {
			v := callCost(c, price)
			cr.CostUSD = &v
			cr.ContextTokens = price.Context
			cost += v
		}
		if c.Cached != nil {
			cached += *c.Cached
			anyCached = true
		}
		r.Calls = append(r.Calls, cr)
		r.Prompt += c.Prompt
		r.Completion += c.Completion
	}
	r.Total = r.Prompt + r.Completion
	if anyCached {
		r.Cached = &cached
	}
	if len(p) > 0 {
		r.CostUSD = &cost
	}
	return r
}

// callCost prices one call, charging the part of the prompt the upstream
// served from its cache at the cache price. The MiMo listing puts that price
// fifty to a hundred times below the input price, and a thread's later turns
// repeat the prefix of its first, so charging every prompt token at full
// price is not a rounding difference — it is the wrong number.
//
// Without a cache price nothing is discounted: a model whose registry entry
// does not list one is charged the way it always was.
func callCost(c Call, price Price) float64 {
	cached := 0
	if c.Cached != nil && price.CacheRead > 0 {
		cached = min(*c.Cached, c.Prompt)
	}
	full := float64(c.Prompt - cached)
	return (full*price.In + float64(cached)*price.CacheRead + float64(c.Completion)*price.Out) / 1e6
}
