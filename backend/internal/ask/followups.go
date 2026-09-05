package ask

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/trick77/rongo/internal/llm"
)

// followupsMaxTokens is small on purpose: this is three questions, and a model
// given room writes an essay around them.
const followupsMaxTokens = 200

// followupsMax is how many pills a finished answer offers. Three fits the row
// under the answer on a phone; more reads as a menu and competes with the
// answer for the reader's attention.
const followupsMax = 3

// followupsMaxRunes is the longest a suggestion may be. A pill is a line of
// text a thumb can hit, and anything longer is the model writing prose onto a
// line that happened to end in a question mark.
const followupsMaxRunes = 120

// followupsSystem takes the language name as its one format argument, twice:
// the language is named first and last, because the file list in between is
// English and a model that has just read it answers in English.
const followupsSystem = `You suggest what to ask next, in %s.

You are given a question that was just answered about a codebase, the answer it
got, and the files the answer was written from. Write two or three follow-up
questions the reader is likely to have now: the edge cases, the neighbouring
mechanism, the configuration - not a rephrasing of the question already asked.

Only suggest questions the listed files could answer. Never suggest a question
about a repository that is not among them.

One question per line. No numbering, no bullets, no quotation marks, no
explanation - just the questions, each one a whole question a person would
type, and each one in %s.`

// Followups suggests what to ask next, once an answer is finished.
//
// It runs on the short-gate deployment with thinking off, like Title: the
// output is a handful of one-line labels, which is exactly the bar for the
// cheap queue.
//
// An empty result is the normal failure and the only failure. A turn that has
// been answered is finished; nothing about it should break because a model
// could not think of a follow-up, so every error path here returns nil, which
// the caller reads as "no pills".
func Followups(
	ctx context.Context,
	c *llm.Client,
	question, answer string,
	audience Audience,
	sources []Source,
	scope Scope,
	lang Language,
) []string {
	if c == nil {
		return nil
	}
	name := languageName(lang)
	out, _, err := c.Complete(ctx, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(followupsSystem, name, name) + languageStyle(lang)},
		{Role: "user", Content: followupsPrompt(question, answer, audience, sources, scope)},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(followupsMaxTokens), llm.WithStep("followups"))
	if err != nil {
		return nil
	}
	return parseFollowups(out)
}

// followupsPrompt is what the model reads: the turn, and the paths it was
// written from. Paths and symbols only, never the gathered code — this call
// picks questions, and the chunks it would cost to send are the answer's job,
// not this one's.
func followupsPrompt(question, answer string, audience Audience, sources []Source, scope Scope) string {
	var b strings.Builder
	role := "a business analyst"
	if audience == AudienceDev {
		role = "a developer"
	}
	fmt.Fprintf(&b, "The reader is %s.\n\nQuestion:\n%s\n\nAnswer:\n%s\n\nThe answer was written from these files:\n", role, question, answer)
	for _, p := range followupsPaths(sources) {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	if len(scope.Unknown) > 0 {
		// The search dropped these names, so nothing in the answer is about
		// them. Offering to ask about them would promise an answer that can
		// only come back as "nothing found".
		fmt.Fprintf(&b, "\nNot indexed, never suggest a question about: %s\n", strings.Join(scope.Unknown, ", "))
	}
	return b.String()
}

// followupsPaths is the distinct repo/path list behind the sources, in a
// stable order. Distinct because gathering returns a chunk at a time and one
// file often supplies several — the same path five times says nothing more
// than the path once, and crowds the rest of the list out of the budget.
func followupsPaths(sources []Source) []string {
	seen := make(map[string]bool, len(sources))
	paths := make([]string, 0, len(sources))
	for _, s := range sources {
		p := s.Repo + "/" + s.Path
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// parseFollowups reads the reply defensively: a line that is not a question is
// not a suggestion, whatever the model meant by it.
func parseFollowups(out string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		q := cleanFollowup(line)
		if q == "" {
			continue
		}
		got = append(got, q)
		if len(got) == followupsMax {
			break
		}
	}
	return got
}

// cleanFollowup strips the decoration a model puts on a list and returns the
// question, or "" when the line is not one.
func cleanFollowup(line string) string {
	q := strings.TrimSpace(line)
	// Numbering ("1.", "2)") and bullets ("-", "*", "•"), in that order: a
	// model asked for neither writes both.
	q = strings.TrimLeft(q, "-*•\t ")
	for i, r := range q {
		if unicode.IsDigit(r) {
			continue
		}
		if i > 0 && (r == '.' || r == ')') {
			q = strings.TrimSpace(q[i+1:])
		}
		break
	}
	q = strings.Trim(q, "\"'“”")
	q = strings.TrimSpace(q)
	if !strings.Contains(q, "?") {
		return ""
	}
	if len([]rune(q)) > followupsMaxRunes {
		return ""
	}
	return q
}
