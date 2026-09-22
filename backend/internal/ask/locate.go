package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
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
// On in the product at one round (BACKEND_LOCATE_ROUNDS); WithLocateLoop(nil)
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
call nothing and reply with the file, the line and the exact code that does
it — one or two sentences, no more. Something else writes the answer the
reader sees; your sentence is what tells it which of many sources is the one
that matters, so name the place precisely or say plainly that it is not in
the index.

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
}

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

// locating reports whether the loop is wired: a client was given.
func (g *Gatherer) locating() bool { return g.locate != nil }

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
	if g.locateRounds > 0 {
		return g.locateRounds
	}
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
	a := &admitter{seen: map[int64]bool{}, budget: g.opts.TokenBudget}
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

	for round := 0; round < g.rounds(); round++ {
		// Counted BEFORE the call: a round that failed was still paid for,
		// and a trace reporting fewer rounds than the meter was billed for
		// is a record disagreeing with the bill.
		report.Rounds = round + 1
		turn, err := g.locate.CallTools(ctx, msgs, locateTools(),
			llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature),
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
			return pointedFirst(a.out, report.Found), report, nil
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

		if len(turn.Calls) > locateMaxCalls {
			for _, over := range turn.Calls[locateMaxCalls:] {
				report.Refused = append(report.Refused, over.Name+"(over the call limit)")
			}
			turn.Calls = turn.Calls[:locateMaxCalls]
		}
		msgs = append(msgs, llm.AssistantCalls(turn.Content, turn.Calls))
		stop := false
		// Which call of this turn the reserve ran out on: the calls after it
		// are refused, and the count has to index the same list they do.
		refusedFrom := 0
		for ci, call := range turn.Calls {
			label, refused, landings, err := g.runLocateTool(ctx, call, a.out, known, stage)
			if err != nil {
				return sources, LocateReport{}, err
			}
			report.Calls = append(report.Calls, label)
			if len(landings) == 0 {
				report.Empty = append(report.Empty, label)
				// What the model is told has to match what happened. A call
				// refused for its repository, its arguments or its tool was
				// not a spelling that missed, and "try another spelling"
				// would send it away from a pattern that was right.
				result := refused
				if result == "" {
					result = "Nothing found. Try a different spelling or another tool."
				}
				msgs = append(msgs, llm.ToolResult(call.ID, result))
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
			// What the model reads is every match, gathered or not: for grep
			// the matching lines with their numbers, as a terminal grep shows
			// them; for the other tools a clipped excerpt. Reading is what
			// lets it tell the line that answers from a file that merely
			// contains the name.
			msgs = append(msgs, llm.ToolResult(call.ID, locateResult(landings, gathered, grepPattern(call))))
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
			}
			break
		}
	}
	return pointedFirst(a.out, report.Found), report, nil
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
// line that answers and decided it was the answer. The loop did. A chunk
// holding the named line wins over another chunk of the same file; a file
// named without a line takes its first chunk. A conclusion naming no source,
// or "not found", leaves the order alone.
func pointedFirst(sources []Source, found string) []Source {
	if found == "" {
		return sources
	}
	best := -1
	for i, s := range sources {
		line, ok := namedAt(found, path.Base(s.Path))
		if !ok {
			continue
		}
		if line >= s.StartLine && line <= s.EndLine {
			best = i
			break
		}
		if best < 0 {
			best = i
		}
	}
	if best <= 0 {
		return sources
	}
	out := make([]Source, 0, len(sources))
	out = append(out, sources[best])
	out = append(out, sources[:best]...)
	return append(out, sources[best+1:]...)
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
		llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature),
		llm.WithMaxTokens(locateMaxTokens), llm.WithStep("locate"))
	if err != nil {
		g.logger().Warn("locate conclusion failed; the landings are kept", "err", err)
		return ""
	}
	return strings.TrimSpace(turn.Content)
}

const locateConclude = `Stop looking. In one or two sentences, name the place that answers the
question, from what you have read: write it as FileName.ext:LINE, followed by
the exact code on that line. If you did not find it, say so plainly instead of
guessing.`

