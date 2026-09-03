package ask

import (
	"context"
	"fmt"
	"strings"

	"github.com/trick77/rongo/internal/llm"
)

// titleMaxTokens is tiny by design: this is a label of a few words, and a model
// given room will write a sentence.
const titleMaxTokens = 48

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
// The caller must never let this block the answer. An empty string is the
// normal failure: the placeholder made from the question's first words stays,
// and nobody needs to be told.
func Title(ctx context.Context, c *llm.Client, question string, lang Language) string {
	if c == nil {
		return ""
	}
	out, _, err := c.Complete(ctx, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(titleSystem, languageNames[ParseLanguage(string(lang))])},
		{Role: "user", Content: question},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(titleMaxTokens))
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
