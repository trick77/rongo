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

	"github.com/trick77/llmwire"
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
	// CostNanoUSD is what the call cost at the vendor's list price, in
	// billionths of a dollar, priced by llmwire from its own table as the
	// reply came in. Absent when llmwire had no rate for the model, and on
	// every row written before the column existed: a call nobody priced must
	// not read as a call that cost nothing. Integer, because a sum of floats
	// drifts and a bill does not.
	CostNanoUSD *int64 `json:"cost_nano_usd,omitempty"`
}

// Nano is the pointer form of a cost, the way Int is for the counts.
func Nano(n int64) *int64 { return &n }

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

// CallReport is one call with its cost, when llmwire priced it, and the
// window of the model it went to, when llmwire's profile sizes it.
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
	// CostUSD is present as soon as any call carries a price. Absent means
	// "nothing here was priced", zero means "priced, and this turn cost
	// nothing" — the two must not merge. A turn stored before costs were
	// recorded is absent for good; the figure is what llmwire said at the
	// time, never re-derived from tokens against today's table.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// Price sums the calls and turns the stored nanodollars into dollars. A
// call without a price carries no cost rather than zero; the total counts it
// as nothing, which is the honest number for "we do not know".
func Price(calls []Call) Report {
	r := Report{Calls: make([]CallReport, 0, len(calls))}
	var cost int64
	priced := false
	cached, anyCached := 0, false
	for _, c := range calls {
		cr := CallReport{Call: c, ContextTokens: contextWindow(c.Model)}
		if c.CostNanoUSD != nil {
			v := float64(*c.CostNanoUSD) / 1e9
			cr.CostUSD = &v
			cost += *c.CostNanoUSD
			priced = true
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
	if priced {
		v := float64(cost) / 1e9
		r.CostUSD = &v
	}
	return r
}

// contextWindow is how many tokens the model can hold, from llmwire's
// profile. Zero for a model it does not know, and then nothing is shown: a
// made-up window would read as a real ceiling.
func contextWindow(model string) int {
	p, err := llmwire.Default().Lookup(model)
	if err != nil {
		return 0
	}
	return int(p.Limits.Context)
}
