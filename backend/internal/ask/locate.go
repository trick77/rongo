package ask

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
)

// The locate loop: after the walk, the crossings and the gap pass, the model
// may LOOK AGAIN, having read what the first look returned.
//
// Why a loop and not a ninth ranking knob. A question whose answer is one
// specific place — "wo wird die Anzahl Haustiere an Ledger gesetzt" — has nothing
// to rank: either the place was found or it was not. Four attempts to express
// that inside a single ranked pass were measured and rejected, all four
// failing the same way, because a chunk only one lane can see cannot win a
// fusion that rewards lane agreement.
//
// What the loop adds is not a better ranking. It is the ability to act on what
// came back: a scan for a guessed identifier that returns NOTHING is a fact
// about the corpus, and the next query should differ because of it. That is
// the one move a function computing a list cannot make, and it is the move a
// bare agent harness makes on the same index to answer the same question — two
// failed searches, then a narrowed one, then the file.
//
// It is deliberately NOT an agent that writes the answer. It gathers, and the
// answer is written afterwards by the one call that has always written it, out
// of numbered sources. Three constraints force that shape and they are not
// negotiable:
//
//   - Citations are positional and frozen before the first token: the answer
//     numbers its sources [1..n] and refuses any marker outside that range, so
//     everything the loop finds must be in the slice BEFORE the stream starts.
//     A loop that searched while streaming could not be cited from.
//   - Every source must carry a real chunk id. The admitter dedupes on it, the
//     budget is spent per chunk, and the thread stores sources as chunk ids
//     with no column for text that has none. So the tools return indexed
//     chunks — never a raw file read, never a raw ripgrep line, which is where
//     the harness loop in internal/retrieve/eval diverges from what can ship.
//   - The loop is a fourth phase under one budget and needs its own reserve,
//     or it resolves material it can never admit. That is the failure
//     crossingReserve and gapReserve were each written to prevent, and
//     reproducing it a third time is not interesting.
//
// On in the product at three rounds (BACKEND_LOCATE_ROUNDS); WithLocateLoop(nil)
// switches it off.

// locateReserve is the share of the token budget the walk, the crossing and
// the gap pass all leave untouched for the loop: one part in eight, 3000 of
// the default 24000 tokens.
//
// Larger than gapReserve because the loop admits chunks the model asked for
// BY NAME after reading the sources, which is the evidence a locate question
// turns on, and because a round that lands nothing still costs a call. What it
// costs the walk is measured by the harness arms, not assumed.
const locateReserve = 8

// locateMaxRounds is how many times the model may look again, NOT how many
// tools it may call: a model puts several calls in one turn, and the harness
// measured questions spending twenty-two calls inside six rounds. Three is
// what the observed trajectories need — a broad query, a narrowing, a read —
// with the round that answers on top.
const locateMaxRounds = 3

// locateMaxTokens caps each round's reply. A round asks for tools or says it
// is done; neither needs room to explain itself, and the answer is written
// later by another call entirely.
const locateMaxTokens = 700

// locateMaxCalls is how many tool calls one turn may run. mimo-v2.6-flash
// answered a first round with 38 calls in one reply and ran into the output
// cap mid-call: a guess-storm, not a search. The calls past the limit are
// not advertised back, so no call is left without its result.
const locateMaxCalls = 6

// locateMaxLandings is how many chunks one tool call may admit. A pattern
// matching a hundred files must not spend the whole reserve on one round.
const locateMaxLandings = 6

const locateSystem = `You are finding WHERE something happens in an indexed codebase. You have
already been given sources that a search gathered. Your job is to decide
whether the specific place the question asks about is among them, and if it
is not, to look for it.

Call tools to look, and READ what comes back. When you have found the place,
call nothing and reply in one or two sentences starting with
"FOUND: <repository> <path>:LINE", written as the tools showed them, and the
exact code on that line. If it is not in
the index, reply starting with "NOT FOUND:". Something else writes the answer
the reader sees; your sentence is what tells it which of many sources is the
one that matters.

Use the vocabulary of the CODE, not of the question. A German question about
"Anzahl Haustiere" is answered by code spelling it anzahlHaustiere, getAnzahlHaustiere
or setAnzahlhaustiere, and the index splits identifiers on punctuation, not on
case: grep finds a name INSIDE a longer one, search does not.

A tool that returns nothing is information. Do not repeat it with the same
argument; change the spelling, shorten the pattern, or try another tool.

Call at most 6 tools per turn. Pick the ones most likely to find the place;
you can look again after reading what they return.`

// LocateTools are the four the loop may call. Deliberately the capabilities
// rongo already has, each returning indexed chunks: a tool the product cannot
// resolve to a chunk id would gather material the answer cannot cite and the
// thread cannot store.
func locateTools() []llm.Tool {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return []llm.Tool{
		{
			Name:        "search",
			Description: "Hybrid semantic and keyword search. Best for a mechanism described in words.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": str("What to look for, in the vocabulary of the code."),
					"repo":  str("Optional repository name to restrict to."),
				},
				"required": []string{"query"},
			},
		},
		{
			Name: "grep",
			Description: "Substring scan over the indexed code. Finds an identifier INSIDE a longer one, " +
				"which keyword search cannot: use it for a spelling you can guess, such as setAnzahlhaustiere.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": str("The exact substring to find. No regular expressions."),
					"repo":    str("Optional repository name to restrict to."),
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "symbol",
			Description: "Find where a name is DEFINED: a function, method, type, constant or field.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": str("The identifier, spelled as the code spells it."),
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "read",
			Description: "Read the indexed chunk of a file at a line, to see what surrounds a hit.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo": str("Repository name."),
					"path": str("File path within the repository."),
					"line": map[string]any{"type": "integer", "description": "A line inside the chunk wanted."},
				},
				"required": []string{"repo", "path", "line"},
			},
		},
	}
}

