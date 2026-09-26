// Package llm talks to rongo's two model lanes over an OpenAI-compatible
// chat completions endpoint.
//
// The wire is github.com/trick77/llmwire: it renders the request the way each
// deployment wants it, reads the stream, decodes usage, redacts the key out of
// error bodies and bounds the call by named timeouts. What stays here is what
// rongo means by a call — two lanes, an answer cap on every request, the
// usage meter and the per-turn ceiling. One completion call and one streaming
// call; no tools, no image path.
//
// Every model fact is llmwire's: hosts, keys, reasoning levels, output limits,
// session identity. A call here says what it wants (a gate or an answer, how
// long an answer may be) and llmwire renders that for whichever model the
// lane is configured with, so swapping a lane's model is configuration only.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/usage"
)

// Lane is which of the two configured models a call goes to. rongo names no
// model itself: BACKEND_LLM_MODEL and BACKEND_LLM_GATE_MODEL say which
// llmwire profile serves each lane (Config.Answer, Config.Gate), both
// mandatory, checked at boot. A free-form host would let a misconfigured
// endpoint answer with a model nobody chose, and the failure would look like
// a quality problem rather than a configuration one; a profile id cannot,
// because llmwire validates every call against it.
//
// The lanes are told apart by name, never by model: both may be the same
// profile, and each still moves alone.
type Lane int

const (
	// LaneAnswer writes what a person reads, and is what a call naming no
	// lane gets.
	LaneAnswer Lane = iota
	// LaneGate returns an id or a label. See ShortGate.
	LaneGate
)

// defaultMaxAnswerTokens caps the answer of a call that names no budget of its
// own. Every request carries a cap: an uncapped one can run until the context
// dies, and the bill is where that shows up first.
const defaultMaxAnswerTokens = 4096

// Config holds the endpoint settings. BaseURL and APIKey send both lanes to
// one host; left empty, the production case, llmwire sends each lane's model
// to the host its profile ships with the key variable that profile names, so
// the lanes may live with different providers. A test points BaseURL at its
// fake and no variable is consulted.
type Config struct {
	BaseURL string
	APIKey  string
	// Timeout bounds the whole call, and also how long the endpoint may take
	// to send response headers: an endpoint under load can queue a call
	// before it answers, and llmwire's one-minute header default would end a
	// queued call that was about to be served. The allowance costs nothing on
	// a fast reply. Zero takes llmwire's defaults.
	Timeout time.Duration
	// IdleTimeout aborts a stream when no frame arrives within the window.
	// Zero takes llmwire's default of ninety seconds.
	IdleTimeout time.Duration
	Logger      *slog.Logger
	// Answer and Gate are the llmwire profile ids of the two lanes: the
	// product's from BACKEND_LLM_MODEL and BACKEND_LLM_GATE_MODEL, the
	// evaluation harness's from its own variables when it measures another
	// model. Both are required, and each must be a chat model able to do what
	// its lane does (answerNeeds, gateNeeds); NewClient refuses anything else,
	// naming the models that would do.
	Answer string
	Gate   string
	// GateTemperature is what a call that pins its temperature sends
	// (WithGateTemperature); nil sends none. Policy, not a model fact: a
	// decision must not re-roll between runs. config.Load holds the default.
	GateTemperature *float64
	// Registry and Lookup replace llmwire's shipped profiles and the process
	// environment. Tests only; nil in production.
	Registry *llmwire.Registry
	Lookup   func(string) (string, bool)
	// TurnMaxTokens is the most a single turn may spend across all of its
	// calls, prompt and completion, read off the usage meter on the context
	// before each request. A tripwire, not a budget: the turn is a fixed
	// pipeline whose cost is bounded by its per-call caps, and this is what
	// says so out loud once a loop or a retry that nobody bounded appears.
	// Zero turns it off. A context without a meter is never checked.
	TurnMaxTokens int
}

// What each lane does with its model, checked at boot. The answer streams to
// a reader; the gate lane runs the locate loop's tool calls.
var (
	answerNeeds = llmwire.Needs{Streaming: true}
	gateNeeds   = llmwire.Needs{Tools: true}
)

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
	// Attempts is how many requests this call took, 1 or 2. A second one
	// happens only when the first delivered nothing. It is not billing: both
	// attempts are recorded in the turn's meter, and these are the figures of
	// the one that came back.
	Attempts int
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
// field is the same as no object.
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
	lane           Lane
	thinkingOff    bool
	maxTokens      int
	pinTemperature bool
	step           string
	jsonObject     bool
	// attemptTimeout bounds ONE attempt, under whatever the caller's context
	// already allows. Zero means the caller's ceiling is the only bound,
	// which is what the answer call wants: there is one attempt worth making
	// and a reader watching it.
	attemptTimeout time.Duration
}

