// Package llm talks to rongo's two MiMo deployments over an OpenAI-compatible
// chat completions endpoint.
//
// The wire is github.com/trick77/llmwire: it renders the request the way each
// deployment wants it, reads the stream, decodes usage, redacts the key out of
// error bodies and bounds the call by named timeouts. What stays here is what
// rongo means by a call — two lanes, a completion cap on every request, the
// usage meter and the per-turn ceiling. One completion call and one streaming
// call; no tools, no image path.
//
// Session affinity is llmwire's: under Config.EmulateOpenCode the client
// presents as the opencode client and carries one session id per process,
// minted at construction and rotated after a thirty-minute idle gap. Calls are
// not pinned per thread.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/usage"
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

// Config holds the endpoint settings. The deployment names are not here on
// purpose. BaseURL and APIKey override the env vars the deployment's llmwire
// profile names (BACKEND_CHAT_BASE_URL and BACKEND_CHAT_API_KEY); left empty,
// the production case, llmwire reads those itself. A test points BaseURL at
// its fake and no variable is consulted.
type Config struct {
	BaseURL string
	APIKey  string
	// Timeout bounds the whole call, and also how long the endpoint may take
	// to send response headers: Pro queues at the endpoint before it answers,
	// and llmwire's one-minute header default would end a queued call that
	// was about to be served. Zero takes llmwire's defaults.
	Timeout time.Duration
	// IdleTimeout aborts a stream when no frame arrives within the window.
	// Zero takes llmwire's default of ninety seconds.
	IdleTimeout time.Duration
	Logger      *slog.Logger
	// Pro and ShortGate, when set, replace the two deployment names on the
	// wire. The PRODUCT never sets them — its deployments are the constants
	// above, and config.Load reads no variable for them. They exist for the
	// evaluation harness alone, whose job includes asking whether the next
	// model answers better than this one, and which can only ask that by
	// running the same pipeline against another name. The name must be one
	// llmwire's registry knows; an unknown one fails the first call.
	Pro       string
	ShortGate string
	// TurnMaxTokens is the most a single turn may spend across all of its
	// calls, prompt and completion, read off the usage meter on the context
	// before each request. A tripwire, not a budget: the turn is a fixed
	// pipeline whose cost is bounded by its per-call caps, and this is what
	// says so out loud once a loop or a retry that nobody bounded appears.
	// Zero turns it off. A context without a meter is never checked.
	TurnMaxTokens int
	// EmulateOpenCode presents every request as the opencode client: its
	// User-Agent and the session header pair. MiMo's token-plan host serves
	// that client; a neutral User-Agent is not what its traffic looks like.
	EmulateOpenCode bool
}

// ErrTurnBudget is why a call was refused when the turn had already spent
// its TurnMaxTokens. Wrapped with the figures; callers test it with
// errors.Is and say something a reader can act on, never the raw text.
var ErrTurnBudget = errors.New("turn token budget spent")

// Message is one OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is what one call cost, as the upstream reported it and llmwire
// decoded it. Returned to the caller, and also recorded into the usage.Meter
// on the context when there is one — that is how a turn's total reaches
// message_usage.
type Usage struct {
	Prompt     int
	Completion int
	Total      int
	// The two details objects the endpoint sends on every reply. Both hold a
	// SUBSET of the count above them, never an addition — Total equals
	// prompt plus completion whether anything was cached or not, which is
	// why a turn's figures looked complete while the cached share was being
	// discarded by the decoder.
	PromptDetails     *PromptDetails
	CompletionDetails *CompletionDetails
	// CostNanoUSD is what llmwire priced the call at from its own table, in
	// billionths of a dollar; nil when it had no rate for the model.
	CostNanoUSD *int64
}

// PromptDetails is how much of the prompt the upstream did not have to read
// again. Priced at cache_read, far under the input price.
type PromptDetails struct {
	Cached int
}

// CompletionDetails is how much of the completion went on thinking rather
// than on the text the reader sees. It comes out of the same completion cap.
type CompletionDetails struct {
	Reasoning int
}

// usageFrom maps llmwire's accounting onto Usage. A nil lane is one the
// upstream did not report, and stays absent, so record leaves the figure out
// rather than writing a zero that reads as "cached nothing". llmwire keeps
// the lanes, not the objects: a details object present but without the
// field is the same as no object. MiMo sends both fields on every reply, so
// nothing is lost there.
func usageFrom(u llmwire.Usage) Usage {
	var out Usage
	if u.Input.Total != nil {
		out.Prompt = int(*u.Input.Total)
	}
	if u.Output.Total != nil {
		out.Completion = int(*u.Output.Total)
	}
	if total, ok := u.Total(); ok {
		out.Total = int(total)
	}
	if u.Input.CacheRead != nil {
		out.PromptDetails = &PromptDetails{Cached: int(*u.Input.CacheRead)}
	}
	if u.Output.Reasoning != nil {
		out.CompletionDetails = &CompletionDetails{Reasoning: int(*u.Output.Reasoning)}
	}
	if u.Cost.Provenance != llmwire.Unpriced {
		out.CostNanoUSD = usage.Nano(u.Cost.NanoUSD)
	}
	return out
}