// LocateReport is what the loop did, for the activity trace and the log. Every
// field comes from facts the loop already held: no second model call is made
// to describe it.
type LocateReport struct {
	// Skipped says why the loop did not run, empty when it did. One of
	// "off", "no sources", "call failed".
	Skipped string
	// Rounds is how many times the model was asked to look.
	Rounds int
	// Concluded is the closing call that asked what was found, which is
	// billed like any other. Counted apart from Rounds because it looked at
	// nothing: a trace reporting fewer calls than the meter was billed for
	// is a record disagreeing with the bill.
	Concluded bool
	// Calls is what it asked for, as "tool(argument)", in order.
	Calls []string
	// Landed is the calls that admitted something new.
	Landed []string
	// Empty is the calls that found nothing — the fact that makes the next
	// query different, and the one a reader most wants to see.
	Empty []string
	// Found is what the loop concluded, in its own words: the file, the line
	// and the code it landed on, or that the place is not in the index.
	//
	// It reaches the ANSWER prompt, and it is the difference between a loop
	// that gathers and a loop that answers. Ninety sources with no indication
	// which one holds the mapping is what the answer call faced before; one
	// sentence from something that just read the code is what a bare agent
	// harness has when it writes its reply. It is a POINTER, never the answer
	// itself: the answer is still written from the numbered sources, and
	// every claim in it still cites one.
	Found string
	// Refused is the calls cut short because the reserve was spent.
	Refused []string

	// Steps is every call in order, as the trace tells it. The label lists
	// above are for the log; the trace reads these.
	Steps []LocateStep
	// Outcome is what the conclusion did to the sources, and Place the
	// source it put first when it pointed. The trace shows these, never
	// Found: that is the model's prose, in the question's language, making
	// claims about code no citation backs.
	Outcome LocateOutcome
	Place   *LocatePlace
}

