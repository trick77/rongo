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
	// placeholder is accepted as the thread's name. It counts REPLIES THAT
	// WERE NOT TITLES — a paragraph, a preamble, an empty line — because that
	// is the only failure this loop can do anything about: it re-asks with a
	// correction. A call that failed on the wire was already made twice by
	// llm.Client under the one retry policy, and the placeholder is a fine
	// answer for a deployment that is down.
	titleAttempts = 3
	// titleAttemptTimeout bounds ONE attempt, and the caller's ceiling
	// (`titleCallTimeout` in the HTTP layer) covers all of them together:
	// without a per-attempt bound a single stalled call would spend the whole
	// budget the retries need. Passed to the client, which owns the window
	// because it owns the retry. It stays generous because the two failures
	// look identical from here — a stalled call and a slow but healthy one —
	// and cutting a title off at a few seconds would lose labels that were on
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

// titleRetryNudge is what makes a retry worth paying for, and the reason this
// loop exists at all. The call is pinned to temperature 0 and the thread pins
// it to one upstream node, so re-sending the same words would fetch the same
// paragraph back twice; the model has to be told what was wrong with it. Every
// attempt after the first carries it, because a reply that was not a title is
// the only thing that gets here.
const titleRetryNudge = `That was not a title. Answer with the title only: one
line, three to six words, no preamble, no explanation.`

// Title writes a short label for a thread.
//
// It runs on the short-gate deployment with thinking off. That is deliberate
// and not an accident of copying: the output is a label, which is exactly the
// bar for the cheap queue, and a reasoning channel bleeding into a six-word
// title is worse than no title.
//
// A reply that was not a title is re-asked with a correction, up to
// titleAttempts; a call that failed on the wire is not re-asked here, because
// llm.Client already made it twice. The caller must never let this block the
// answer. An empty string is still the normal end of a bad run: the
// placeholder made from the question's first words stays, and nobody needs to
// be told.
func Title(ctx context.Context, c *llm.Client, question string, lang Language) string {
	if c == nil {
		return ""
	}
	msgs := []llm.Message{
		{Role: "system", Content: fmt.Sprintf(titleSystem, languageName(lang)) + languageStyle(lang)},
		{Role: "user", Content: question},
	}
	for attempt := 0; attempt < titleAttempts; attempt++ {
		asking := msgs
		if attempt > 0 {
			// Past the first attempt the reply was a paragraph, so the
			// correction goes with the question.
			if !sched.Sleep(ctx, titleRetryPause) {
				// The caller's ceiling ran out mid-run. What is left of it is
				// not worth another call.
				return ""
			}
			asking = append(msgs, llm.Message{Role: "user", Content: titleRetryNudge})
		}
		title, replied := titleOnce(ctx, c, asking)
		if title != "" {
			return title
		}
		if !replied {
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
	out, u, err := c.Complete(ctx, msgs,
		llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(titleMaxTokens),
		llm.WithAttemptTimeout(titleAttemptTimeout), llm.WithStep("title"))
	if err != nil {
		// A completion cut off by the token cap is still a reply, and the
		// wrong shape is exactly what the nudge addresses.
		var fin *llm.FinishError
		return "", errors.As(err, &fin)
	}
	title = strings.TrimSpace(out)
	title = strings.Trim(title, "\"'“”.")
	if title == "" && u.Attempts > 1 {
		// Nothing came back twice in a row. That is the deployment, not a
		// shape a correction could fix.
		return "", false
	}
	// A model that answered with a paragraph did not write a title. Taking the
	// first line of it would put half a sentence in the sidebar.
	if strings.Contains(title, "\n") || len([]rune(title)) > 60 {
		return "", true
	}
	return title, true
}
