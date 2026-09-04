// Package pricing builds rongo's price table from models.dev, without anyone
// typing a price.
//
// The two MiMo deployments are priced from MiMo's own API listing, whatever
// endpoint rongo is configured to call. The same deployment name is listed at
// zero on a flat-rate token plan and at a markup behind a reseller, and
// neither figure is what a turn is worth: a plan hides the cost, a gateway
// prices its own contract rather than the model. The vendor's API is the one
// number that means the same thing on every host.
//
// The embedding model is the other case and keeps the endpoint lookup: it is
// whatever endpoint the operator configured, and the registry lists every
// provider with its API base URL, so matching the URL prices it exactly.
//
// A model the registry cannot price is not guessed at. The table stays empty,
// the UI shows tokens only, and the log says why.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/sched"
	"github.com/trick77/rongo/internal/usage"
)

// DefaultURL is where the registry lives. BACKEND_PRICES_URL overrides it.
const DefaultURL = "https://models.dev/api.json"

// MiMoAPI is the endpoint whose listing prices the two deployments, whatever
// endpoint rongo is configured to call. Not configurable: a price that moved
// with the endpoint is exactly what made a token plan read as free.
const MiMoAPI = "https://api.xiaomimimo.com/v1"

// Registry is models.dev's api.json, cut down to what the resolver reads.
type Registry map[string]Provider

// Provider is one entry of the registry: the base URL it serves, when it
// has exactly one, and the models it lists.
type Provider struct {
	// API is the provider's OpenAI-compatible base URL. Absent for providers
	// the registry does not tie to one URL (OpenAI itself among them).
	API    string           `json:"api"`
	Models map[string]Model `json:"models"`
}

// Model is one model as the registry prices it. A nil Cost is a model the
// registry lists but does not price.
type Model struct {
	Cost *Cost `json:"cost"`
}

// Cost is USD per million tokens, the unit usage.Price uses.
type Cost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// hostsWithoutAPI maps the base URL host of a provider whose registry entry
// has no api field. The registry ties OpenAI to its SDK rather than to a
// URL, and rongo's default embedding endpoint is exactly that host.
var hostsWithoutAPI = map[string]string{
	"api.openai.com": "openai",
}

// Fetch downloads and decodes the registry. Only the fields the resolver
// reads are kept; the file carries a great deal more.
func Fetch(ctx context.Context, rawURL string, client *http.Client) (Registry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry %s answered %s", rawURL, resp.Status)
	}
	var reg Registry
	// Bounded: a registry is a few megabytes, and an endpoint that streams
	// forever must not hold the process's memory.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&reg); err != nil {
		return nil, fmt.Errorf("registry %s: %w", rawURL, err)
	}
	return reg, nil
}

// Resolve builds the price table: MiMoAPI prices both deployments, the
// embedding endpoint prices the embedding model. An empty embedding endpoint
// is a configured state (indexing off), skipped without a word.
//
// All or nothing: a turn's total that silently leaves out the calls nobody
// could price would read as the whole cost, so one model the registry cannot
// price empties the table. Each gap is one warning.
func Resolve(reg Registry, embedBaseURL, embedModel string) (usage.Prices, []string) {
	prices := usage.Prices{}
	var warnings []string

	price := func(baseURL string, models ...string) {
		matches := providersFor(reg, baseURL)
		if len(matches) == 0 {
			warnings = append(warnings, fmt.Sprintf("endpoint %s is not in the price registry", endpointOf(baseURL)))
			return
		}
		for _, model := range models {
			p, warning := agreedPrice(reg, matches, model)
			if warning != "" {
				warnings = append(warnings, warning)
				continue
			}
			prices[model] = p
		}
	}

	price(MiMoAPI, llm.ProDeployment, llm.ShortGateDeployment)
	if embedBaseURL != "" {
		price(embedBaseURL, embedModel)
	}
	if len(warnings) > 0 {
		return usage.Prices{}, append(warnings, "showing tokens only: every model rongo calls must be priced, or none is")
	}
	return prices, nil
}

// agreedPrice is the price the matched providers give model. Several
// providers can share one base URL (a vendor and its coding plan, listed
// twice); when they disagree, the endpoint alone does not say which
// contract applies, and no figure is better than one of two.
func agreedPrice(reg Registry, ids []string, model string) (usage.Price, string) {
	var (
		agreed usage.Price
		found  bool
	)
	for _, id := range ids {
		m, listed := reg[id].Models[model]
		if !listed || m.Cost == nil {
			return usage.Price{}, fmt.Sprintf("provider %s does not price %s", id, model)
		}
		p := usage.Price{In: m.Cost.Input, Out: m.Cost.Output}
		if found && p != agreed {
			return usage.Price{}, fmt.Sprintf("providers %s share the endpoint but price %s differently; the endpoint does not say which contract applies", strings.Join(ids, ", "), model)
		}
		agreed, found = p, true
	}
	return agreed, ""
}

