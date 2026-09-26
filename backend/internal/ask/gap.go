package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// The gap pass: after the symbol walk and the crossings, ONE short-gate call
// reads what was gathered and lists the names the mechanism depends on whose
// definition is not among the sources. Every name it returns is then resolved
// DETERMINISTICALLY, against the symbols table or the edge table, and the
// landing chunks are added to the sources.
//
// Why a model at all: the walk follows what the TEXT of a gathered chunk
// spells, and the two things it cannot see are a name spelled nowhere in the
// gathered text (a queue the answer needs but only the configuration names)
// and a name the selectivity ceiling refused. Reading the gathered code
// against the question is the judgement a developer makes before opening one
// more file, and it is the only step here that has it.
//
// Those two lookups and no third: a name the index knows as neither a symbol
// nor a token is Unresolved. Matching the word in raw text landed on chunks
// that merely mention it (docs/measurements/2026-09-16-gap-pass.md), and it
// was also the one path that had no index row to correct the spelling with.
//
// What it stores: the names are used to look code up and are gone with the
// turn. Every landing's reason carries the INDEX's spelling — symbols.name or
// integration_tokens.value — never the model's, which keeps the pass outside
// the rule against model-written text about code, the same footing as the
// routing judge and the reranker.
//
// What it may never do: fail a turn, or be worse than not running. A call
// that errors or a reply that cannot be read leaves the sources exactly as
// they arrived and says so in the log.

// gapMaxNames caps the reply. Eight names is what the reserve can hold.
const gapMaxNames = 8

// gapMaxTokens caps the reply's length: eight short objects.
const gapMaxTokens = 384

// gapPromptChars is the budget for the sources in the prompt, shared out
// between them. One short-gate call reads the whole gather, and a corpus that
// fans out hands it a hundred and fifty chunks.
const gapPromptChars = 60000

// gapExcerptMin is the floor under a source's share of that budget: the
// header plus the opening lines, which is where a signature and a doc comment
// sit.
const gapExcerptMin = 240

// gapMaxLandings caps the landings one name may contribute, one per file: a
// property key is set once per stage, and four stages is a report.
const gapMaxLandings = 4

// gapNameRunes is the longest name worth looking up. Past it the reply is a
// sentence rather than an identifier.
const gapNameRunes = 80

// GapName is one dependency the model says is missing: the name as the code
// spells it, and what kind of thing it is.
type GapName struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// GapReport is what the pass did, for the trace: what it asked for, what it
// found, what it could not resolve, what the budget refused, and why it did
// nothing at all.
type GapReport struct {
	// Asked is the reply after filtering, in the model's own order.
	Asked []GapName
	// Landed is the asked names that put a chunk in front of the answer that
	// was not there before. Unresolved is those no lookup resolved, this
	// turn's stage included. Refused is those whose landings the reserve
	// could not hold, the names after the refusal among them.
	//
	// A name that resolved onto chunks the sources already carry is in none
	// of the three: nothing was added, nothing was missing, nothing refused.
	Landed     []string
	Unresolved []string
	Refused    []string
	// Skipped is "" when the pass ran, else why it did not:
	// "off", "no sources", "call failed", "not json".
	Skipped string
}

// WithGapPass gives the gatherer the short-gate client the gap pass calls.
// Nil is off, which is what the product ships until the arm is measured.
func (g *Gatherer) WithGapPass(c *llm.Client) *Gatherer {
	g.gap = c
	return g
}

// logger is Log, or the default logger when none was set.
func (g *Gatherer) logger() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

const gapSystem = `You read the code gathered to answer a question about a codebase and list
what it depends on that is NOT among the sources. Answer with JSON ONLY:
{"missing":[{"name":"...","kind":"symbol"|"route"|"destination"|"property"}]}

A dependency is a call, a type, a constant, an HTTP route, a queue or topic
name, or a configuration key that the mechanism the question asks about
uses, whose definition, handler or value does not appear in any source
below. Look inside the code, not only at the headers: a field read in a
method body counts.

name  the identifier, path, queue name or key exactly as the code spells it
kind  symbol       a function, method, class, type, constant or field
      route        an HTTP path such as /paymentAuth
      destination  a queue, topic or exchange name
      property     a configuration key such as spring.rabbitmq.host

At most 8 entries, the ones the answer would need first. Leave out anything
defined in a source below, language built-ins, and standard library or
framework names. An empty list is a correct answer. Do not explain.`

