// Package embed turns text into vectors through an OpenAI-compatible
// /embeddings endpoint, and caches the result by content hash so unchanged
// code is never embedded twice.
//
// The wire is github.com/trick77/llmwire: it batches the inputs, posts,
// checks that every input got exactly one vector at its own index, redacts
// the key out of error bodies and bounds the call. What stays here is what
// rongo adds — the vector width the database was built with, the usage
// meter, and a heartbeat while a request is in flight.
package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/usage"
)

const (
	defaultTimeout = 1 * time.Minute
	// defaultHeartbeat is how often an in-flight request reports that it is
	// still waiting. A stalled endpoint would otherwise be silent for the whole
	// timeout, which looks exactly like a hung indexer.
	defaultHeartbeat = 15 * time.Second
)

// Config configures the embedding client. Logger defaults to slog.Default();
// a negative HeartbeatInterval disables the heartbeat. Model must be one
// llmwire's registry knows; an unknown one fails the first call.
type Config struct {
	BaseURL           string
	APIKey            string
	Model             string
	Dim               int
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
}

// Client embeds text through an OpenAI-compatible endpoint.
type Client struct {
	wire      *llmwire.Client
	model     string
	dim       int
	log       *slog.Logger
	heartbeat time.Duration
}

// NewClient builds a Client. hc is optional; one supplied must not carry
// http.Client.Timeout, since llmwire bounds the call itself.
func NewClient(cfg Config, hc *http.Client) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeat
	}
	return &Client{
		wire: llmwire.New(llmwire.Config{
			BaseURL:       cfg.BaseURL,
			APIKey:        cfg.APIKey,
			HeaderTimeout: defaultTimeout,
			CallTimeout:   defaultTimeout,
			HTTPClient:    hc,
		}),
		model:     cfg.Model,
		dim:       cfg.Dim,
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
	}
}

// Model names the deployment this client embeds against. It is configuration
// rather than a secret — it rides on every request body — and the cache is
// keyed by it.
func (c *Client) Model() string { return c.model }

// Dim is the vector width this client expects back.
func (c *Client) Dim() int { return c.dim }

// Embed returns one vector per input, aligned to INPUT ORDER; llmwire splits
// a large input set across several requests of sixty-four. Empty input yields
// no vectors and makes no request.
//
// Order is the property to protect here: out-of-order results pair every chunk
// with someone else's embedding, and nothing downstream can notice — the
// vectors are all well-formed, they simply describe the wrong code. llmwire
// places each vector at its own index and errors on a duplicate, a gap or a
// count mismatch; the width check below is rongo's, because the width is what
// the vec0 table was built with.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	started := time.Now()
	stop := c.startHeartbeat(ctx, len(inputs))
	defer stop()

	resp, warnings, err := c.wire.Embed(ctx, llmwire.EmbedRequest{Model: c.model, Inputs: inputs})
	for _, w := range warnings {
		c.log.Debug("embed: wire warning", "kind", w.Kind, "feature", w.Feature, "details", w.Details)
	}
	if err != nil {
		return nil, embedError(err, time.Since(started))
	}
	// A turn's query embedding is metered; indexing runs on a context without
	// a meter and is not. Embedding has no completion side. Recorded only when
	// every batch reported its tokens: a partial count reads as a smaller
	// call, and an unknown one is not.
	var tokens int64
	if resp.Usage.Input.Total != nil {
		tokens = *resp.Usage.Input.Total
		usage.Record(ctx, usage.Call{
			Step:   "embed",
			Model:  c.model,
			Prompt: int(tokens),
			Ms:     usage.Int(int(time.Since(started).Milliseconds())),
		})
	}
	if c.dim > 0 {
		for _, v := range resp.Vectors {
			if len(v) != c.dim {
				return nil, fmt.Errorf("embedding has %d dimensions, want %d — vec0 would reject it later, far from the cause",
					len(v), c.dim)
			}
		}
	}
	c.log.Debug("embed: request done",
		"inputs", len(inputs),
		"model", c.model,
		"duration_ms", time.Since(started).Milliseconds(),
		"tokens_in", tokens)
	return resp.Vectors, nil
}

// embedError phrases a wire failure the way this package's callers read it.
// An APIError carries the status and a body llmwire already capped and
// redacted; a transport error is trimmed to scheme and host, because
// net/http quotes the FULL URL and some deployments carry their key in a
// query string. The host survives because "which endpoint was unreachable"
// is the whole diagnostic value of the message.
func embedError(err error, took time.Duration) error {
	var apiErr *llmwire.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return fmt.Errorf("embedding failed with status %d: %s", apiErr.StatusCode, apiErr.Message)
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		where := "the embedding endpoint"
		if u, perr := url.Parse(uerr.URL); perr == nil && u.Host != "" {
			where = u.Scheme + "://" + u.Host
		}
		return fmt.Errorf("embed request failed after %s: %s %s: %w",
			took.Round(time.Millisecond), uerr.Op, where, uerr.Err)
	}
	return err
}

// startHeartbeat logs at intervals while a request is in flight and returns a
// stop function. peeq keeps this in its llm package; rongo has none, and one
// ticker does not justify inventing one.
func (c *Client) startHeartbeat(ctx context.Context, inputs int) func() {
	if c.heartbeat <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(c.heartbeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				c.log.Info("embed: still waiting for response", "inputs", inputs, "model", c.model)
			}
		}
	}()
	return func() { close(done) }
}
