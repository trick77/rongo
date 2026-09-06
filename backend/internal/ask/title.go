package ask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/sched"
)

// titleMaxTokens is tiny by design: this is a label of a few words, and a model
// given room will write a sentence.
const titleMaxTokens = 48

const (
	// titleAttempts is how many times a title is asked for before the
	// placeholder is accepted as the thread's name. A label of six words on the
	// short-gate lane is the cheapest call rongo makes, and the placeholder is
	// the question's first words — visibly a stand-in — so a hiccup upstream or
	// one reply that came back as a paragraph is worth another try, not a
	// thread named after its own question for good.
	titleAttempts = 3
	// titleAttemptTimeout bounds ONE attempt, and the caller's ceiling
	// (`titleCallTimeout` in the HTTP layer) covers all of them together:
	// without a per-attempt bound a single stalled call would spend the whole
	// budget the retries need. It stays generous because the two failures look
	// identical from here — a stalled call and a slow but healthy one — and
	// cutting a title off at a few seconds would lose labels that were on
	// their way.
	titleAttemptTimeout = 20 * time.Second
	// titleRetryPause keeps a failing upstream from being hit three times in
	// the same instant. Short: this runs inside a turn a person is watching.
	titleRetryPause = 250 * time.Millisecond
)

// titleSystem takes the language name as its one format argument.
const titleSystem = `Sum the question up in three to six words, as a title for a sidebar,
in %s. No quotation marks, no full stop at the end, no explanation - just the
title.`

// titleRetryNudge is what makes a retry worth paying for after a reply that
// was not a title. The call is pinned to temperature 0 and the thread pins it
// to one upstream node, so re-sending the same words would fetch the same
// paragraph back twice; the model has to be told what was wrong with it. A
// call that failed on the wire is retried unchanged — there was no reply to
// correct.
const titleRetryNudge = `That was not a title. Answer with the title only: one
line, three to six words, no preamble, no explanation.`

// Title writes a short label for a thread.
//
// It runs on the short-gate deployment with thinking off. That is deliberate
// and not an accident of copying: the output is a label, which is exactly the
// bar for the cheap queue, and a reasoning channel bleeding into a six-word
// title is worse than no title.
//
// A failed call and a reply that is not a title are both retried, up to
// titleAttempts. The caller must never let this block the answer. An empty
// string is still the normal end of a bad run: the placeholder made from the
// question's first words stays, and nobody needs to be told.
func Title(ctx context.Context, c *llm.Client, question string, lang Language) string {
	if c == nil {
		return ""
	}
	msgs := []llm.Message{
		{Role: "system", Content: fmt.Sprintf(titleSystem, languageName(lang)) + languageStyle(lang)},
		{Role: "user", Content: question},
	}
	// nudge carries the correction into the next attempt, and only after a
	// reply that was not a title.
	var nudge []llm.Message
	for attempt := 0; attempt < titleAttempts; attempt++ {
		if attempt > 0 && !sched.Sleep(ctx, titleRetryPause) {
			// The caller's ceiling ran out mid-run. What is left of it is not
			// worth another call.
			return ""
		}
		title, replied := titleOnce(ctx, c, append(msgs, nudge...))
		if title != "" {
			return title
		}
		if replied {
			nudge = []llm.Message{{Role: "user", Content: titleRetryNudge}}
		} else {
			nudge = nil
		}
		if ctx.Err() != nil {
			return ""
		}
	}
	return ""
}

// titleOnce is one attempt. It returns the title, or "" for every way the call
// can fail to produce one; `replied` separates the two kinds of failure — true
// when the upstream answered and the answer was not a title, false when the
// call itself failed.
func titleOnce(ctx context.Context, c *llm.Client, msgs []llm.Message) (title string, replied bool) {
	call, cancel := context.WithTimeout(ctx, titleAttemptTimeout)
	defer cancel()
	out, _, err := c.Complete(call, msgs,
		llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(titleMaxTokens), llm.WithStep("title"))
	if err != nil {
		// A completion cut off by the token cap is still a reply, and the
		// wrong shape is exactly what the nudge addresses.
		var fin *llm.FinishError
		return "", errors.As(err, &fin)
	}
	title = strings.TrimSpace(out)
	title = strings.Trim(title, "\"'“”.")
	// A model that answered with a paragraph did not write a title. Taking the
	// first line of it would put half a sentence in the sidebar.
	if strings.Contains(title, "\n") || len([]rune(title)) > 60 {
		return "", true
	}
	return title, true
}