// WithJSONObject asks the endpoint for a JSON object and nothing else
// (response_format json_object). NO call uses it: measured on the five parsed calls (understand, the routing
// judges, naming, the reranker) it moved the flow corpus 28/30+26/30 to
// 25/27+26/30, inside judge noise, and the baseline had zero decode failures
// for it to fix — the prompt plus llmwire.JSONObject at the call site already
// reads every reply (docs/measurements/2026-09-13-json-object.md). It stays
// defined so the next person finds that table before re-measuring.
func WithJSONObject() Option {
	return func(o *callOptions) { o.jsonObject = true }
}

// WithAttemptTimeout bounds one attempt rather than the whole call, so a
// stalled first attempt leaves the retry a window of its own. Without it a
// caller that wants both has to wrap each attempt itself, and the retry lives
// here now.
func WithAttemptTimeout(d time.Duration) Option {
	return func(o *callOptions) { o.attemptTimeout = d }
}

// WithStep labels the call for the usage meter: the word a reader sees next
// to its tokens in the breakdown. A call without one is recorded as "llm",
// which is a bug to fix at the call site, not a mode.
func WithStep(name string) Option {
	return func(o *callOptions) { o.step = name }
}

// record writes one call into the context's meter, if a turn is metering.
// took is wall clock for the whole call, request to last byte. model is the
// name that went on the wire (deployment applied), not the lane: a turn served
// by an override shows the model it actually paid for.
func record(ctx context.Context, o callOptions, model string, u Usage, took time.Duration) {
	step := o.step
	if step == "" {
		step = "llm"
	}
	c := usage.Call{
		Step:       step,
		Model:      model,
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

// ShortGate routes this call to the gate lane, the one configured for calls
// whose output is an id or a label.
//
// It says nothing about reasoning. The lane may think when asked to;
// suppressing thought is WithoutThinking, a separate switch. Never describe this as "the model that cannot think" — the
// bar for using it is "the output is an id or a label", not "no thought needed".
func ShortGate() Option {
	return func(o *callOptions) { o.lane = LaneGate }
}

// Pro routes this call to the answer lane, which is also what a call that
// names no lane gets (see resolve). It exists so a caller can say so rather
// than say nothing: "the default" and "the answer lane on purpose" read the
// same in code and mean different things to whoever changes the default next.
// The eval harness needs the deliberate form, because it measures the two
// lanes against each other and a lane selected by omission silently follows
// the default it is supposed to be compared with.
func Pro() Option {
	return func(o *callOptions) { o.lane = LaneAnswer }
}

// WithoutThinking asks for the shallowest reasoning the lane's model allows
// (llmwire.ReasoningMinimal): off where the model can switch it off, its
// lowest level otherwise. For gate calls only — an id, a label, a decision.
// On some models minimal is thinking off, which measurably costs prose and
// arithmetic their correctness; nothing a reader keeps may use it. It does
// not change the lane: an answer-lane call can be asked not to think, and a
// short-gate call can be asked to.
func WithoutThinking() Option {
	return func(o *callOptions) { o.thinkingOff = true }
}

// WithMaxAnswerTokens caps the visible answer. The reasoning allowance on top
// is llmwire's, sized for the reasoning setting actually sent and clamped to
// the model's output limit, so n is sized for the answer alone. Use it
// wherever a truncated reply is not worse than a long one.
func WithMaxAnswerTokens(n int) Option {
	return func(o *callOptions) { o.maxTokens = n }
}

// WithGateTemperature pins the sampling temperature for this call to what the
// policy says a gate call sends. A call that does not ask for it sends no
// temperature at all, and the endpoint's default applies — which is what
// every call did before this existed. It takes no value on purpose: the pin
// is the policy's (BACKEND_LLM_GATE_TEMPERATURE), and an argument here was
// ignored for a release while every caller passed one.
//
// Every call in internal/ask whose output is an id, a label or a one-word
// decision pins — the understanding, the routing judge, the candidate
// naming, the thread title, the reranker. None of them is read as prose, and
// a re-roll on any of them is a defect rather than variety.
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
func WithGateTemperature() Option {
	return func(o *callOptions) { o.pinTemperature = true }
}

// Client calls the chat completions endpoint.
type Client struct {
	wire *llmwire.Client
	log  *slog.Logger
	// answer and gate are the profile ids of the two lanes; see Config.
	answer, gate    string
	gateTemperature *float64
	turnMaxTokens   int
	// demoted records the features warn has already reported, keyed by
	// feature name; see warn.
	demoted sync.Map
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

// Deployment reports which profile a lane is served by.
func (c *Client) Deployment(lane Lane) string { return c.deployment(lane) }

// deployment maps a lane to the name sent on the wire.
func (c *Client) deployment(lane Lane) string {
	if lane == LaneGate {
		return c.gate
	}
	return c.answer
}

// NewClient builds a Client. hc may be nil, in which case llmwire makes one.
// A caller-supplied hc must not carry http.Client.Timeout: that bound caps
// body reads too and would cut a long answer mid-stream, which is what the
// named timeouts in Config exist to prevent.
//
// The error is an unconfigured lane, a model that cannot do its lane's work
// (with the models that could), or a missing key variable, named. Each lane
// reaches its own model's host with its own provider's key: the lanes may
// live with different providers.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	answer, gate := cfg.Answer, cfg.Gate
	if answer == "" {
		return nil, errors.New("llm: no answer model; set BACKEND_LLM_MODEL to an llmwire profile id")
	}
	if gate == "" {
		return nil, errors.New("llm: no gate model; set BACKEND_LLM_GATE_MODEL to an llmwire profile id")
	}
	reg := cfg.Registry
	if reg == nil {
		reg = llmwire.Default()
	}
	if err := requireLane(reg, "BACKEND_LLM_MODEL", answer, answerNeeds); err != nil {
		return nil, err
	}
	if err := requireLane(reg, "BACKEND_LLM_GATE_MODEL", gate, gateNeeds); err != nil {
		return nil, err
	}
	wire, err := llmwire.FromEnvModels(llmwire.Config{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		HeaderTimeout: cfg.Timeout,
		IdleTimeout:   cfg.IdleTimeout,
		CallTimeout:   cfg.Timeout,
		HTTPClient:    hc,
		Registry:      cfg.Registry,
		Lookup:        cfg.Lookup,
	}, answer, gate)
	if err != nil {
		return nil, err
	}
	return &Client{
		wire:            wire,
		log:             log,
		answer:          answer,
		gate:            gate,
		gateTemperature: cfg.GateTemperature,
		turnMaxTokens:   cfg.TurnMaxTokens,
	}, nil
}

// requireLane refuses a lane's model that is unknown, not a chat model, or
// short of what the lane does, naming the variable and the models that would
// do. llmwire's error stays wrapped for errors.As.
func requireLane(reg *llmwire.Registry, variable, model string, needs llmwire.Needs) error {
	_, err := reg.Require(model, needs)
	if err == nil {
		return nil
	}
	var unknown *llmwire.UnknownModelError
	if errors.As(err, &unknown) {
		return fmt.Errorf("llm: %s=%q is not an llmwire model; valid choices are %s: %w",
			variable, model, strings.Join(reg.ChatModels(needs), ", "), err)
	}
	return fmt.Errorf("llm: %s: %w", variable, err)
}

func resolve(opts []Option) callOptions {
	o := callOptions{lane: LaneAnswer, maxTokens: defaultMaxAnswerTokens}
	for _, fn := range opts {
		fn(&o)
	}
	if o.maxTokens <= 0 {
		o.maxTokens = defaultMaxAnswerTokens
	}
	return o
}

// request renders what one call sends. A call site says what it means: a
// pinned temperature is "a re-roll here is a defect", thinking off is "this
// is a gate call, the output is an id or a label". What either means for the
// lane's model is llmwire's, from the profile.
//
// Those knobs are preferences, not requirements: a model that refuses one
// (a forced temperature, say) is still the model the deployment chose.
// BestEffort tells llmwire to send the nearest request it accepts, with a
// warning that warn logs, instead of failing the call before it leaves. It
// is set only when a knob is present, so a call that asks for nothing
// special keeps llmwire's strict validation.
func (c *Client) request(msgs []Message, o callOptions) llmwire.ChatRequest {
	req := llmwire.ChatRequest{
		Model:           c.deployment(o.lane),
		Messages:        make([]llmwire.Message, 0, len(msgs)),
		MaxAnswerTokens: &o.maxTokens,
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, llmwire.TextMessage(llmwire.Role(m.Role), m.Content))
	}
	if o.pinTemperature {
		req.Temperature = c.gateTemperature
	}
	// Gate calls reason as little as the model allows; every other call is
	// read by a person and runs at the model's own default, deliberately.
	if o.thinkingOff {
		req.Reasoning = llmwire.ReasoningMinimal()
	}
	req.BestEffort = req.Temperature != nil || req.Reasoning != nil
	if o.jsonObject {
		req.ResponseFormat = llmwire.ResponseFormat{Kind: llmwire.FormatJSONObject}
	}
	return req
}

// warn logs what llmwire could not send as asked. Debug, because a warning
// here is a request the wire coerced and still sent, not a failed call.
func (c *Client) warn(model string, ws []llmwire.Warning) {
	for _, w := range ws {
		// A knob the model refused and BestEffort rewrote is the one warning
		// that changes what the model was asked: a gate call pinned at 0 ran
		// at the model's forced value, or thought when it was told not to.
		// Once per model and feature at Warn, so a deployment whose model
		// does that is told at the first question and not on every line.
		if w.Kind == llmwire.WarnUnsupported {
			if _, seen := c.demoted.LoadOrStore(model+"\x00"+w.Feature, true); !seen {
				c.log.Warn("llm: request knob refused by the model, sent without it", "model", model, "feature", w.Feature, "details", w.Details)
			}
			continue
		}
		c.log.Debug("llm: wire warning", "kind", w.Kind, "feature", w.Feature, "details", w.Details)
	}
}

// Complete runs one non-streaming call and returns the assistant's content.
//
// A call that came back with no content at all is made once more when it is
// worth it; see retry.go for what "worth it" means. The attempt count rides
// on the Usage so a step can report it.
func (c *Client) Complete(ctx context.Context, msgs []Message, opts ...Option) (string, Usage, error) {
	o := resolve(opts)
	if err := c.underBudget(ctx); err != nil {
		return "", Usage{}, err
	}
	var content string
	u, err := c.attempted(ctx, o,
		func() (Usage, error) {
			var u Usage
			var err error
			content, u, err = c.complete(ctx, o, msgs)
			return u, err
		},
		func() bool { return content != "" })
	return content, u, err
}

// complete is one attempt.
func (c *Client) complete(ctx context.Context, o callOptions, msgs []Message) (string, Usage, error) {
	ctx, cancel := c.attemptWindow(ctx, o)
	defer cancel()
	// Timed from before the request so the figure is what a reader waited
	// for, queueing at the endpoint included, not what the endpoint spent
	// generating.
	started := time.Now()
	resp, warnings, err := c.wire.Chat(ctx, c.request(msgs, o))
	c.warn(c.deployment(o.lane), warnings)
	if err != nil {
		return "", Usage{}, err
	}
	u := usageFrom(resp.Usage)
	record(ctx, o, c.deployment(o.lane), u, time.Since(started))
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
	// A stream is retried only while NOTHING has reached the caller. Once a
	// delta is out it is on a reader's screen, and a second call would write a
	// different answer over the one being read; what to do with the fragment
	// is the caller's decision. llmwire counts the characters it handed over,
	// which is the same fact without a flag beside the callback.
	var delivered int64
	return c.attempted(ctx, o,
		func() (Usage, error) {
			u, chars, err := c.stream(ctx, o, msgs, onToken)
			delivered = chars
			return u, err
		},
		func() bool { return delivered > 0 })
}

// attemptWindow bounds one attempt when the call asked for a per-attempt
// window, and hands the caller's own context back when it did not. The bound
// is always under the caller's: a ceiling is a ceiling.
func (c *Client) attemptWindow(ctx context.Context, o callOptions) (context.Context, context.CancelFunc) {
	if o.attemptTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, o.attemptTimeout)
}