// endpointOf is what a base URL is matched on: host, port and path,
// lowercased, without scheme or trailing slash. The registry tells a
// vendor's coding plan from its pay-as-you-go endpoint by the path alone,
// and three local servers apart by the port alone, so neither can be
// dropped. A value that does not parse as a URL is matched as written.
func endpointOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimRight(rawURL, "/"))
	}
	return strings.ToLower(u.Host + strings.TrimRight(u.Path, "/"))
}

// providersFor finds the providers serving baseURL: the ones whose api is
// the endpoint itself or a prefix of it, path segment by path segment, and
// among those the longest. Ids come back sorted so a shared endpoint
// resolves the same way every boot. A provider without an api field is
// found through hostsWithoutAPI, by host, when nothing else matches.
func providersFor(reg Registry, baseURL string) []string {
	want := endpointOf(baseURL)
	var (
		best    []string
		bestLen = -1
	)
	for id, p := range reg {
		if p.API == "" {
			continue
		}
		api := endpointOf(p.API)
		if want != api && !strings.HasPrefix(want, api+"/") {
			continue
		}
		switch {
		case len(api) > bestLen:
			best, bestLen = []string{id}, len(api)
		case len(api) == bestLen:
			best = append(best, id)
		}
	}
	if len(best) > 0 {
		sort.Strings(best)
		return best
	}
	if u, err := url.Parse(baseURL); err == nil {
		if id, ok := hostsWithoutAPI[strings.ToLower(u.Hostname())]; ok {
			if _, listed := reg[id]; listed {
				return []string{id}
			}
		}
	}
	return nil
}

// Source is where a Table gets its prices from and what it prices. The LLM
// endpoint is deliberately absent: the deployments are priced from MiMoAPI,
// not from wherever they happen to be called.
type Source struct {
	URL    string
	Client *http.Client
	// EmbedBaseURL is the endpoint that is looked up; EmbedModel is the one
	// embedding model rongo calls.
	EmbedBaseURL string
	EmbedModel   string
}

// Table is the live price table: what the HTTP layer reads on every usage
// report. Safe for concurrent readers while Run replaces it in the
// background.
type Table struct {
	current atomic.Pointer[usage.Prices]
}

// NewTable starts empty: until the registry has answered, a report carries
// tokens only. There is no hand-typed price to start from.
func NewTable() *Table {
	t := &Table{}
	t.apply(nil)
	return t
}

// NewFixedTable is a table that never changes. For tests: the process's table
// is the one Start keeps current.
func NewFixedTable(prices usage.Prices) *Table {
	t := &Table{}
	t.apply(prices)
	return t
}

// Prices is the current table. Never nil, and a nil Table is an empty one:
// a deployment with no table shows tokens only rather than failing.
func (t *Table) Prices() usage.Prices {
	if t == nil {
		return usage.Prices{}
	}
	return *t.current.Load()
}

// apply installs fetched as the whole table. Copied, so a caller still
// holding the map it passed in cannot change what readers see.
func (t *Table) apply(fetched usage.Prices) {
	installed := usage.Prices{}
	for model, p := range fetched {
		installed[model] = p
	}
	t.current.Store(&installed)
}

// Refresh fetches the registry once and installs what it resolves. A failed
// fetch leaves the table as it was: yesterday's price beats no price, and
// the caller logs the error.
func (t *Table) Refresh(ctx context.Context, src Source) error {
	reg, err := Fetch(ctx, src.URL, src.Client)
	if err != nil {
		return err
	}
	prices, warnings := Resolve(reg, src.EmbedBaseURL, src.EmbedModel)
	for _, w := range warnings {
		slog.Warn("prices: "+w, "registry", src.URL)
	}
	for model, p := range prices {
		slog.Info("prices: resolved", "model", model, "in_per_mtok", p.In, "out_per_mtok", p.Out)
	}
	t.apply(prices)
	return nil
}

const (
	fetchTimeout    = 10 * time.Second
	refreshInterval = 24 * time.Hour
	retryInterval   = time.Hour
)

// Run keeps the table current until ctx ends: one fetch at boot, then daily,
// with an hourly retry after a failure so a process that started before its
// network was up does not stay tokens-only until the next restart.
func (t *Table) Run(ctx context.Context, src Source) {
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		err := t.Refresh(fetchCtx, src)
		cancel()
		wait := refreshInterval
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("prices: registry fetch failed; showing tokens only until it succeeds", "registry", src.URL, "err", err)
			wait = retryInterval
		}
		if !sched.Sleep(ctx, sched.Jittered(wait)) {
			return
		}
	}
}

// Start is the process's one entry point: a table kept current on a worker
// for as long as ctx lives. An empty URL is the lookup switched off, said
// once in the log: tokens only.
func Start(ctx context.Context, workers *sync.WaitGroup, src Source) *Table {
	t := NewTable()
	if src.URL == "" {
		slog.Info("prices: registry lookup is off; showing tokens only")
		return t
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		t.Run(ctx, src)
	}()
	return t
}
