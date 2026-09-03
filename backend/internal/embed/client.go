// Package embed turns text into vectors through an OpenAI-compatible
// /embeddings endpoint, and caches the result by content hash so unchanged
// code is never embedded twice.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/usage"
)

const (
	defaultTimeout = 1 * time.Minute
	// maxErrorBody caps how much of a failing response reaches an error string.
	// An endpoint answering with an HTML error page would otherwise put a
	// screenful into a log line.
	maxErrorBody = 4 << 10
	// maxInputs caps how many texts ride in one request. A full repository
	// index produces thousands of chunks, and one request carrying all of them
	// would sit far past any sensible HTTP timeout.
	maxInputs = 64
	// defaultHeartbeat is how often an in-flight request reports that it is
	// still waiting. A stalled endpoint would otherwise be silent for the whole
	// timeout, which looks exactly like a hung indexer.
	defaultHeartbeat = 15 * time.Second
)

// Config configures the embedding client. Logger defaults to slog.Default();
// a negative HeartbeatInterval disables the heartbeat.
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
	baseURL   string
	apiKey    string
	model     string
	dim       int
	http      *http.Client
	log       *slog.Logger
	heartbeat time.Duration
}

// NewClient builds a Client. hc is optional.
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeat
	}
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		dim:       cfg.Dim,
		http:      hc,
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

type request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// Embed returns one vector per input, aligned to INPUT ORDER, splitting large
// input sets across several requests. Empty input yields no vectors and makes
// no request.
//
// Order is the property to protect here: out-of-order results pair every chunk
// with someone else's embedding, and nothing downstream can notice — the
// vectors are all well-formed, they simply describe the wrong code.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += maxInputs {
		end := min(start+maxInputs, len(inputs))
		vecs, err := c.embedOne(ctx, inputs[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) embedOne(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(request{Model: c.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	stop := c.startHeartbeat(ctx, len(inputs))
	defer stop()

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request failed after %s: %w",
			time.Since(started).Round(time.Millisecond), redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("embedding failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed response
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	// A turn's query embedding is metered; indexing runs on a context without
	// a meter and is not. Embedding has no completion side.
	usage.Record(ctx, usage.Call{Step: "embed", Model: c.model, Prompt: int(parsed.Usage.PromptTokens)})
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding count mismatch: got %d, want %d", len(parsed.Data), len(inputs))
	}

	// Placing each vector at its OWN index rather than sorting: sorting trusts
	// the indexes to be a permutation, and a response with a duplicated index
	// would then quietly shift every later vector by one. Here a duplicate or an
	// out-of-range index is an error.
	out := make([][]float32, len(inputs))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(inputs) {
			return nil, fmt.Errorf("embedding response index %d out of range for %d inputs", d.Index, len(inputs))
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("embedding response repeats index %d; results cannot be paired with their inputs", d.Index)
		}
		if c.dim > 0 && len(d.Embedding) != c.dim {
			return nil, fmt.Errorf("embedding has %d dimensions, want %d — vec0 would reject it later, far from the cause",
				len(d.Embedding), c.dim)
		}
		out[d.Index] = d.Embedding
	}
	c.log.Debug("embed: request done",
		"inputs", len(inputs),
		"model", c.model,
		"duration_ms", time.Since(started).Milliseconds(),
		"tokens_in", parsed.Usage.PromptTokens)
	return out, nil
}

// redactURL strips the request URL out of a transport error, keeping the
// scheme and host.
//
// net/http wraps every transport failure in a *url.Error carrying the FULL
// URL, and that error is what a caller logs. Some OpenAI-compatible
// deployments carry their key in a query string, so the plain wrapped error is
// a credential in a log line — the rule is "never log a token, a full URL, or
// a query string", and this is the one place in this package that would break
// it. The host survives because "which endpoint was unreachable" is the whole
// diagnostic value of the message.
func redactURL(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	where := "the embedding endpoint"
	if u, perr := url.Parse(uerr.URL); perr == nil && u.Host != "" {
		where = u.Scheme + "://" + u.Host
	}
	return fmt.Errorf("%s %s: %w", uerr.Op, where, uerr.Err)
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