// stream is one attempt. It reports how many characters reached onToken, which
// is what says whether this attempt delivered.
func (c *Client) stream(ctx context.Context, o callOptions, msgs []Message, onToken func(string)) (Usage, int64, error) {
	ctx, cancel := c.attemptWindow(ctx, o)
	defer cancel()
	// Timed from before the request, closed when the last frame is read: for
	// the answer call that is the whole time the reader watched it write.
	started := time.Now()
	stream, warnings, err := c.wire.ChatStream(ctx, c.request(msgs, o))
	c.warn(c.deployment(o.lane), warnings)
	if err != nil {
		return Usage{}, 0, err
	}
	defer func() { _ = stream.Close() }()

	// llmwire drains the stream and hands back what it assembled, error or
	// not. Recorded even when the read failed, as long as a usage frame
	// arrived: what the upstream reported was paid for. A stream that broke
	// before its usage frame (idle timeout, a dropped connection, an endpoint
	// that ignores include_usage) records nothing rather than zeros — a zero
	// row would read as "this call was free", and it was not; it is unknown.
	// Gated on the lanes, not on Reported(): an object carrying a bare
	// total_tokens or an empty details object counts as reported and would
	// record a 0/0 call — the free-looking row this guard exists to refuse.
	res, err := stream.Collect(onToken)
	got := usageFrom(res.Usage)
	if _, ok := res.Usage.Total(); ok {
		record(ctx, o, c.deployment(o.lane), got, time.Since(started))
	}
	if err != nil {
		return got, res.Chars, err
	}
	// Anything but a normal stop is a failure the caller must hear about: a
	// reasoning model that spends the whole completion budget thinking ends
	// with "length" and not one content delta, and reading that as success
	// once stored an empty answer as a finished turn. The usage frame follows
	// the finish, which is why the count the error needs is only here.
	if fr := res.FinishReason; fr != "" && fr != "stop" {
		return got, res.Chars, &FinishError{Reason: fr, Completion: got.Completion}
	}
	return got, res.Chars, nil
}
