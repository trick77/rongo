// Package llm talks to rongo's two MiMo deployments over an OpenAI-compatible
// chat completions endpoint.
//
// Ported from loom's client, which had already worked out the parts that matter
// here, and cut down to what rongo's pipeline needs: one completion call and one
// streaming call. No tools, no image path.
package llm

import (
	"bufio"
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
)

// The two deployments are hardcoded, never configurable. rongo targets MiMo
// specifically; a deployment name in an environment variable would let a
// misconfigured host answer with a model nobody chose, and the failure would
// look like a quality problem rather than a configuration one.
//
// ShortGateDeployment is the SAME reasoning family as Pro. It is picked because
// it queues less, not because it cannot think — see ShortGate.
const (
	ProDeployment       = "mimo-v2.5-pro"
	ShortGateDeployment = "mimo-v2.5"
)

// defaultMaxTokens caps a call that names no budget of its own. Every request
// carries a cap: an uncapped one can run until the context dies, and the bill is
// where that shows up first.
const defaultMaxTokens = 4096

// chatUserAgent is the User-Agent sent to the MiMo endpoint: the client string
// an OpenAI-compatible SDK sends, which is the shape that endpoint is built
// around. Go's default "Go-http-client/1.1" names the HTTP library and says
// nothing about the protocol being spoken. Same value as loom against the same
// endpoint, so behaviour stays comparable between the two.
const chatUserAgent = "opencode/1.18.26 ai-sdk/openai-compatible/3.0.43 ai-sdk/provider-utils/5.0.36 runtime/bun/1.4.0"

const maxErrorBodyBytes = 8 << 10

// Config holds the endpoint settings. The deployment names are not here on
// purpose.
type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
	// IdleTimeout aborts a stream when no frame arrives within the window.
	// Zero disables the watchdog; the coarse Timeout still applies.
	IdleTimeout time.Duration
	Logger      *slog.Logger
}

// Message is one OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is what one call cost, as the upstream reported it. Returned rather
// than stored: the llm_calls table is a later phase.
type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Total      int `json:"total_tokens"`
}