// LocateStep is one call the loop made. Arg is the query, pattern or symbol
// name; Repo, Path and Line are what a read opened. Matches is the lines a
// grep showed, Held how many of a call's landings the turn already had, Added
// how many it admitted. NotRun says why a call was refused before any lookup.
type LocateStep struct {
	Tool    string `json:"tool"`
	Arg     string `json:"arg,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Matches int    `json:"matches,omitempty"`
	Held    int    `json:"held,omitempty"`
	Added   int    `json:"added,omitempty"`
	NotRun  string `json:"not_run,omitempty"`
	// Cut is the call the reserve ran out on: it admitted what fit.
	Cut bool `json:"cut,omitempty"`

	label string
}

// LocatePlace is the source a conclusion put first.
type LocatePlace struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// LocateOutcome is how the loop's conclusion ended. Empty when it reached
// none: no lookup ran, or the closing call failed.
type LocateOutcome string

const (
	// LocatePointed means the named place is a gathered source, now the first.
	LocatePointed LocateOutcome = "pointed"
	// LocateNotFound means the model said the place is not in the index.
	LocateNotFound LocateOutcome = "not_found"
	// LocateUnpinned means it named a place no gathered source holds, so the
	// order stayed as retrieval left it.
	LocateUnpinned LocateOutcome = "unpinned"
)

// WithLocateLoop gives the gatherer the client the locate loop calls, and the
// searcher its search tool needs. Nil is off.
func (g *Gatherer) WithLocateLoop(c *llm.Client, s Searcher) *Gatherer {
	g.locate, g.locateSearch = c, s
	return g
}

// WithLocateRounds caps how many times the model may look, for the harness
// arm that measures ONE look against the loop. A loop is only worth its
// rounds if acting on what came back beats not acting on it, and that
// comparison needs both halves on the same corpus. Zero or less means
// locateMaxRounds; the product sets it from BACKEND_LOCATE_ROUNDS.
func (g *Gatherer) WithLocateRounds(n int) *Gatherer {
	g.locateRounds = n
	return g
}

// within is the gatherer confined to repos, the turn's ceiling: every
// admission outside it is skipped. A copy, so the shared gatherer is never
// changed. Empty repos is no ceiling.
func (g *Gatherer) within(repos []string) *Gatherer {
	c := *g
	c.ceiling = repos
	return &c
}

// allowed is the ceiling as the admitter reads it, nil for none.
func (g *Gatherer) allowed() map[string]bool {
	if len(g.ceiling) == 0 {
		return nil
	}
	set := make(map[string]bool, len(g.ceiling))
	for _, r := range g.ceiling {
		set[r] = true
	}
	return set
}

// forTurn is the gatherer a turn gathers with. A resumed turn never runs the
// loop (see Locate), so it gathers through a copy without it, and the walk,
// the crossing and the gap pass get the share the loop would otherwise hold
// back. The shared gatherer is never changed: the next fresh turn needs it.
func (g *Gatherer) forTurn(resumed bool) *Gatherer {
	if !resumed || g.locate == nil {
		return g
	}
	c := *g
	c.locate, c.locateSearch = nil, nil
	return &c
}

// rounds is the ceiling this gatherer runs the loop under.
func (g *Gatherer) rounds() int {
	if g.locateRounds > 0 && g.locateRounds < locateMaxRounds {
		return g.locateRounds
	}
	// Zero means the default, and anything above the ceiling is capped to
	// it: each round may run locateMaxCalls calls, so an unbounded setting
	// is an unbounded bill.
	return locateMaxRounds
}

// Locate runs the loop and returns the sources it was given plus whatever
// landed, with a report of what happened.
//
// Like the gap pass it returns an error only for a cancelled context or a
// database failure. A model failure of any kind is a warning in the log, the
// sources unchanged, and a reason in the report: every question was answered
// before this loop existed, and it may never do worse than nothing.
// known is the turn's repository restriction, empty meaning the whole corpus.
// It is a CEILING: a repository the model names is honoured only if the turn
// already reached it, and a name outside is reported rather than searched. A
// thread is a funnel — it narrows, never widens — and the loop is the one
// place where the MODEL picks what to look in, so the ceiling has to be
// applied here rather than trusted to the prompt.
func (g *Gatherer) Locate(ctx context.Context, question string, sources []Source, known []string, resumed bool, stage retrieve.StagePrefixes) ([]Source, LocateReport, error) {
	if g.locate == nil {
		return sources, LocateReport{Skipped: "off"}, nil
	}
	if resumed {
		// A module card stores the hits its candidate was built from and
		// replays them, because the answer has to be built from exactly what
		// was offered. The loop's search and grep tools look past that, so a
		// reader who chose a module would get an answer citing code the card
		// never showed — the quiet widening the funnel rule exists to stop.
		return sources, LocateReport{Skipped: "resumed"}, nil
	}
	if len(sources) == 0 {
		// Nothing gathered is a true answer, and the loop is not the place to
		// overturn it: a turn whose search came back empty says so.
		return sources, LocateReport{Skipped: "no sources"}, nil
	}

	// The full budget with the sources' cost recomputed, the way FillGaps
	// rebuilds it: the reserve exists so there is room left under the ceiling.
	a := &admitter{seen: map[int64]bool{}, budget: g.opts.TokenBudget, allowed: g.allowed()}
	for _, s := range sources {
		a.seen[s.ChunkID] = true
		a.spent += estimateTokens(s.Text)
	}
	a.out = append(a.out, sources...)

	msgs := []llm.ToolMessage{
		{Role: "system", Content: locateSystem},
		{Role: "user", Content: locatePrompt(question, sources)},
	}
	var report LocateReport
	// Every file the model was shown, path to repository, so the place its
	// conclusion names can be fetched even when no call admitted it.
	shown := shownFiles{}

	for round := 0; round < g.rounds(); round++ {
		// Counted BEFORE the call: a round that failed was still paid for,
		// and a trace reporting fewer rounds than the meter was billed for
		// is a record disagreeing with the bill.
		report.Rounds = round + 1
		turn, err := g.locate.CallTools(ctx, msgs, locateTools(),
			llm.ShortGate(), llm.WithoutThinking(), llm.WithGateTemperature(),
			llm.WithMaxTokens(locateMaxTokens), llm.WithStep("locate"))
		if err != nil && ctx.Err() == nil {
			// A fan-out that ran into the output cap still carries every call
			// before the one it was cut in: kept, and the round goes on.
			if calls := completeCalls(err, turn.Calls); len(calls) > 0 {
				g.logger().Warn("locate round cut at the cap; the complete calls kept",
					"round", round+1, "calls", len(turn.Calls), "kept", len(calls), "err", err)
				turn.Calls, err = calls, nil
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				// The reader left; there is no turn to look for.
				return sources, LocateReport{}, ctx.Err()
			}
			// Whatever landed in earlier rounds is kept: it is indexed code
			// the model asked for by name, and it is no worse than what the
			// walk gathered without it.
			g.logger().Warn("locate round failed; what landed so far kept",
				"round", round+1, "err", err)
			report.Skipped = "call failed"
			// A round cut at the cap still has the rounds before it: the
			// model read what they landed, and the pointer is worth most on
			// the turns that looked the longest. Asked from the messages
			// BEFORE the cut round — its calls are half-written arguments,
			// and advertising them without results is a malformed request.
			// A transport failure is left alone: the next call would meet
			// the same endpoint.
			var cut *llm.FinishError
			if errors.As(err, &cut) && len(report.Calls) > 0 {
				report.Found, report.Concluded = g.conclude(ctx, msgs), true
			}
			return g.finishLocate(ctx, a, &report, shown, stage), report, nil
		}
		if turn.Done() {
			// What it concluded, having read what it found. Kept only when a
			// tool actually ran: a first round that answers without looking
			// is the model talking about sources it was handed, which the
			// answer call can read for itself.
			if len(report.Calls) > 0 {
				report.Found = strings.TrimSpace(turn.Content)
			}
			break
		}

		// Calls over the limit follow the round's own calls in the trace,
		// which is the order a reader reads them in.
		var overLimit []LocateStep
		if len(turn.Calls) > locateMaxCalls {
			for _, over := range turn.Calls[locateMaxCalls:] {
				report.Refused = append(report.Refused, over.Name+"(over the call limit)")
				overLimit = append(overLimit, callStep(over).refusedAs("", "over the limit of calls in one round"))
			}
			turn.Calls = turn.Calls[:locateMaxCalls]
		}
		msgs = append(msgs, llm.AssistantCalls(turn.Content, turn.Calls))
		stop := false
		// Which call of this turn the reserve ran out on: the calls after it
		// are refused, and the count has to index the same list they do.
		refusedFrom := 0
		for ci, call := range turn.Calls {
			step, refused, display, landings, err := g.runLocateTool(ctx, call, a.out, known, stage, shown)
			if err != nil {
				return sources, LocateReport{}, err
			}
			label := step.label
			if refused != "" {
				// Refused before any lookup: not a search that came back
				// empty, and not looking. Reported as not run, and kept out
				// of Calls, which is what "a tool ran" is read from.
				report.Refused = append(report.Refused, label)
				report.Steps = append(report.Steps, step)
				msgs = append(msgs, llm.ToolResult(call.ID, refused))
				continue
			}
			report.Calls = append(report.Calls, label)
			if len(landings) == 0 && display != "" {
				// It looked and showed the model something, and admitted
				// nothing by itself: a grep. What it showed is the result.
				report.Steps = append(report.Steps, step)
				msgs = append(msgs, llm.ToolResult(call.ID, display))
				continue
			}
			if len(landings) == 0 {
				// A refusal took the other branch above, so this is a lookup
				// that ran and found nothing, and the model is told exactly
				// that.
				report.Empty = append(report.Empty, label)
				report.Steps = append(report.Steps, step)
				msgs = append(msgs, llm.ToolResult(call.ID, "Nothing found. Try a different spelling or another tool."))
				continue
			}
			before := len(a.out)
			// Which matches the turn already held, read BEFORE admitting:
			// the model is shown those too, marked, because the chunk that
			// answers is usually among them and hiding it hid the one line
			// the model needed to read.
			gathered := make([]bool, len(landings))
			for i, l := range landings {
				gathered[i] = a.seen[l.ChunkID]
				if gathered[i] {
					step.Held++
				}
			}
			for _, l := range landings {
				// Hop MaxHops+2: past the walk, the crossings and the gap
				// pass. Never hop 0 — hitRepos reads hop 0 to say which
				// repositories the question's own search reached, and a loop
				// landing is not that.
				if !a.take(l, g.opts.MaxHops+2) {
					stop, refusedFrom = true, ci
					break
				}
			}
			if len(a.out) > before {
				report.Landed = append(report.Landed, label)
			}
			step.Added, step.Cut = len(a.out)-before, stop
			report.Steps = append(report.Steps, step)
			// What the model reads is every match, gathered or not: for grep
			// the matching lines with their numbers, as a terminal grep shows
			// them; for the other tools a clipped excerpt. Reading is what
			// lets it tell the line that answers from a file that merely
			// contains the name.
			for _, l := range landings {
				shown[repoPath{l.Repo, l.Path}] = true
			}
			result := display
			if result == "" {
				result = locateResult(landings, gathered)
			}
			msgs = append(msgs, llm.ToolResult(call.ID, result))
			if stop {
				// The reserve ran out mid-call. Every call this turn
				// ADVERTISED still needs a result: the assistant message
				// above names them all, and an OpenAI-compatible endpoint
				// rejects a tool_calls turn with a result missing — so a
				// conclusion asked after this point would fail the request
				// rather than return a pointer.
				for _, rest := range turn.Calls[ci+1:] {
					msgs = append(msgs, llm.ToolResult(rest.ID,
						"Not run: the budget for this step is spent."))
				}
				break
			}
		}
		report.Steps = append(report.Steps, overLimit...)
		// The conclusion is asked on the last round AND when the reserve ran
		// out, which can happen on any round: the stop path is exactly where
		// the loop landed the most material, so dropping the pointer there
		// loses it on the turns it is worth most.
		if round == g.rounds()-1 || stop {
			// The last round spent its budget on tools, so nothing has asked
			// the model what it concluded. One more call with no tools does,
			// and without it Found is empty at every setting where the model
			// uses all its rounds looking — including rounds=1, where ANY
			// tool call exits through the loop condition rather than through
			// Done. The prompt block that carries the pointer would then be
			// dead code exactly when the looking worked.
			report.Found, report.Concluded = g.conclude(ctx, msgs), true
		}
		if stop {
			// The reserve is spent. Every call AFTER the one it ran out on is
			// refused without a lookup, the way the gap pass refuses the rest
			// of its names: the admitter has said what it cannot hold, and a
			// later small landing would be admitted out of the model's own
			// order.
			//
			// Indexed off the turn's own calls. An earlier version sliced the
			// labels by len(report.Landed), which counts a DIFFERENT list — a
			// call that found nothing lands in Empty, so the two drift apart
			// and the labels reported as refused were whichever ones the
			// offset happened to reach.
			for _, rest := range turn.Calls[refusedFrom+1:] {
				report.Refused = append(report.Refused, rest.Name+"(not tried)")
				report.Steps = append(report.Steps, callStep(rest).refusedAs("", "the token budget for this step was spent"))
			}
			break
		}
	}
	return g.finishLocate(ctx, a, &report, shown, stage), report, nil
}

// completeCalls is what survives of a turn cut at the output cap: the calls
// whose arguments parse. The cut lands inside the last call, so its
// arguments are half a JSON object; the ones before it are whole. Any other
// error keeps nothing.
func completeCalls(err error, calls []llm.ToolCall) []llm.ToolCall {
	var cut *llm.FinishError
	if !errors.As(err, &cut) || cut.Reason != "length" {
		return nil
	}
	var out []llm.ToolCall
	for _, c := range calls {
		if json.Valid([]byte(c.Arguments)) {
			out = append(out, c)
		}
	}
	return out
}

// pointedFirst moves the source the loop's conclusion names to the front,
// so the answer's first source is the place the loop read and named.
//
// The answer writes from the sources in order, and the order is retrieval's:
// fused lanes and a reranker scoring excerpts, none of which ever read the
// line that answers and decided it was the answer. The loop did.
//
// Only a conclusion starting "FOUND:" and naming File:LINE moves anything,
// and only to the gathered chunk of that file holding that line. A file named
// in passing, in a "NOT FOUND:" conclusion or without a line is not a place
// the loop stood behind, and promoting it would turn a rejection into the
// answer's opening.
func pointedFirst(sources []Source, found string) []Source {
	best, _ := pointedAt(sources, found)
	if best <= 0 {
		return sources
	}
	out := make([]Source, 0, len(sources))
	out = append(out, sources[best])
	out = append(out, sources[:best]...)
	return append(out, sources[best+1:]...)
}

// pointedAt is the index of the source a FOUND conclusion names, and the
// place it names; -1 when it names none a source holds.
func pointedAt(sources []Source, found string) (int, LocatePlace) {
	claim, ok := strings.CutPrefix(strings.TrimSpace(found), "FOUND:")
	if !ok {
		return -1, LocatePlace{}
	}
	files := make([]repoPath, 0, len(sources))
	seen := map[repoPath]bool{}
	for _, s := range sources {
		if f := (repoPath{s.Repo, s.Path}); !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	f, line, ok := resolvePlace(claim, files)
	if !ok {
		return -1, LocatePlace{}
	}
	for i, s := range sources {
		if s.Repo == f.repo && s.Path == f.path && line >= s.StartLine && line <= s.EndLine {
			return i, LocatePlace{Repo: f.repo, Path: f.path, Line: line}
		}
	}
	return -1, LocatePlace{}
}

// namedAt reports whether text names the file base as a whole word, and the
// line number written right after it as "base:123", or 0 when none is.
func namedAt(text, base string) (int, bool) {
	for from := 0; ; {
		i := strings.Index(text[from:], base)
		if i < 0 {
			return 0, false
		}
		i += from
		from = i + len(base)
		if i > 0 && isIdentRune(rune(text[i-1])) {
			continue
		}
		rest := text[from:]
		if !strings.HasPrefix(rest, ":") {
			return 0, true
		}
		digits := rest[1:]
		n := 0
		for n < len(digits) && digits[n] >= '0' && digits[n] <= '9' {
			n++
		}
		line, _ := strconv.Atoi(digits[:n])
		return line, true
	}
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// conclude asks the model what it found, with no tools to call, after a round
// budget spent entirely on looking. Its sentence is the pointer the answer
// prompt carries.
//
// Never fails the turn: a conclusion that does not arrive costs the answer its
// pointer and nothing else, and the landings are already in the sources.
func (g *Gatherer) conclude(ctx context.Context, msgs []llm.ToolMessage) string {
	msgs = append(msgs, llm.ToolMessage{Role: "user", Content: locateConclude})
	turn, err := g.locate.CallTools(ctx, msgs, nil,
		llm.ShortGate(), llm.WithoutThinking(), llm.WithGateTemperature(),
		llm.WithMaxTokens(locateMaxTokens), llm.WithStep("locate"))
	if err != nil {
		g.logger().Warn("locate conclusion failed; the landings are kept", "err", err)
		return ""
	}
	return strings.TrimSpace(turn.Content)
}

const locateConclude = `Stop looking. In one or two sentences, from what you have read, start with
"FOUND: <repository> <path>:LINE", written as the tools showed them, followed by
the exact code on that line. If you did not find it, start with "NOT FOUND:"
and say so plainly instead of guessing.`

// runLocateTool executes one call and returns a label for the trace, what it
// landed, and — for a call refused before any lookup — what the model is told
// instead of "nothing found". The message comes from here, where the reason is
// known, never re-derived by the caller from the label. An argument the model
// got wrong is not an error: it is a call that found nothing, and the model is
// told why.
func (g *Gatherer) runLocateTool(ctx context.Context, call llm.ToolCall, sources []Source, known []string, stage retrieve.StagePrefixes, shown shownFiles) (step LocateStep, refused, display string, landings []Source, err error) {
	var args struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
		Name    string `json:"name"`
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Line    int    `json:"line"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return LocateStep{Tool: call.Name}.refusedAs(call.Name+"(unparseable)", "the call was malformed"), locateUnparseable, "", nil, nil //nolint:nilerr // malformed arguments are the model's mistake, told back to it, never a failed turn
	}
	step = callStep(call)

	// The restriction, narrowed by what the model asked for and never widened
	// past it. A name outside the ceiling is refused out loud: silently
	// searching the whole corpus instead is the quiet widening the funnel
	// rule exists to stop, and silently searching nothing would report a
	// wrong "not in the index".
	repos := known
	if args.Repo != "" {
		if len(known) > 0 && !slices.Contains(known, args.Repo) {
			return step.refusedAs(call.Name+"("+args.Repo+": outside this turn's repositories)", "that repository is outside this turn"),
				"That repository is outside this turn. Search the ones already in front of you.", "", nil, nil
		}
		// An UNRESTRICTED turn has no ceiling to check the name against, and
		// a name the index does not carry is dropped downstream by
		// knownRepos — which reads as "no restriction", so the model asked to
		// narrow to one repository and would silently get the whole corpus.
		// Said out loud instead, the way a named repository the index lacks
		// is said out loud everywhere else.
		if len(known) == 0 && !g.indexed(ctx, args.Repo) {
			return step.refusedAs(call.Name+"("+args.Repo+": not in the index)", "that repository is not in the index"),
				"That repository is not in the index, so nothing was looked up. The pattern may be right: call again without repo, or with a repository already among the sources.", "", nil, nil
		}
		repos = []string{args.Repo}
	}

	switch call.Name {
	case "search":
		if args.Query == "" || g.locateSearch == nil {
			return step.refusedAs("search()", "it named nothing to look up"), "The query was empty, so nothing was looked up.", "", nil, nil
		}
		// Question empty, for the reason grep's is: knownRepos UNIONS names
		// mentioned in the question into the restriction, so a query reading
		// "where does loom set anzahlHaustiere" would add loom to a turn pinned
		// away from it — the funnel widening, from a sentence the model wrote.
		hits, err := g.locateSearch.Search(ctx, retrieve.Query{
			Text: args.Query, Repos: repos, K: locateMaxLandings, Stage: stage})
		if err != nil {
			// A failed lookup is nothing found, not a failed turn.
			g.logger().Warn("locate search failed", "query", args.Query, "err", err)
			return step.as("search(" + args.Query + ")"), "", "", nil, nil
		}
		return step.as("search(" + args.Query + ")"), "", "", hitSources(hits, "locate:search "+args.Query), nil

	case "grep":
		if args.Pattern == "" || g.locateSearch == nil {
			return step.refusedAs("grep()", "it named nothing to look up"), "The pattern was empty, so nothing was looked up.", "", nil, nil
		}
		// Substring, never SeedHits. The scan is what sees setAnzahlhaustiere
		// inside a token FTS5 indexed whole, which is the case the ranked
		// lanes cannot serve and the reason this tool exists — but SeedHits
		// derives accessor spellings from what it is given, drops terms under
		// a length floor and skips one occurring in more files than SeedFiles.
		// Handed a literal the model already worked out, all three are a
		// silent "nothing found" for a spelling that was right, and the
		// prompt then tells the model to change it. That is the failure this
		// loop exists to fix, and it must not be reintroduced inside it.
		// The question argument is empty, not the pattern: knownRepos reads it
		// to spot a repository NAMED in the question, and a pattern that
		// happens to contain one would silently narrow the scan to it.
		hits, err := g.locateSearch.Substring(ctx, args.Pattern, locateGrepChunks, repos, "", stage)
		if err != nil {
			g.logger().Warn("locate grep failed", "pattern", args.Pattern, "err", err)
			return step.as("grep(" + args.Pattern + ")"), "", "", nil, nil
		}
		// Admits nothing by itself. It shows every matching line, the way a
		// terminal grep does, and the model decides which one answers: the
		// place its conclusion names is what gets admitted (admitFound).
		// Admitting the first few chunks instead admitted them in ADDRESS
		// order, which is no order at all for this question.
		listing, n := grepListing(hits, args.Pattern, sources, shown, len(hits) >= locateGrepChunks)
		if n == 0 {
			return step.as("grep(" + args.Pattern + ")"), "", "", nil, nil
		}
		step.Matches = n
		return step.as(fmt.Sprintf("grep(%s) %d lines", args.Pattern, n)), "", listing, nil, nil

	case "symbol":
		if args.Name == "" {
			return step.refusedAs("symbol()", "it named nothing to look up"), "The name was empty, so nothing was looked up.", "", nil, nil
		}
		// The gathered sources, not nil: atHome is what puts the repositories
		// the turn is already reading first, so a name sibling products
		// define byte for byte lands at home. The stage and repository
		// filters go INTO the lookup, before atHome picks that home: applied
		// after, a home defining the name only outside the stage would be
		// dropped whole and the tool would report "nothing found" for a name
		// another repository in scope defines.
		landings, err = g.symbolLandings(ctx, args.Name, sources, func(s Source) bool {
			return inStage(s.Repo, s.Path, stage) && (len(repos) == 0 || slices.Contains(repos, s.Repo))
		})
		if err != nil {
			return LocateStep{}, "", "", nil, err
		}
		landings = capLandings(landings, locateMaxLandings)
		return step.as("symbol(" + args.Name + ")"), "", "", reasoned(landings, "locate:symbol "+args.Name), nil

	case "read":
		if args.Repo == "" || args.Path == "" {
			return step.refusedAs("read()", "it named no file"), "read needs both repo and path, so nothing was looked up.", "", nil, nil
		}
		step.label = fmt.Sprintf("read(%s:%d)", args.Path, args.Line)
		s, ok, err := g.chunkAt(ctx, args.Repo, args.Path, args.Line)
		if err != nil {
			return LocateStep{}, "", "", nil, err
		}
		landed := within([]Source{s}, stage)
		if !ok || len(landed) == 0 {
			return step, "", "", nil, nil
		}
		// The FILE around the line, not the one chunk: a chunk boundary can
		// cut the method in half, and the model is deciding what the code
		// there does.
		from, to := max(1, args.Line-locateReadBefore), args.Line+locateReadAfter
		text, err := g.fileLines(ctx, args.Repo, args.Path, from, to)
		if err != nil {
			return LocateStep{}, "", "", nil, err
		}
		shown[repoPath{args.Repo, args.Path}] = true
		display := fmt.Sprintf("%s %s:%d-%d\n%s", args.Repo, args.Path, from, to, text)
		return step, "", display, reasoned(landed, "locate:read "+args.Path), nil
	}
	return step.refusedAs(call.Name+"(unknown tool)", "there is no tool by that name"),
		"There is no tool by that name. The tools are search, grep, symbol and read.", "", nil, nil
}

