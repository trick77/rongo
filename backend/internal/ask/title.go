package ask

import (
	"context"
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
	// titleAttemptTimeout bounds ONE attempt. The caller's ceiling
	// (`titleCallTimeout` in the HTTP layer) covers all of them together, so
	// without a per-attempt bound a single stalled call would spend the whole
	// budget the retries need.
	titleAttemptTimeout = 10 * time.Second
	// titleRetryPause keeps a failing upstream from being hit three times in
	// the same instant. Short: this runs inside a turn a person is watching.
	titleRetryPause = 250 * time.Millisecond
)

// titleSystem takes the language name as its one format argument.
const titleSystem = `Sum the question up in three to six words, as a title for a sidebar,
in %s. No quotation marks, no full stop at the end, no explanation - just the
title.`

// Title writes a short label for a thread.
//
// It runs on the short-gate deployment with thinking off. That is deliberate
// and not an accident of copying: the output is a label, which is exactly the
// bar for the cheap queue, and a reasoning channel bleeding into a six-word
// title is worse than no title.
//
// A failed call and a reply that is not a title are the same thing here and
// both are retried, up to titleAttempts. The caller must never let this block
// the answer. An empty string is still the normal end of a bad run: the
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
		if attempt > 0 && !sched.Sleep(ctx, titleRetryPause) {
			// The turn is over — the thread was deleted, or the caller's
			// ceiling ran out. There is nothing left to name.
			return ""
		}
		if title := titleOnce(ctx, c, msgs); title != "" {
			return title
		}
		if ctx.Err() != nil {
			return ""
		}
	}
	return ""
}

// titleOnce is one attempt: it returns the title, or "" for every way the call
// can fail to produce one.
func titleOnce(ctx context.Context, c *llm.Client, msgs []llm.Message) string {
	call, cancel := context.WithTimeout(ctx, titleAttemptTimeout)
	defer cancel()
	out, _, err := c.Complete(call, msgs,
		llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(titleMaxTokens), llm.WithStep("title"))
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(out)
	title = strings.Trim(title, "\"'“”.")
	// A model that answered with a paragraph did not write a title. Taking the
	// first line of it would put half a sentence in the sidebar.
	if strings.Contains(title, "\n") || len([]rune(title)) > 60 {
		return ""
	}
	return title
}
