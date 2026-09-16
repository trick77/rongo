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
// Session affinity and the opencode identity are llmwire's: the MiMo provider
// entry in its profiles.yaml switches them on, and the client then carries one
// session id per process, minted at construction and rotated after a
// thirty-minute idle gap. Calls are not pinned per thread.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/usage"
)

// The two default deployments. rongo is built and measured against MiMo;
// BACKEND_LLM_MODEL and BACKEND_LLM_GATE_MODEL replace them for a deployment
// that has a different model (Config.Pro, Config.ShortGate), and the name
// must be one llmwire's registry knows, checked at boot. A free-form host
// would let a misconfigured endpoint answer with a model nobody chose, and
// the failure would look like a quality problem rather than a configuration
// one; a profile id cannot, because llmwire validates every call against it.
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
// purpose. BaseURL and APIKey override the deployment's llmwire profile; left
// empty, the production case, llmwire uses the host its profile ships and
// reads LLMWIRE_MIMO_API_KEY itself. A test points BaseURL at its fake and no
// variable is consulted.
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
	// wire: the product from BACKEND_LLM_MODEL and BACKEND_LLM_GATE_MODEL, the
	// evaluation harness from its own variables when it asks whether the
	// next model answers better than this one. The name must be one llmwire's
	// registry knows, and both lanes must be served by the one host the
	// client is built for; NewClient refuses anything else.
	Pro       string
	ShortGate string
	// Policy is what the call sites' intents mean on the wire for the models
	// in use. Zero value = DefaultPolicy, the one rongo was measured with.
	Policy Policy
	// TurnMaxTokens is the most a single turn may spend across all of its
	// calls, prompt and completion, read off the usage meter on the context
	// before each request. A tripwire, not a budget: the turn is a fixed
	// pipeline whose cost is bounded by its per-call caps, and this is what
	// says so out loud once a loop or a retry that nobody bounded appears.
	// Zero turns it off. A context without a meter is never checked.
	TurnMaxTokens int
}

// The two reasoning values that are not an effort level. Anything else a
// Policy carries is a level the lane's profile must accept.
const (
	// ReasoningDefault leaves the field out: the model does what it does
	// when nobody asks.
	ReasoningDefault = "default"
	// ReasoningOff switches thinking off, however the model spells that.
	ReasoningOff = "off"
)

// Policy is how a deployment answers the two questions every call site
// asks in MiMo's terms. It is configuration (BACKEND_LLM_GATE_TEMPERATURE,
// BACKEND_LLM_GATE_REASONING, BACKEND_LLM_REASONING), because the answers
// were measured on MiMo and a different model has different ones.
type Policy struct {
	// GateTemperature is what a call that pins its temperature sends. nil
	// sends none, which leaves the endpoint's default.
	GateTemperature *float64
	// GateReasoning is what a gate call (WithoutThinking) sends: ReasoningOff,
	// ReasoningDefault or an effort level.
	GateReasoning string
	// ProReasoning is what every other call sends. Same values.
	ProReasoning string
}

// DefaultPolicy is what rongo was measured with: gate calls pinned at 0 with
// thinking off, the answer lane at the model's own default.
func DefaultPolicy() Policy {
	zero := 0.0
	return Policy{GateTemperature: &zero, GateReasoning: ReasoningOff, ProReasoning: ReasoningDefault}
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
	jsonObject  bool
	// attemptTimeout bounds ONE attempt, under whatever the caller's context
	// already allows. Zero means the caller's ceiling is the only bound,
	// which is what the answer call wants: there is one attempt worth making
	// and a reader watching it.
	attemptTimeout time.Duration
}

// WithJSONObject asks the endpoint for a JSON object and nothing else
// (response_format json_object). Both MiMo profiles honour it, and NO call
// uses it: measured on the five parsed calls (understand, the routing
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
	policy         Policy
	turnMaxTokens  int
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

// Deployment reports which profile a lane (ProDeployment or
// ShortGateDeployment) is served by, override applied.
func (c *Client) Deployment(lane string) string { return c.deployment(lane) }

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
// The error is a missing key variable, named. The client is built for the
// Pro lane's profile, whose host and key then serve the gate lane as well:
// one client, one host. A gate profile that llmwire would reach through a
// different host is refused here, at boot, because the alternative is a gate
// call that leaves for the Pro host under the gate's name and comes back as
// an unknown-model 400 on the first question.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	pro, gate := ProDeployment, ShortGateDeployment
	if cfg.Pro != "" {
		pro = cfg.Pro
	}
	if cfg.ShortGate != "" {
		gate = cfg.ShortGate
	}
	wire, err := llmwire.FromEnv(pro, llmwire.Config{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		HeaderTimeout: cfg.Timeout,
		IdleTimeout:   cfg.IdleTimeout,
		CallTimeout:   cfg.Timeout,
		HTTPClient:    hc,
	})
	if err != nil {
		return nil, err
	}
	// An explicit BaseURL is one host by construction (a test fake, a
	// stand-in endpoint), so the provider comparison would only ever send
	// its author to a variable that is not in play.
	if cfg.BaseURL == "" {
		if err := sameHost(wire.Registry(), pro, gate); err != nil {
			return nil, err
		}
	} else if _, err := wire.Registry().Lookup(gate); err != nil {
		return nil, err
	}
	pol := cfg.Policy
	if pol == (Policy{}) {
		pol = DefaultPolicy()
	}
	if pol.GateReasoning == "" {
		pol.GateReasoning = ReasoningOff
	}
	if pol.ProReasoning == "" {
		pol.ProReasoning = ReasoningDefault
	}
	// Reasoning is checked here, not left to BestEffort per call: a level
	// the model does not have is a configuration error with a known answer
	// (the levels it has), and a deployment should hear that at boot, not
	// run for a week with every gate call silently demoted. Both settings
	// against both models: WithoutThinking is a property of the call, not
	// of the lane, so a Pro call may be a gate call and receive GateReasoning.
	for _, model := range []string{pro, gate} {
		if err := checkReasoning(wire.Registry(), model, "BACKEND_LLM_GATE_REASONING", pol.GateReasoning); err != nil {
			return nil, err
		}
		if err := checkReasoning(wire.Registry(), model, "BACKEND_LLM_REASONING", pol.ProReasoning); err != nil {
			return nil, err
		}
	}
	return &Client{
		wire:          wire,
		log:           log,
		pro:           cfg.Pro,
		shortGate:     cfg.ShortGate,
		policy:        pol,
		turnMaxTokens: cfg.TurnMaxTokens,
	}, nil
}