// FillGaps runs the pass and returns the sources it was given, plus whatever
// landed, and a report of what happened.
//
// It returns an error only for a cancelled context or a database failure. A
// model failure of any kind — the call, the reply, the shape of it — is a
// warning in the log, the sources unchanged, and a reason in the report: the
// walk answered every question before this pass existed.
func (g *Gatherer) FillGaps(ctx context.Context, question string, sources []Source, stage retrieve.StagePrefixes) ([]Source, GapReport, error) {
	if g.gap == nil {
		return sources, GapReport{Skipped: "off"}, nil
	}
	if len(sources) == 0 {
		return sources, GapReport{Skipped: "no sources"}, nil
	}

	out, _, err := g.gap.Complete(ctx, []llm.Message{
		{Role: "system", Content: gapSystem},
		{Role: "user", Content: gapPrompt(question, sources)},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithGateTemperature(),
		llm.WithMaxAnswerTokens(gapMaxTokens), llm.WithStep("gap"))
	if err != nil {
		if ctx.Err() != nil {
			// The reader left; there is no turn to fill a gap for.
			return sources, GapReport{}, ctx.Err()
		}
		g.logger().Warn("gap call failed; the gathered sources kept", "err", err)
		return sources, GapReport{Skipped: "call failed"}, nil
	}
	var reply struct {
		Missing []GapName `json:"missing"`
	}
	body, _ := llmwire.JSONObject(out)
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		g.logger().Warn("gap reply was not JSON; the gathered sources kept", "reply", llmwire.Truncate(out, 120))
		return sources, GapReport{Skipped: "not json"}, nil //nolint:nilerr // a failed or unparseable gap call keeps the gathered sources and reports Skipped; it must not fail the turn (logged above)
	}

	report := GapReport{Asked: gapNames(reply.Missing, sources)}
	if len(report.Asked) == 0 {
		return sources, report, nil
	}

	// The full budget, with what the sources already cost recomputed rather
	// than carried: FillGaps is called with what the walk returned, and the
	// reserve exists precisely so there is room left under that ceiling.
	budget := g.opts.TokenBudget
	// The locate loop runs AFTER this pass and has a reserve of its own, so
	// the gap pass leaves it alone the way the walk and the crossing leave
	// the gap reserve alone. Without this the loop is paid for in model calls
	// and then admits nothing, which is the failure both earlier reserves
	// were written to prevent.
	if g.locate != nil {
		budget -= g.opts.TokenBudget / locateReserve
	}
	a := &admitter{seen: map[int64]bool{}, budget: budget, allowed: g.allowed()}
	for _, s := range sources {
		a.seen[s.ChunkID] = true
		a.spent += estimateTokens(s.Text)
	}
	a.out = append(a.out, sources...)

	// The stage and the turn's ceiling go INTO the lookups, before the
	// per-name cap: applied after, four out-of-scope definitions fill the cap
	// and the one in scope is dropped, with the name reported neither landed,
	// unresolved nor refused.
	keep := func(s Source) bool { return inStage(s.Repo, s.Path, stage) && a.permits(s) }
	for i, n := range report.Asked {
		landings, err := g.resolve(ctx, n, sources, keep)
		if err != nil {
			return sources, GapReport{}, err
		}
		// A name only another stage or repository holds is one this turn
		// could not resolve, and dropping it silently would leave the
		// report short a name.
		if len(landings) == 0 {
			report.Unresolved = append(report.Unresolved, n.Name)
			continue
		}
		before := len(a.out)
		noRoom := false
		for _, l := range landings {
			if !a.take(l, g.opts.MaxHops+1) {
				noRoom = true
				break
			}
		}
		if noRoom {
			// The reserve is spent. A name half admitted is not a landing —
			// the answer reads a definition's places together — and every
			// name after it is refused without a lookup, because the
			// admitter has already said what it cannot hold and a later
			// small one would be admitted out of the model's own order.
			for _, rest := range report.Asked[i:] {
				report.Refused = append(report.Refused, rest.Name)
			}
			break
		}
		// Landed means something NEW was admitted: a name resolved onto
		// chunks the model was already reading cost the pass a lookup and
		// gained the answer nothing.
		if len(a.out) > before {
			report.Landed = append(report.Landed, n.Name)
		}
	}
	return a.out, report, nil
}

