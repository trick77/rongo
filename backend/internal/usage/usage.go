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
}

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

// Price is what a model charges, in USD per million tokens. Embedding models
// have no output side; their Out stays zero.
type Price struct {
	In  float64
	Out float64
}

// Prices maps a model name to its price. Empty means nothing is priced and a
// report carries tokens only.
type Prices map[string]Price

// PriceSource is where the HTTP layer reads the current table. A Prices map
// is its own source, so a test hands over a literal; the process hands over
// a table that a registry refreshes in the background.
type PriceSource interface {
	Prices() Prices
}

// Prices makes a static table its own source.
func (p Prices) Prices() Prices { return p }

// CallReport is one call with its cost, when the model is priced.
type CallReport struct {
	Call
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// Report is what one turn cost, as the browser sees it. total_tokens keeps its
// name from the days when the usage event carried nothing else.
type Report struct {
	Calls      []CallReport `json:"calls"`
	Prompt     int          `json:"prompt_tokens"`
	Completion int          `json:"completion_tokens"`
	Total      int          `json:"total_tokens"`
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
	for _, c := range calls {
		cr := CallReport{Call: c}
		if price, ok := p[c.Model]; ok {
			v := (float64(c.Prompt)*price.In + float64(c.Completion)*price.Out) / 1e6
			cr.CostUSD = &v
			cost += v
		}
		r.Calls = append(r.Calls, cr)
		r.Prompt += c.Prompt
		r.Completion += c.Completion
	}
	r.Total = r.Prompt + r.Completion
	if len(p) > 0 {
		r.CostUSD = &cost
	}
	return r
}