type thinkingOption struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
	Thinking            *thinkingOption `json:"thinking,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	// Temperature is omitted unless a call names one, so the endpoint's own
	// default keeps applying to everything that does not care.
	Temperature *float64 `json:"temperature,omitempty"`
}

// callOptions is what the Option funcs assemble.
type callOptions struct {
	model       string
	thinking    *thinkingOption
	maxTokens   int
	temperature *float64
}

// Option adjusts a single call.
type Option func(*callOptions)

// ShortGate routes this call to the non-Pro deployment, which queues less.
//
// It says nothing about reasoning. Both deployments are the same reasoning
// family and both think when asked to; suppressing thought is WithoutThinking,
// a separate switch. Never describe this as "the model that cannot think" — the
// bar for using it is "the output is an id or a label", not "no thought needed".
func ShortGate() Option {
	return func(o *callOptions) { o.model = ShortGateDeployment }
}

// WithoutThinking disables MiMo's native reasoning for this call. It does not
// change the deployment: a Pro call can be asked not to think, and a short-gate
// call can be asked to.
func WithoutThinking() Option {
	return func(o *callOptions) { o.thinking = &thinkingOption{Type: "disabled"} }
}

// WithMaxTokens caps the completion. Use it wherever a truncated reply is not
// worse than a long one.
func WithMaxTokens(n int) Option {
	return func(o *callOptions) { o.maxTokens = n }
}

// WithTemperature pins the sampling temperature for this call. A call that
// does not name one sends no temperature at all, and the endpoint's default
// applies — which is what every call did before this existed.
//
// It is a THIRD switch, independent of the deployment and of thinking. Use it
// where a re-roll is a defect rather than variety: the routing judge, the
// understanding step, the naming call — anything whose whole output is an id,
// a label or a one-word decision. Phase 4c measured what its absence costs.
// Two runs of the same routing arm, over frozen expansions and an unchanged
// corpus, decided three of sixty-one questions differently, which is larger
// than the difference phase 4b published between the Pro and non-Pro
// deployments. A reader sees the same thing from the other side: ask twice,
// get a card once and an answer the other time.
//
// The answer call deliberately does not use it. That one is written for a
// person to read, and pinning it to make a routing measurement reproducible
// would change what everybody reads.
func WithTemperature(v float64) Option {
	return func(o *callOptions) { o.temperature = &v }
}

// Client calls the chat completions endpoint.
type Client struct {
	baseURL     string
	apiKey      string
	http        *http.Client
	idleTimeout time.Duration
	log         *slog.Logger
}

// NewClient builds a Client. hc may be nil, in which case one is made with the
// configured timeout.
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		hc = &http.Client{Timeout: timeout}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		http:        hc,
		idleTimeout: cfg.IdleTimeout,
		log:         log,
	}
}

func resolve(opts []Option) callOptions {
	o := callOptions{model: ProDeployment, maxTokens: defaultMaxTokens}
	for _, fn := range opts {
		fn(&o)
	}
	if o.maxTokens <= 0 {
		o.maxTokens = defaultMaxTokens
	}
	return o
}

// Complete runs one non-streaming call and returns the assistant's content.
func (c *Client) Complete(ctx context.Context, msgs []Message, opts ...Option) (string, Usage, error) {
	resp, err := c.post(ctx, msgs, resolve(opts), false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, fmt.Errorf("decode chat completion: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", out.Usage, errors.New("chat completion carried no choices")
	}
	return out.Choices[0].Message.Content, out.Usage, nil
}

// Stream runs one streaming call, handing each content delta to onToken as it
// arrives. Only the final answer streams; every other step is an ordinary
// request.
func (c *Client) Stream(ctx context.Context, msgs []Message, onToken func(string), opts ...Option) (Usage, error) {
	// The watchdog is armed here, not merely configured. An upstream that
	// stalls after the first delta would otherwise hold the reader on a
	// half-written answer until the coarse HTTP timeout — minutes of a cursor
	// that looks like it is still thinking.
	if c.idleTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	resp, err := c.post(ctx, msgs, resolve(opts), true)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()

	// beat is called for every frame that arrives; the timer fires only when
	// none has for the whole window.
	beat := func() {}
	if c.idleTimeout > 0 {
		timer := time.AfterFunc(c.idleTimeout, func() { _ = resp.Body.Close() })
		defer timer.Stop()
		beat = func() { timer.Reset(c.idleTimeout) }
	}

	// finishReason is how the upstream said the stream ended. Anything but a
	// normal stop is a failure the caller must hear about: a reasoning model
	// that spends the whole completion budget thinking ends with "length" and
	// not one content delta, and reading that as success once stored an empty
	// answer as a finished turn. The reason is recorded and the stream read to
	// the end, because the usage frame that follows it carries the completion
	// count the error needs.
	var usage Usage
	var finishReason string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		beat()
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
			// Error is what an OpenAI-style upstream sends when it fails AFTER
			// status 200: one frame with no choices. Dropping it read as a
			// clean, empty stream.
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			// A malformed frame is not worth killing a half-written answer for.
			c.log.Warn("unparseable stream frame", "err", err)
			continue
		}
		if frame.Error != nil {
			return usage, fmt.Errorf("chat completion failed mid-stream (%s): %s",
				frame.Error.Type, c.redactKey(frame.Error.Message))
		}
		if frame.Usage != nil && frame.Usage.Total > 0 {
			usage = *frame.Usage
		}
		for _, ch := range frame.Choices {
			if ch.Delta.Content != "" && onToken != nil {
				onToken(ch.Delta.Content)
			}
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return usage, fmt.Errorf("read stream: %w", redactURL(err))
	}
	if finishReason != "" && finishReason != "stop" {
		return usage, fmt.Errorf("chat completion ended with finish_reason=%s after %d completion tokens",
			finishReason, usage.Completion)
	}
	return usage, nil
}

func (c *Client) post(ctx context.Context, msgs []Message, o callOptions, stream bool) (*http.Response, error) {
	body := chatRequest{
		Model:               o.model,
		Messages:            msgs,
		Stream:              stream,
		Thinking:            o.thinking,
		MaxCompletionTokens: o.maxTokens,
		Temperature:         o.temperature,
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", chatUserAgent)
	// Session headers pin a conversation to one upstream node. Both names carry
	// the same value; the upstream sends the pair too. Every call a turn makes —
	// the gates, the answer, the title written alongside it — shares the id,
	// because the thread is attached once to the request context. A call made
	// outside a turn falls back to the per-process id.
	sessionID := chatSessionID(threadIDFromContext(ctx))
	req.Header.Set("X-Session-Id", sessionID)
	req.Header.Set("X-Session-Affinity", sessionID)
	// Accept-Encoding is left unset on purpose so net/http keeps negotiating and
	// decompressing gzip transparently; setting it by hand would hand us a
	// compressed body to decode ourselves.
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactURL(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		// The upstream may echo request headers back in an error body. Strip
		// the key before it reaches a log line.
		return nil, fmt.Errorf("chat completion failed with status %d: %s",
			resp.StatusCode, c.redactKey(string(msg)))
	}
	return resp, nil
}

// redactKey removes the API key from text that is about to be quoted into an
// error. An upstream that echoes the Authorization header would otherwise put
// the credential into every log line that touches the failure.
func (c *Client) redactKey(s string) string {
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "[redacted]")
}

// redactURL keeps a transport error from carrying the full request URL, which
// can hold query parameters. Same rule as the embedding client: scheme and host
// are enough to tell an operator where it failed.
func redactURL(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	where := "the model endpoint"
	if u, perr := url.Parse(uerr.URL); perr == nil && u.Host != "" {
		where = u.Scheme + "://" + u.Host
	}
	return fmt.Errorf("%s %s: %w", uerr.Op, where, uerr.Err)
}