// runLocateTool executes one call and returns a label for the trace, what it
// landed, and — for a call refused before any lookup — what the model is told
// instead of "nothing found". The message comes from here, where the reason is
// known, never re-derived by the caller from the label. An argument the model
// got wrong is not an error: it is a call that found nothing, and the model is
// told why.
func (g *Gatherer) runLocateTool(ctx context.Context, call llm.ToolCall, sources []Source, known []string, stage retrieve.StagePrefixes) (label, refused string, landings []Source, err error) {
	var args struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
		Name    string `json:"name"`
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Line    int    `json:"line"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return call.Name + "(unparseable)", locateUnparseable, nil, nil //nolint:nilerr // malformed arguments are the model's mistake, told back to it, never a failed turn
	}

	// The restriction, narrowed by what the model asked for and never widened
	// past it. A name outside the ceiling is refused out loud: silently
	// searching the whole corpus instead is the quiet widening the funnel
	// rule exists to stop, and silently searching nothing would report a
	// wrong "not in the index".
	repos := known
	if args.Repo != "" {
		if len(known) > 0 && !contains(known, args.Repo) {
			return call.Name + "(" + args.Repo + ": outside this turn's repositories)",
				"That repository is outside this turn. Search the ones already in front of you.", nil, nil
		}
		// An UNRESTRICTED turn has no ceiling to check the name against, and
		// a name the index does not carry is dropped downstream by
		// knownRepos — which reads as "no restriction", so the model asked to
		// narrow to one repository and would silently get the whole corpus.
		// Said out loud instead, the way a named repository the index lacks
		// is said out loud everywhere else.
		if len(known) == 0 && !g.indexed(ctx, args.Repo) {
			return call.Name + "(" + args.Repo + ": not in the index)",
				"That repository is not in the index, so nothing was looked up. The pattern may be right: call again without repo, or with a repository already among the sources.", nil, nil
		}
		repos = []string{args.Repo}
	}

	switch call.Name {
	case "search":
		if args.Query == "" || g.locateSearch == nil {
			return "search()", "The query was empty, so nothing was looked up.", nil, nil
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
			return "search(" + args.Query + ")", "", nil, nil
		}
		return "search(" + args.Query + ")", "", hitSources(hits, "locate:search "+args.Query), nil

	case "grep":
		if args.Pattern == "" || g.locateSearch == nil {
			return "grep()", "The pattern was empty, so nothing was looked up.", nil, nil
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
		hits, err := g.locateSearch.Substring(ctx, args.Pattern, locateMaxLandings, repos, "", stage)
		if err != nil {
			g.logger().Warn("locate grep failed", "pattern", args.Pattern, "err", err)
			return "grep(" + args.Pattern + ")", "", nil, nil
		}
		return "grep(" + args.Pattern + ")", "", capLandings(hitSources(hits, "locate:grep "+args.Pattern), locateMaxLandings), nil

	case "symbol":
		if args.Name == "" {
			return "symbol()", "The name was empty, so nothing was looked up.", nil, nil
		}
		// The gathered sources, not nil: atHome is what puts the repositories
		// the turn is already reading first, so a name sibling products
		// define byte for byte lands at home. The stage and repository
		// filters go INTO the lookup, before atHome picks that home: applied
		// after, a home defining the name only outside the stage would be
		// dropped whole and the tool would report "nothing found" for a name
		// another repository in scope defines.
		landings, err = g.symbolLandings(ctx, args.Name, sources, func(s Source) bool {
			return inStage(s.Repo, s.Path, stage) && (len(repos) == 0 || contains(repos, s.Repo))
		})
		if err != nil {
			return "", "", nil, err
		}
		landings = capLandings(landings, locateMaxLandings)
		return "symbol(" + args.Name + ")", "", reasoned(landings, "locate:symbol "+args.Name), nil

	case "read":
		if args.Repo == "" || args.Path == "" {
			return "read()", "read needs both repo and path, so nothing was looked up.", nil, nil
		}
		label = fmt.Sprintf("read(%s:%d)", args.Path, args.Line)
		s, ok, err := g.chunkAt(ctx, args.Repo, args.Path, args.Line)
		if err != nil {
			return "", "", nil, err
		}
		if !ok {
			return label, "", nil, nil
		}
		return label, "", reasoned(within([]Source{s}, stage), "locate:read "+args.Path), nil
	}
	return call.Name + "(unknown tool)",
		"There is no tool by that name. The tools are search, grep, symbol and read.", nil, nil
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

// locateCeiling is the set of repositories the loop may look in.
//
// A turn that already has a restriction keeps it: a named member or a pin
// narrows, and the loop never widens it to the rest of the project. A turn
// naming nothing has no restriction, which the lookups read as the whole
// corpus — but its search has landed by now, and the router answers such a
// turn without a card only because the landing sits in one place. So the
// PROJECTS the question's own hits (hop 0) belong to are the ceiling, with
// the libraries those projects declare in uses and nothing else: a grep
// matching in a second product would otherwise admit and cite it, the
// cross-repo answer the repository card exists to stop. The walk's later
// hops do not open a project; they are the walk's reasons, not the
// question's.
//
// No hop-0 source, or no project map, falls back to the repositories
// already gathered. Never empty for non-empty sources: empty means the whole
// corpus.
func locateCeiling(known []string, sources []Source, pm projects.Map) []string {
	if len(known) > 0 {
		return known
	}
	seed := hitRepos(sources)
	if len(seed) == 0 {
		seed = sourceRepos(sources)
	}
	set := map[string]bool{}
	for _, r := range seed {
		set[r] = true
		// Members carries a library only through a declared uses edge, and a
		// library's own project of one lists only itself.
		for _, m := range pm.Members(pm.Of(r)) {
			set[m] = true
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func sourceRepos(sources []Source) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sources {
		if s.Repo != "" && !seen[s.Repo] {
			seen[s.Repo] = true
			out = append(out, s.Repo)
		}
	}
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

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// capLandings trims landings to at most n, so one broad call cannot spend the
// reserve.
func capLandings(ss []Source, n int) []Source {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

// locateResult is what one call reports back to the model: every match,
// with the ones the turn already held marked as such.
//
// The code, not only the address, and for grep only the LINES that match.
// A model that never reads what it found cannot tell the line that answers
// the question from the file that merely contains it; a bare agent harness
// answering the same question from the same code reads grep's output, which
// is file, line number and line. Showing "nothing new" for a match already
// gathered hid exactly that line on the question the loop exists for, where
// the answering chunk is gathered long before the loop runs.
//
// Clipped: the loop is choosing where to look, not writing the answer, and a
// round that pasted six whole files would spend the reply cap on quotation.
func locateResult(landed []Source, gathered []bool, pattern string) string {
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
		if lines := matchingLines(s, pattern); lines != "" {
			b.WriteString(lines)
		} else {
			b.WriteString(clipRunes(s.Text, locateExcerpt))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// matchingLines renders the lines of s containing pattern, case-insensitive,
// each as "<line number>: <line>", the way grep prints them. Empty when there
// is no pattern or no line holds it whole, and the caller shows an excerpt.
func matchingLines(s Source, pattern string) string {
	if pattern == "" {
		return ""
	}
	needle := strings.ToLower(pattern)
	var b strings.Builder
	n := 0
	for i, line := range strings.Split(s.Text, "\n") {
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		fmt.Fprintf(&b, "%d: %s\n", s.StartLine+i, clipRunes(strings.TrimSpace(line), locateLineMax))
		if n++; n == locateLinesPerMatch {
			break
		}
	}
	return b.String()
}

// grepPattern is the pattern a grep call asked for, or empty for any other
// tool or arguments that do not parse.
func grepPattern(call llm.ToolCall) string {
	if call.Name != "grep" {
		return ""
	}
	var args struct {
		Pattern string `json:"pattern"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return ""
	}
	return args.Pattern
}

// locateLinesPerMatch and locateLineMax bound what one grep match shows: a
// handful of lines, each clipped, so a name repeated down a long chunk
// cannot fill the round.
const (
	locateLinesPerMatch = 8
	locateLineMax       = 240
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