// FinishError is how the upstream ended a completion when it was not a
// normal stop: "length" when the completion budget ran out, "content_filter"
// and the like. Both Complete and Stream return it, and Stream returns it only
// after every content delta was delivered, so the caller decides what the text
// it already has is worth. The completion count is what says whether the
// budget was the cause.
type FinishError struct {
	Reason     string
	Completion int
}

func (e *FinishError) Error() string {
	return fmt.Sprintf("chat completion ended with finish_reason=%s after %d completion tokens",
		e.Reason, e.Completion)
}

// callOptions is what the Option funcs assemble.
type callOptions struct {
	model       string
	thinkingOff bool
	maxTokens   int
	temperature *float64
	step        string
}

// WithStep labels the call for the usage meter: the word a reader sees next
// to its tokens in the breakdown. A call without one is recorded as "llm",
// which is a bug to fix at the call site, not a mode.
func WithStep(name string) Option {
	return func(o *callOptions) { o.step = name }
}

// record writes one call into the context's meter, if a turn is metering.
// took is wall clock for the whole call, request to last byte.
func record(ctx context.Context, o callOptions, u Usage, took time.Duration) {
	step := o.step
	if step == "" {
		step = "llm"
	}
	c := usage.Call{
		Step:       step,
		Model:      o.model,
		Prompt:     u.Prompt,
		Completion: u.Completion,
		Ms:         usage.Int(int(took.Milliseconds())),
	}
	// A details object saying zero is a measurement — this call cached
	// nothing — and is recorded as zero. Only a reply that carried no details
	// object at all leaves the figure absent.
	if u.PromptDetails != nil {
		c.Cached = usage.Int(u.PromptDetails.Cached)
	}
	if u.CompletionDetails != nil {
		c.Reasoning = usage.Int(u.CompletionDetails.Reasoning)
	}
	c.CostNanoUSD = u.CostNanoUSD
	usage.Record(ctx, c)
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

// Pro routes this call to the Pro deployment, which is also what a call that
// names no deployment gets (see resolve). It exists so a caller can say Pro
// rather than say nothing: "the default" and "Pro on purpose" read the same in
// code and mean different things to whoever changes the default next. The eval
// harness needs the deliberate form, because it measures the two lanes against
// each other and a lane selected by omission silently follows the default it
// is supposed to be compared with.
func Pro() Option {
	return func(o *callOptions) { o.model = ProDeployment }
}

// WithoutThinking disables MiMo's native reasoning for this call. It does not
// change the deployment: a Pro call can be asked not to think, and a short-gate
// call can be asked to.
func WithoutThinking() Option {
	return func(o *callOptions) { o.thinkingOff = true }
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
	wire *llmwire.Client
	log  *slog.Logger
	// pro and shortGate are the wire names for the two lanes; see Config.
	pro, shortGate string
	turnMaxTokens  int
}

// underBudget refuses the next call once the turn's meter has reached the
// ceiling. Checked before the request and never mid-stream: a call that
// started runs to its own completion cap, and the tripwire stops the one
// after it.
func (c *Client) underBudget(ctx context.Context) error {
	if c.turnMaxTokens <= 0 {
		return nil
	}
	m := usage.MeterFrom(ctx)
	if m == nil {
		return nil
	}
	if spent := m.Total(); spent >= c.turnMaxTokens {
		return fmt.Errorf("%w: %d of %d tokens", ErrTurnBudget, spent, c.turnMaxTokens)
	}
	return nil
}

// deployment maps a lane to the name sent on the wire.
func (c *Client) deployment(lane string) string {
	switch {
	case lane == ProDeployment && c.pro != "":
		return c.pro
	case lane == ShortGateDeployment && c.shortGate != "":
		return c.shortGate
	}
	return lane
}

// NewClient builds a Client. hc may be nil, in which case llmwire makes one.
// A caller-supplied hc must not carry http.Client.Timeout: that bound caps
// body reads too and would cut a long answer mid-stream, which is what the
// named timeouts in Config exist to prevent.
//
// The error is a missing BACKEND_CHAT_BASE_URL or BACKEND_CHAT_API_KEY,
// named. Both lanes live on the one host, so the Pro profile's variables
// serve the gate as well.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	wire, err := llmwire.FromEnv(ProDeployment, llmwire.Config{
		BaseURL:         cfg.BaseURL,
		APIKey:          cfg.APIKey,
		HeaderTimeout:   cfg.Timeout,
		IdleTimeout:     cfg.IdleTimeout,
		CallTimeout:     cfg.Timeout,
		HTTPClient:      hc,
		EmulateOpenCode: cfg.EmulateOpenCode,
	})
	if err != nil {
		return nil, err
	}
	return &Client{
		wire:          wire,
		log:           log,
		pro:           cfg.Pro,
		shortGate:     cfg.ShortGate,
		turnMaxTokens: cfg.TurnMaxTokens,
	}, nil
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