// callStep is the step a call asks for, before it runs. Arguments that do not
// parse leave the tool name alone.
func callStep(call llm.ToolCall) LocateStep {
	var args struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
		Name    string `json:"name"`
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Line    int    `json:"line"`
	}
	step := LocateStep{Tool: call.Name}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return step
	}
	step.Arg, step.Repo = cmp.Or(args.Query, args.Pattern, args.Name), args.Repo
	if call.Name == "read" {
		step.Path, step.Line = args.Path, args.Line
	}
	return step
}

// as names the step for the log.
func (s LocateStep) as(label string) LocateStep {
	s.label = label
	return s
}

// refusedAs is a step refused before any lookup, with why in the reader's
// words.
func (s LocateStep) refusedAs(label, why string) LocateStep {
	s.label, s.NotRun = label, why
	return s
}

const locateUnparseable = "The arguments were not valid JSON, so nothing was looked up. Send the call again with well-formed arguments."

// hitSources turns what a tool found into sources the answer can cite.
//
// The reason says how the chunk arrived — "locate:grep setAnzahlhaustiere" — and
// reachedVia renders it for the answer prompt.
//
// For grep, symbol and read the argument is an identifier or a path, so the
// reason carries the index's own spelling the way the gap pass's does. The
// search tool is the exception: its argument is a phrase the model wrote, and
// that phrase reaches the prompt. It is a QUERY, not a claim about code, so it
// stays outside the rule against storing model text about code — but it is
// model text, and saying otherwise here would be false.
func hitSources(hits []retrieve.Hit, reason string) []Source {
	out := make([]Source, 0, len(hits))
	for _, h := range hits {
		out = append(out, Source{
			ChunkID: h.ChunkID, Repo: h.Repo, Branch: h.Branch, Path: h.Path,
			Symbol: h.Symbol, StartLine: h.StartLine, EndLine: h.EndLine,
			SHA: h.SHA, Text: h.RawText, Reason: reason,
		})
	}
	return out
}