// resolve turns one name into landings, deterministically: the kind says
// which index holds it, and a name that index does not know is reported,
// never guessed at.
func (g *Gatherer) resolve(ctx context.Context, n GapName, sources []Source, keep func(Source) bool) ([]Source, error) {
	switch edges.Kind(n.Kind) {
	case edges.KindRoute, edges.KindDestination, edges.KindProperty:
		return g.tokenLandings(ctx, edges.Kind(n.Kind), n.Name, keep)
	default:
		return g.symbolLandings(ctx, n.Name, sources, keep)
	}
}

// symbolLandings finds where a name is DEFINED, under the same selectivity
// ceiling the walk follows, and prefers a repository the answer is already
// being written about: sibling products define the same identifiers byte for
// byte, and a lookup by name alone would cite whichever path sorts first.
//
// One chunk per file and at most gapMaxLandings files, the way the token
// landings are capped, with tests last: an overload lands twice in one file,
// and a name four files define would otherwise spend the whole reserve on one
// entry of the reply.
//
// keep, when set, drops definers BEFORE the home repository is chosen: atHome
// keeps one repository, and a filter applied after it would drop that
// repository's out-of-scope definitions and report nothing for a name another
// repository defines in scope.
func (g *Gatherer) symbolLandings(ctx context.Context, name string, sources []Source, keep func(Source) bool) ([]Source, error) {
	// No home and no near side: the caller has a name and no file, so every
	// selective definer over the enabled repositories is a candidate.
	rows, err := g.definers(ctx, []string{name}, "", "", "")
	if err != nil {
		return nil, err
	}
	if keep != nil {
		kept := rows[:0]
		for _, r := range rows {
			if keep(r) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if len(rows) == 0 {
		return nil, nil
	}
	var out []Source
	files := map[string]bool{}
	for _, s := range mechanismFirst(atHome(rows, sources)) {
		key := s.Repo + "\x00" + s.Path
		if files[key] || len(out) >= gapMaxLandings {
			continue
		}
		files[key] = true
		// The index's own spelling, from symbols.name, not the model's.
		s.Reason = "gap:" + strings.TrimPrefix(s.Reason, "reference:")
		out = append(out, s)
	}
	return out, nil
}

// atHome keeps the definers of the FIRST gathered repository that defines the
// name, in the order the repositories appear in the sources, or every definer
// when no gathered repository defines it — which is genuine composition and
// still travels.
func atHome(rows []Source, sources []Source) []Source {
	defines := map[string]bool{}
	for _, r := range rows {
		defines[r.Repo] = true
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if seen[s.Repo] {
			continue
		}
		seen[s.Repo] = true
		if !defines[s.Repo] {
			continue
		}
		var out []Source
		for _, r := range rows {
			if r.Repo == s.Repo {
				out = append(out, r)
			}
		}
		return out
	}
	return rows
}

// tokenLandings finds the chunk at every line carrying the token, one per
// file and at most gapMaxLandings of them.
//
// The stage restriction is applied BEFORE the cap: a property key is set once
// per stage, and counting the stages the turn did not ask about towards the
// cap spends it on landings that are then filtered away — a turn narrowed to
// production would lose the one file it was allowed to read.
func (g *Gatherer) tokenLandings(ctx context.Context, kind edges.Kind, name string, keep func(Source) bool) ([]Source, error) {
	var out []Source
	files := map[string]bool{}
	for _, v := range gapValues(kind, name) {
		ns, err := edges.Holders(ctx, g.db, kind, v)
		if err != nil {
			return nil, fmt.Errorf("look up the holders of %s %q: %w", kind, v, err)
		}
		for _, h := range ns {
			if keep != nil && !keep(Source{Repo: h.Repo, Path: h.Path}) {
				continue
			}
			key := h.Repo + "\x00" + h.Path
			if files[key] || len(out) >= gapMaxLandings {
				continue
			}
			s, ok, err := g.chunkAt(ctx, h.Repo, h.Path, h.Line)
			if err != nil {
				return nil, fmt.Errorf("read the landing of %s %q: %w", kind, v, err)
			}
			if !ok {
				continue
			}
			files[key] = true
			// integration_tokens.value, not the model's spelling.
			s.Reason = "gap:" + h.Value
			out = append(out, s)
		}
		if len(out) > 0 {
			break
		}
	}
	return out, nil
}

// gapNames filters the reply before anything is looked up: nothing empty,
// nothing sentence-shaped, nothing twice, and nothing the sources already
// carry. Filtered first and capped afterwards, so junk does not eat the
// eight slots; an unknown kind is read as a symbol, which is the lookup that
// can fail harmlessly.
func gapNames(got []GapName, sources []Source) []GapName {
	seen := map[string]bool{}
	var out []GapName
	for _, n := range got {
		n.Name = strings.TrimSpace(n.Name)
		switch edges.Kind(n.Kind) {
		case edges.KindRoute, edges.KindDestination, edges.KindProperty:
		default:
			n.Kind = "symbol"
		}
		// Keyed by kind AND name: "orders" the queue and "orders" the method
		// are two lookups in two indexes, and one is not the other's
		// duplicate.
		key := n.Kind + " " + n.Name
		if n.Name == "" || len([]rune(n.Name)) > gapNameRunes || seen[key] {
			continue
		}
		if strings.IndexFunc(n.Name, unicode.IsSpace) >= 0 {
			continue
		}
		seen[key] = true
		if amongSources(n, sources) {
			continue
		}
		out = append(out, n)
		if len(out) == gapMaxNames {
			break
		}
	}
	return out
}

// amongSources reports a name whose definition, handler or value is already
// in front of the model: a source defining it, a symbol hop that named it, or
// a crossing that followed it.
//
// Each check answers for its own kind. A source's symbol and a symbol hop's
// reason are answers about a DEFINITION, and a file defining a method called
// "orders" says nothing about where the queue "orders" is served. The
// crossing is compared by kind and value, never as a substring of the reason:
// a crossing on "/orders/123/items" is not the route "/orders", and reading
// it as one drops the name the model asked for.
func amongSources(n GapName, sources []Source) bool {
	// gapNames has already read an unknown kind as a symbol, so this is the
	// whole of the symbol case.
	symbol := n.Kind == "symbol"
	values := gapValues(edges.Kind(n.Kind), n.Name)
	for _, s := range sources {
		if symbol && (s.Symbol == n.Name || s.Reason == "reference:"+n.Name) {
			return true
		}
		kind, value, _, ok := edgeVia(s.Reason)
		if !ok || kind != n.Kind {
			continue
		}
		for _, v := range values {
			if value == v {
				return true
			}
		}
	}
	return false
}

// gapValues is the spellings of one name worth matching: a route the model
// spelled without the leading slash every indexed route carries is tried both
// ways, whether the name is being looked up or compared.
func gapValues(kind edges.Kind, name string) []string {
	if kind == edges.KindRoute && !strings.HasPrefix(name, "/") {
		return []string{name, "/" + name}
	}
	return []string{name}
}

// gapPrompt renders the question and the sources in renderSources' shape, so
// the model reads the same headers the answer call does. Each source's share
// of the prompt shrinks as there are more of them, never below the header
// and the opening lines.
func gapPrompt(question string, sources []Source) string {
	share := gapPromptChars / len(sources)
	if share < gapExcerptMin {
		share = gapExcerptMin
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nSources:\n", question)
	for i, s := range sources {
		fmt.Fprintf(&b, "\n[%d] %s %s:%d-%d", i+1, s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", s.Symbol)
		}
		b.WriteString("\n")
		b.WriteString(retrieve.Excerpt(s.Text, share))
		b.WriteString("\n")
	}
	return b.String()
}
