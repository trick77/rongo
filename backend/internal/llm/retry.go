package llm

import (
	"context"
	"errors"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/sched"
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

// retryAfterCap bounds the wait a rate-limited call honours. A reader is
// sitting in front of a turn that has written nothing, so a host asking for a
// minute is asking for more than there is, and the call fails instead of
// holding the turn open for it.
const retryAfterCap = 10 * time.Second

// sleep is sched.Sleep, indirect so a test can hold the clock still: the one
// wait here is a host's Retry-After, and honouring it in real time would spend
// that second doing nothing.
var sleep = sched.Sleep

// retriable reports whether a call that delivered nothing is worth making
// again. completion is what the endpoint said it charged for.
//
// err nil is the clean stop that produced no content, and only the
// transport's version of it is retried: a positive completion count is the
// model having written something the caller could not use — thinking that ate
// the budget, a filtered answer — and it would write it again.
//
// The refusals are llmwire's own sentinels rather than a status range. A
// request the endpoint rejected — a key, a parameter, an unknown model — is
// rejected again, because rongo sends the same bytes twice, and a response
// shape the endpoint chose to send is not transient either, which is the
// distinction llmwire draws it for. The turn's token ceiling is this process's
// arithmetic and is no smaller a moment later.
//
// Never a failure that already ran the call window out: ErrCallCap and
// ErrNoResponseHeaders are both bound by BACKEND_LLM_TIMEOUT, fifteen minutes
// by default, and a second window would hold the reader for half an hour. The
// idle bound is ninety seconds and stays retriable, as do a reset, a 5xx and
// an empty completion.
//
// A rate limit says come back, not no, and is the one refusal worth another
// call — but only when the wait it asks for fits under the cap. Past that the
// host has already said it will refuse.
func retriable(err error, completion int) bool {
	switch {
	case err == nil:
		return completion == 0
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, llmwire.ErrRateLimited):
		return retryWait(err) <= retryAfterCap
	case errors.Is(err, llmwire.ErrAuth),
		errors.Is(err, llmwire.ErrBadRequest),
		errors.Is(err, llmwire.ErrResponseShape),
		errors.Is(err, llmwire.ErrCallCap),
		errors.Is(err, llmwire.ErrNoResponseHeaders),
		errors.Is(err, ErrTurnBudget):
		return false
	}
	var fin *FinishError
	return !errors.As(err, &fin)
}

// retryWait is the wait the host asked for: a rate limit's Retry-After as it
// was sent, and zero for every other failure. Zero means no usable hint rather
// than "wait nothing special", and the second call goes out at once.
func retryWait(err error) time.Duration {
	var rl *llmwire.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		return 0
	}
	return rl.RetryAfter
}

// worthAnother decides whether to make the call again, and does the waiting.
// It logs only when the second call is actually going out, so the line means
// one more request reached the endpoint.
func (c *Client) worthAnother(ctx context.Context, o callOptions, err error, u Usage) bool {
	if ctx.Err() != nil || !retriable(err, u.Completion) {
		return false
	}
	wait := retryWait(err)
	if !sleep(ctx, wait) {
		return false
	}
	c.log.Warn("llm: call retried, nothing was delivered",
		"step", o.step, "model", c.deployment(o.model), "reason", retryReason(err),
		"wait", wait.String(), "completion_tokens", u.Completion)
	return true
}

// retryReason is what the warn line says the first call died of. A nil error
// is the stream that ended cleanly with no content, which has no message of
// its own.
func retryReason(err error) string {
	if err == nil {
		return "empty completion"
	}
	return err.Error()
}