// reasoned stamps how a landing arrived. symbolLandings and chunkAt set
// reasons of their own for the walk and the gap pass; a loop landing has to
// say it was the loop, or the answer prompt reports it as something it was not.
func reasoned(ss []Source, reason string) []Source {
	for i := range ss {
		ss[i].Reason = reason
	}
	return ss
}

// turnCeiling is the set of repositories a turn may gather from: the walk,
// the crossings, the gap pass and the loop all run under it.
//
// The PROJECTS of what the question named, or else of what its search hit,
// with the libraries those projects declare in uses. Once the project is
// known the turn does not leave it, except into a library it is built on: a
// walk or a crossing reaching a second product would admit and cite it, the
// cross-repo answer the repository card exists to stop. A named member opens
// its own project for the walk, because the walk follows what the code
// references; the search itself stays narrowed to the member.
//
// No ceiling (nil) only when nothing was named or hit. A project map that
// knows none of the repositories (project data unavailable) confines the turn
// to what was named or hit: narrower than the project, never the whole
// corpus.
func turnCeiling(known, hits []string, pm projects.Map) []string {
	seeds := known
	if len(seeds) == 0 {
		seeds = hits
	}
	set := map[string]bool{}
	for _, r := range seeds {
		// Members carries a library only through a declared uses edge, and a
		// library's own project of one lists only itself.
		for _, m := range pm.Members(pm.Of(r)) {
			set[m] = true
		}
	}
	for _, r := range seeds {
		set[r] = true
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// indexed reports whether the corpus carries a repository under that name and
// has it enabled. A parked one is not a place to look: parking stops new
// answers, and the read tool's own query says the same with enabled = 1.
func (g *Gatherer) indexed(ctx context.Context, repo string) bool {
	var n int
	err := g.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repo_state WHERE name = ? AND enabled = 1`, repo).Scan(&n)
	return err == nil && n > 0
}

// capLandings trims landings to at most n, so one broad call cannot spend the
// reserve.
func capLandings(ss []Source, n int) []Source {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

// locateResult is what a search, symbol or read call reports back to the
// model when it has no display of its own: every match with a clipped
// excerpt, the ones the turn already held marked as such. Showing "nothing
// new" for a match already gathered hid the one line the model needed on the
// questions the loop exists for, where the answering chunk is gathered long
// before it runs.
func locateResult(landed []Source, gathered []bool) string {
	if len(landed) == 0 {
		return "Nothing found."
	}
	var b strings.Builder
	for i, s := range landed {
		fmt.Fprintf(&b, "%s %s:%d-%d", s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", s.Symbol)
		}
		if i < len(gathered) && gathered[i] {
			b.WriteString(" [already among the sources]")
		}
		b.WriteByte('\n')
		b.WriteString(clipRunes(s.Text, locateExcerpt))
		b.WriteString("\n\n")
	}
	return b.String()
}

// grepListing renders every line of hits that contains pattern, grouped by
// file and deduplicated across overlapping chunk windows, in the shape a
// terminal grep prints: a count, then each file, then "Line N: code". Files
// the turn already holds are marked. At most locateGrepLines lines, with a
// note when more exist. It records every listed file in shown.
func grepListing(hits []retrieve.Hit, pattern string, sources []Source, shown shownFiles, capped bool) (string, int) {
	held := map[string]bool{}
	for _, s := range sources {
		held[s.Repo+"\x00"+s.Path] = true
	}
	needle := strings.ToLower(pattern)
	type file struct {
		repo, path string
		lines      map[int]string
	}
	var files []*file
	byKey := map[string]*file{}
	// The store stops at locateGrepChunks without saying so; a listing
	// that reached it may be missing matches past the cut.
	total, more := 0, capped
	for _, h := range hits {
		for i, line := range strings.Split(h.RawText, "\n") {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			key := h.Repo + "\x00" + h.Path
			f := byKey[key]
			if f == nil {
				f = &file{repo: h.Repo, path: h.Path, lines: map[int]string{}}
				byKey[key] = f
				files = append(files, f)
			}
			n := h.StartLine + i
			if _, dup := f.lines[n]; dup {
				continue
			}
			if total == locateGrepLines {
				more = true
				continue
			}
			f.lines[n] = line
			total++
		}
	}
	if total == 0 {
		return "", 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching lines", total)
	if more {
		b.WriteString(" (more matches available; narrow the pattern)")
	}
	b.WriteString(":\n")
	for _, f := range files {
		if len(f.lines) == 0 {
			continue
		}
		shown[repoPath{f.repo, f.path}] = true
		fmt.Fprintf(&b, "%s %s", f.repo, f.path)
		if held[f.repo+"\x00"+f.path] {
			b.WriteString(" [among the sources]")
		}
		b.WriteByte('\n')
		nums := make([]int, 0, len(f.lines))
		for n := range f.lines {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			fmt.Fprintf(&b, "  Line %d: %s\n", n, clipRunes(strings.TrimSpace(f.lines[n]), locateLineMax))
		}
	}
	return b.String(), total
}

// fileLines renders lines from..to of an indexed file, each as "N: code",
// assembled from every chunk overlapping the range. Windows overlap, so a
// line seen twice is written once.
func (g *Gatherer) fileLines(ctx context.Context, repo, path string, from, to int) (string, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT c.start_line, c.raw_text
		FROM files f
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND f.path = ? AND c.end_line >= ? AND c.start_line <= ?
		ORDER BY c.start_line`, repo, path, from, to)
	if err != nil {
		return "", fmt.Errorf("read %s:%d-%d: %w", path, from, to, err)
	}
	defer func() { _ = rows.Close() }()
	lines := map[int]string{}
	for rows.Next() {
		var start int
		var text string
		if err := rows.Scan(&start, &text); err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		for i, line := range strings.Split(text, "\n") {
			if n := start + i; n >= from && n <= to {
				if _, seen := lines[n]; !seen {
					lines[n] = line
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	nums := make([]int, 0, len(lines))
	for n := range lines {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var b strings.Builder
	for _, n := range nums {
		fmt.Fprintf(&b, "%d: %s\n", n, clipRunes(lines[n], locateLineMax))
	}
	return b.String(), nil
}

// finishLocate admits the place the conclusion names, reports it, and puts it
// first. Every way out of the loop that carries a conclusion ends here.
func (g *Gatherer) finishLocate(ctx context.Context, a *admitter, report *LocateReport, shown shownFiles, stage retrieve.StagePrefixes) []Source {
	if label := g.admitFound(ctx, a, report.Found, shown, stage); label != "" {
		report.Landed = append(report.Landed, label)
	}
	found := strings.TrimSpace(report.Found)
	switch best, place := pointedAt(a.out, found); {
	case best >= 0:
		report.Outcome, report.Place = LocatePointed, &place
	case strings.HasPrefix(found, "NOT FOUND:"):
		report.Outcome = LocateNotFound
	case strings.HasPrefix(found, "FOUND:"):
		report.Outcome = LocateUnpinned
	}
	return pointedFirst(a.out, report.Found)
}

// admitFound fetches and admits the place a FOUND conclusion names when no
// call admitted it: grep shows lines and admits nothing, so the chunk the
// model chose is usually not among the sources yet. It is resolved against
// the files the model was actually shown (resolvePlace), never guessed, and
// admitted under the loop's reserve like any landing. The label it returns
// names the place for the trace; empty when nothing was admitted.
func (g *Gatherer) admitFound(ctx context.Context, a *admitter, found string, shown shownFiles, stage retrieve.StagePrefixes) string {
	claim, ok := strings.CutPrefix(strings.TrimSpace(found), "FOUND:")
	if !ok {
		return ""
	}
	files := make([]repoPath, 0, len(shown))
	for f := range shown {
		files = append(files, f)
	}
	f, line, ok := resolvePlace(claim, files)
	if !ok {
		return ""
	}
	for _, s := range a.out {
		if s.Repo == f.repo && s.Path == f.path && line >= s.StartLine && line <= s.EndLine {
			return ""
		}
	}
	s, ok, err := g.chunkAt(ctx, f.repo, f.path, line)
	if err != nil {
		g.logger().Warn("locate could not fetch the place it found", "path", f.path, "err", err)
		return ""
	}
	if !ok || len(within([]Source{s}, stage)) == 0 {
		return ""
	}
	s.Reason = "locate:found"
	if !a.take(s, g.opts.MaxHops+2) {
		g.logger().Warn("locate found a place the budget cannot hold", "path", f.path, "line", line)
		return ""
	}
	return fmt.Sprintf("found(%s:%d)", f.path, line)
}

// repoPath is one file of one repository: the same path in two repositories
// is two files.
type repoPath struct{ repo, path string }

// shownFiles is every file the model was shown during the loop.
type shownFiles map[repoPath]bool

// resolvePlace finds the one file a conclusion names, with its line.
//
// The full path wins over the file name, and a repository named in the
// conclusion narrows the candidates. More than one candidate left is no
// answer: citing a same-named file from another directory or repository as
// "the place" is worse than citing none.
func resolvePlace(claim string, files []repoPath) (repoPath, int, bool) {
	type hit struct {
		f    repoPath
		line int
	}
	var full, base []hit
	for _, f := range files {
		if line, named := namedAt(claim, f.path); named && line > 0 {
			full = append(full, hit{f, line})
		} else if line, named := namedAt(claim, path.Base(f.path)); named && line > 0 {
			base = append(base, hit{f, line})
		}
	}
	cands := full
	if len(cands) == 0 {
		cands = base
	}
	if len(cands) > 1 {
		var inRepo []hit
		for _, c := range cands {
			if _, named := namedAt(claim, c.f.repo); named {
				inRepo = append(inRepo, c)
			}
		}
		cands = inRepo
	}
	if len(cands) != 1 {
		return repoPath{}, 0, false
	}
	return cands[0].f, cands[0].line, true
}

// locateGrepChunks is how many matching chunks one grep reads, and
// locateGrepLines how many matching lines it shows: opencode's grep lists up
// to 100 and says when more exist. locateLineMax clips each line, and
// locateReadBefore and locateReadAfter are the window read shows around its
// line.
const (
	locateGrepChunks = 80
	locateGrepLines  = 100
	locateLineMax    = 240
	locateReadBefore = 20
	locateReadAfter  = 40
)

// locateExcerpt is how much of a landed chunk the model reads per call.
const locateExcerpt = 1200

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…"
}

// locatePrompt is the question and the addresses already gathered. Addresses
// only: the model decides whether the place it needs is among them, and the
// source TEXT is what the answer call reads, not this one.
func locatePrompt(question string, sources []Source) string {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteString("\n\nAlready gathered:\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "%s %s:%d-%d", s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", s.Symbol)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