// request renders what one call sends. Thinking is left at the deployment's
// default — on — unless the call switched it off; temperature is omitted
// unless a call names one, so the endpoint's own default keeps applying to
// everything that does not care.
func (c *Client) request(msgs []Message, o callOptions) llmwire.ChatRequest {
	req := llmwire.ChatRequest{
		Model:       c.deployment(o.model),
		Messages:    make([]llmwire.Message, 0, len(msgs)),
		MaxTokens:   &o.maxTokens,
		Temperature: o.temperature,
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, llmwire.TextMessage(llmwire.Role(m.Role), m.Content))
	}
	if o.thinkingOff {
		req.Reasoning = llmwire.ReasoningOff()
	}
	return req
}

// warn logs what llmwire could not send as asked. Debug, because a warning
// here is a request the wire coerced and still sent, not a failed call.
func (c *Client) warn(ws []llmwire.Warning) {
	for _, w := range ws {
		c.log.Debug("llm: wire warning", "kind", w.Kind, "feature", w.Feature, "details", w.Details)
	}
}

// Complete runs one non-streaming call and returns the assistant's content.
func (c *Client) Complete(ctx context.Context, msgs []Message, opts ...Option) (string, Usage, error) {
	o := resolve(opts)
	if err := c.underBudget(ctx); err != nil {
		return "", Usage{}, err
	}
	// Timed from before the request so the figure is what a reader waited
	// for, queueing at the endpoint included, not what the endpoint spent
	// generating.
	started := time.Now()
	resp, warnings, err := c.wire.Chat(ctx, c.request(msgs, o))
	c.warn(warnings)
	if err != nil {
		return "", Usage{}, chatError(err)
	}
	u := usageFrom(resp.Usage)
	record(ctx, o, u, time.Since(started))
	// A reply cut at the cap is not a reply. Every caller here parses the
	// content, and a truncated JSON body read as "unparseable" would hide
	// that the budget was the cause.
	if fr := resp.FinishReason; fr != "" && fr != "stop" {
		return resp.Content, u, &FinishError{Reason: fr, Completion: u.Completion}
	}
	return resp.Content, u, nil
}

// Stream runs one streaming call, handing each content delta to onToken as it
// arrives. Only the final answer streams; every other step is an ordinary
// request.
func (c *Client) Stream(ctx context.Context, msgs []Message, onToken func(string), opts ...Option) (Usage, error) {
	o := resolve(opts)
	if err := c.underBudget(ctx); err != nil {
		return Usage{}, err
	}
	// Timed from before the request, closed when the last frame is read: for
	// the answer call that is the whole time the reader watched it write.
	started := time.Now()
	stream, warnings, err := c.wire.ChatStream(ctx, c.request(msgs, o))
	c.warn(warnings)
	if err != nil {
		return Usage{}, chatError(err)
	}
	defer stream.Close()

	// finishReason is how the upstream said the stream ended. Anything but a
	// normal stop is a failure the caller must hear about: a reasoning model
	// that spends the whole completion budget thinking ends with "length" and
	// not one content delta, and reading that as success once stored an empty
	// answer as a finished turn. The reason is kept and the stream read to
	// the end, because the usage frame that follows it carries the completion
	// count the error needs.
	var finishReason string
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case llmwire.EventContent:
			if ev.Text != "" && onToken != nil {
				onToken(ev.Text)
			}
		case llmwire.EventFinish:
			finishReason = ev.FinishReason
		}
	}
	// Recorded even when the read failed, as long as a usage frame arrived:
	// what the upstream reported was paid for. A stream that broke before
	// its usage frame (idle timeout, a dropped connection, an endpoint that
	// ignores include_usage) records nothing rather than zeros — a zero row
	// would read as "this call was free", and it was not; it is unknown.
	// Gated on the lanes, not on Reported(): an object carrying a bare
	// total_tokens or an empty details object counts as reported and would
	// record a 0/0 call — the free-looking row this guard exists to refuse.
	wu := stream.Usage()
	got := usageFrom(wu)
	if _, ok := wu.Total(); ok {
		record(ctx, o, got, time.Since(started))
	}
	if err := stream.Err(); err != nil {
		return got, chatError(err)
	}
	if finishReason != "" && finishReason != "stop" {
		return got, &FinishError{Reason: finishReason, Completion: got.Completion}
	}
	return got, nil
}

// chatError phrases a wire failure the way the rest of rongo reads it. An
// APIError without a status is the error frame an upstream sends after
// status 200; anything else keeps llmwire's own wording, which already names
// the bound that fired, carries no key (stripped by value, whatever its
// shape) and no request URL.
func chatError(err error) error {
	var apiErr *llmwire.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 0 {
			return fmt.Errorf("chat completion failed mid-stream (%s): %s", apiErr.Type, apiErr.Message)
		}
		return fmt.Errorf("chat completion failed with status %d: %s", apiErr.StatusCode, apiErr.Message)
	}
	return err
}
