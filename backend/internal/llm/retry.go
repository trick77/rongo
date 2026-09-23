package llm

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/sched"
	"github.com/trick77/rongo/internal/usage"
)

// One retry, one policy, for every call rongo makes. A night's run ended one
// answer turn of ten on an empty completion and six of 28 on another estate:
// llmwire reported a stream idle, no response headers, an empty completion, a
// connection reset. Each of those failed the turn and threw away work the
// reader had already waited for.
//
// The rule is "nothing was delivered". A call that produced no content can be
// made again, because making it again is the only way to find out whether the
// failure was the transport; a call that delivered has spent its attempt, and
// what to do about a half-finished answer is the caller's decision, not the
// wire's.

const (
	// retryAfterCap bounds the wait a rate-limited call honours. A reader is
	// sitting in front of a turn that has written nothing, so a host asking
	// for a minute is asking for more than there is, and the call fails
	// instead of holding the turn open for it.
	retryAfterCap = 10 * time.Second
	// retryPause is the floor under every retry, and the width of the jitter
	// added to it. A failure that named no wait is still a failure: refiring
	// in the same instant hits an upstream that is having a moment with the
	// same burst that just bounced, and candidate naming fires one call per
	// candidate, so without the jitter a fan-out retries in lockstep.
	retryPause = 250 * time.Millisecond
)

// sleep is sched.Sleep, indirect so a test can hold the clock still: the waits
// here are short but real, and a suite honouring them spends that time doing
// nothing.
var sleep = sched.Sleep

// jitter is the random part of a retry wait, indirect for the same reason.
var jitter = func() time.Duration { return time.Duration(rand.Int64N(int64(retryPause))) } //nolint:gosec // jitter and backoff, not a secret: no security property depends on this value

// retriable is a positive list: a failure is retried because it is named here,
// never because it failed to match something. An error llmwire does not
// classify, or one from a layer that has not thought about this, is a failure
// nobody has decided is transient — and deciding it is transient by default is
// how a broken deployment gets asked twice for everything.
//
// err nil is the clean stop that produced no content, and only the
// transport's version of it is retried: a positive completion count is the
// model having written something the caller could not use — thinking that ate
// the budget, a filtered answer — and it would write it again.
//
// The three classes on the list are the upstream and the wire: ErrUpstream is
// a 5xx or a transport failure, ErrStreamIdle is a stream that went quiet
// inside its ninety seconds, ErrMalformedResponse is a proxy's error page in
// place of an answer. A rate limit joins them when the wait it asks for fits
// under the cap AND under what the caller has left, because waiting past
// either is worse than failing now.
//
// A deadline that fired while the CALLER's context is still live is the
// per-attempt window, and a second window is what that option is for.
//
// Everything else is refused on purpose: a 4xx is a request the endpoint
// rejected and rongo sends the same bytes twice; ErrResponseShape is a shape
// the endpoint chose to send; ErrCallCap and ErrNoResponseHeaders already ran
// the whole call window out, fifteen minutes by default, and a second window
// would hold the reader for half an hour; a FinishError is a budget the model
// spent; and a cancelled context has nobody waiting for the answer. The
// refusals are read first, because a call cap is a deadline too.
func retriable(ctx context.Context, err error, completion int) bool {
	if ctx.Err() != nil {
		return false
	}
	switch {
	case err == nil:
		return completion == 0
	case errors.Is(err, llmwire.ErrCallCap),
		errors.Is(err, llmwire.ErrNoResponseHeaders),
		errors.Is(err, llmwire.ErrAuth),
		errors.Is(err, llmwire.ErrBadRequest),
		errors.Is(err, llmwire.ErrResponseShape),
		errors.Is(err, ErrTurnBudget):
		return false
	case errors.Is(err, context.DeadlineExceeded):
		// The caller's own context is live — that was the first thing checked
		// — so the deadline that fired was the per-attempt window. Handing the
		// second attempt a window of its own is the whole point of having one.
		return true
	case errors.Is(err, llmwire.ErrRateLimited):
		wait := retryWait(err)
		if wait > retryAfterCap {
			return false
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
			return false
		}
		return true
	case errors.Is(err, llmwire.ErrUpstream),
		errors.Is(err, llmwire.ErrStreamIdle),
		errors.Is(err, llmwire.ErrMalformedResponse):
		return true
	}
	// A connection that died carries no llmwire class: a status is what gets
	// classified, and a dial failure, a reset and a dropped stream never had
	// one. They are named here by the stdlib's own types rather than left to
	// the default, because a reset mid-answer is the failure this whole
	// policy was written for.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// retryWait is how long to hold before the second call: the host's own
// Retry-After when it sent a usable one, and never less than the floor.
func retryWait(err error) time.Duration {
	var rl *llmwire.RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > retryPause {
		return rl.RetryAfter
	}
	return retryPause
}

// worthAnother decides whether to make the call again, and does the waiting.
// It logs only when the second call is actually going out, so the line means
// one more request reached the endpoint.
//
// The turn's ceiling is read again here: the first attempt was paid for, and a
// turn that spent the last of its budget on it must not spend more.
func (c *Client) worthAnother(ctx context.Context, o callOptions, err error, u Usage) bool {
	if !retriable(ctx, err, u.Completion) {
		return false
	}
	if m := usage.MeterFrom(ctx); m != nil && m.RetryFailed() {
		// A retry in this turn has already come back failing. The deployment
		// is having more than a moment, and doubling every remaining call of
		// the turn buys nothing but a slower failure.
		return false
	}
	if err := c.underBudget(ctx); err != nil {
		return false
	}
	wait := retryWait(err) + jitter()
	if !sleep(ctx, wait) {
		return false
	}
	c.log.Warn("llm: call retried, nothing was delivered",
		"step", o.step, "model", c.deployment(o.lane), "reason", retryReason(err),
		"wait", wait.String(), "completion_tokens", u.Completion)
	return true
}

// retryReason is what the warn line says the first call died of. A nil error
// is the call that ended cleanly with no content, which has no message of its
// own.
func retryReason(err error) string {
	if err == nil {
		return "empty completion"
	}
	return err.Error()
}

// attempted runs one call, and runs it a second time when the first delivered
// nothing and the failure is one of the transient ones. delivered says whether
// what came back reached the caller — content for a completion, a delta for a
// stream — because that is the whole of the rule.
//
// A second attempt that fails too marks the turn, and every later call of that
// turn goes out once: an outage costs a turn one doubled call, not one per
// step.
func (c *Client) attempted(ctx context.Context, o callOptions, call func() (Usage, error), delivered func() bool) (Usage, error) {
	u, err := call()
	u.Attempts = 1
	if delivered() || !c.worthAnother(ctx, o, err, u) {
		return u, err
	}
	u, err = call()
	u.Attempts = 2
	if !delivered() {
		if m := usage.MeterFrom(ctx); m != nil {
			m.MarkRetryFailed()
		}
	}
	return u, err
}