// checkReasoning refuses a reasoning setting the lane's model cannot honour.
// ReasoningDefault always passes: it sends nothing. ReasoningOff on a model
// that cannot stop thinking, and a level outside the model's set, name the
// variable and what the model accepts.
func checkReasoning(reg *llmwire.Registry, model, variable, value string) error {
	if value == ReasoningDefault {
		return nil
	}
	p, err := reg.Lookup(model)
	if err != nil {
		return err
	}
	r := p.Reasoning
	if !r.Supported {
		// A model that never reasons already satisfies "do not reason", and
		// llmwire sends nothing for it; only a level asks for what is not there.
		if value == ReasoningOff {
			return nil
		}
		return fmt.Errorf("llm: %s=%q, but %s has no reasoning control; use %s", variable, value, model, ReasoningDefault)
	}
	if value == ReasoningOff {
		if !r.CanBeDisabled {
			return fmt.Errorf("llm: %s=%s, but %s cannot stop thinking; use %s or one of %v", variable, value, model, ReasoningDefault, r.EffortValues)
		}
		return nil
	}
	if !r.Accepts(value) {
		return fmt.Errorf("llm: %s=%q is not a level %s accepts; it takes %v, %s or %s", variable, value, model, r.EffortValues, ReasoningOff, ReasoningDefault)
	}
	return nil
}

// sameHost checks that both lanes are served by the provider the client was
// built for. A provider is one host in llmwire, so equal providers is the
// whole test; a model routed through the gateway by LLMWIRE_LITELLM_MODELS
// shows up here as the gateway's provider, which is what makes "both lanes
// through the gateway" and "both lanes at the vendor" pass and a mix fail.
func sameHost(reg *llmwire.Registry, pro, gate string) error {
	p, err := reg.Lookup(pro)
	if err != nil {
		return err
	}
	g, err := reg.Lookup(gate)
	if err != nil {
		return err
	}
	if p.Provider != g.Provider {
		return fmt.Errorf("llm: %s is served by %q and %s by %q; both lanes must share one host (list both in %s, or neither)",
			pro, p.Provider, gate, g.Provider, llmwire.GatewayModelsEnv)
	}
	return nil
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

// request renders what one call sends. A call site says what it means: a
// pinned temperature is "a re-roll here is a defect", thinking off is "this
// is a gate call, the output is an id or a label". What that means on the
// wire is the Policy's to say, because it was tuned on MiMo (pin at 0, no
// thought) and another model has other answers: the policy may send a
// different pin, no pin, an effort level, or leave the model's default.
//
// Those knobs are preferences, not requirements: a model that refuses one
// (the gpt-5 series answers any temperature but its default with a 400; some
// models cannot stop thinking) is still the model the deployment chose.
// BestEffort tells llmwire to send the nearest request it accepts, with a
// warning that warn logs, instead of failing the call before it leaves. It
// is set only when a knob is present, so a call that asks for nothing
// special keeps llmwire's strict validation.
func (c *Client) request(msgs []Message, o callOptions) llmwire.ChatRequest {
	req := llmwire.ChatRequest{
		Model:     c.deployment(o.model),
		Messages:  make([]llmwire.Message, 0, len(msgs)),
		MaxTokens: &o.maxTokens,
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, llmwire.TextMessage(llmwire.Role(m.Role), m.Content))
	}
	if o.temperature != nil {
		req.Temperature = c.policy.GateTemperature
	}
	reasoning := c.policy.ProReasoning
	if o.thinkingOff {
		reasoning = c.policy.GateReasoning
	}
	switch reasoning {
	case ReasoningDefault:
	case ReasoningOff:
		req.Reasoning = llmwire.ReasoningOff()
	default:
		req.Reasoning = llmwire.ReasoningEffort(reasoning)
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
	c.warn(c.deployment(o.model), warnings)
	if err != nil {
		return "", Usage{}, err
	}
	u := usageFrom(resp.Usage)
	record(ctx, o, c.deployment(o.model), u, time.Since(started))
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
	c.warn(c.deployment(o.model), warnings)
	if err != nil {
		return Usage{}, 0, err
	}
	defer stream.Close()

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
		record(ctx, o, c.deployment(o.model), got, time.Since(started))
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
