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
// DETERMINISTICALLY — the symbols table, the edge table, and the keyword lane
// as a last resort — and the landing chunks are added to the sources.
//
// Why a model at all: the walk follows what the TEXT of a gathered chunk
// spells, and the two things it cannot see are a name spelled nowhere in the
// gathered text (a queue the answer needs but only the configuration names)
// and a name the selectivity ceiling refused. Reading the gathered code
// against the question is the judgement a developer makes before opening one
// more file, and it is the only step here that has it.
//
// What it stores: the names are used to look code up and are gone with the
// turn, which keeps the pass outside the rule against model-written text
// about code — the same footing as the routing judge and the reranker. The
// one exception is the keyword fallback: no index row was matched to correct
// the spelling, so its landing carries the MODEL's string in the source's
// reason, and the turn writes that to message_sources.reason. It is a trace
// line — never indexed, never embedded, never read back as code — and it is
// why that lookup is the narrowest of the three.
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
// sit. Same reasoning, and same value, as retrieve's rerankExcerpt.
const gapExcerptMin = 240

// gapFTSLimit is how many chunks the keyword fallback may contribute per
// name. It is the weakest of the three lookups — a name matched as a word in
// raw text, not as a definition — so it lands a couple of chunks or none.
const gapFTSLimit = 2

// gapMaxLandings caps the landings one token value may contribute, one per
// file: a property key is set once per stage, and four stages is a report.
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
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature),
		llm.WithMaxTokens(gapMaxTokens), llm.WithStep("gap"))
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
		g.logger().Warn("gap reply was not JSON; the gathered sources kept", "reply", gapExcerpt(out, 120))
		return sources, GapReport{Skipped: "not json"}, nil
	}

	report := GapReport{Asked: gapNames(reply.Missing, sources)}
	if len(report.Asked) == 0 {
		return sources, report, nil
	}

	// The full budget, with what the sources already cost recomputed rather
	// than carried: FillGaps is called with what the walk returned, and the
	// reserve exists precisely so there is room left under that ceiling.
	a := &admitter{seen: map[int64]bool{}, budget: g.opts.TokenBudget}
	for _, s := range sources {
		a.seen[s.ChunkID] = true
		a.spent += estimateTokens(s.Text)
	}
	a.out = append(a.out, sources...)

	refused := false
	for i, n := range report.Asked {
		// Once the reserve has refused a landing, what is left cannot hold
		// even a source's smallest share of the prompt, and every further
		// lookup queries the index for a chunk nothing can admit.
		if refused && a.budget-a.spent < gapExcerptMin/4 {
			for _, rest := range report.Asked[i:] {
				report.Refused = append(report.Refused, rest.Name)
			}
			break
		}
		landings, err := g.resolve(ctx, n, sources, stage)
		if err != nil {
			return sources, GapReport{}, err
		}
		// The stage restriction before the emptiness check: a name only
		// another stage holds is one this turn could not resolve, and
		// dropping it silently would leave the report short a name.
		landings = within(landings, stage)
		if len(landings) == 0 {
			report.Unresolved = append(report.Unresolved, n.Name)
			continue
		}
		before := len(a.out)
		noRoom := false
		for _, l := range landings {
			if !a.take(l, g.opts.MaxHops+1) {
				noRoom, refused = true, true
			}
		}
		// Landed means something NEW was admitted: a name resolved onto
		// chunks the model was already reading cost the pass a lookup and
		// gained the answer nothing.
		switch {
		case len(a.out) > before:
			report.Landed = append(report.Landed, n.Name)
		case noRoom:
			report.Refused = append(report.Refused, n.Name)
		}
	}
	return a.out, report, nil
}

// resolve turns one name into landings, deterministically. The first lookup
// that yields a chunk wins; a name none of them resolves is reported, never
// guessed at.
func (g *Gatherer) resolve(ctx context.Context, n GapName, sources []Source, stage retrieve.StagePrefixes) ([]Source, error) {
	var landings []Source
	var err error
	switch edges.Kind(n.Kind) {
	case edges.KindRoute, edges.KindDestination, edges.KindProperty:
		landings, err = g.tokenLandings(ctx, edges.Kind(n.Kind), n.Name, stage)
	default:
		landings, err = g.symbolLandings(ctx, n.Name, sources)
	}
	if err != nil || len(landings) > 0 {
		return landings, err
	}
	return g.keywordLandings(ctx, n.Name, stage)
}

// symbolLandings finds where a name is DEFINED, under the same selectivity
// ceiling the walk follows, and prefers a repository the answer is already
// being written about: sibling products define the same identifiers byte for
// byte, and a lookup by name alone would cite whichever path sorts first.
func (g *Gatherer) symbolLandings(ctx context.Context, name string, sources []Source) ([]Source, error) {
	// No home and no near side: the caller has a name and no file, so every
	// selective definer over the enabled repositories is a candidate.
	rows, err := g.definers(ctx, []string{name}, "", "", "")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	rows = atHome(rows, sources)
	out := make([]Source, 0, len(rows))
	for _, s := range rows {
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
func (g *Gatherer) tokenLandings(ctx context.Context, kind edges.Kind, name string, stage retrieve.StagePrefixes) ([]Source, error) {
	var out []Source
	files := map[string]bool{}
	for _, v := range gapValues(kind, name) {
		ns, err := edges.Holders(ctx, g.db, kind, v)
		if err != nil {
			return nil, fmt.Errorf("look up the holders of %s %q: %w", kind, v, err)
		}
		for _, h := range ns {
			if !inStage(h.Repo, h.Path, stage) {
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

// keywordLandings is the last resort: the name as a strict FTS term. This is
// the ONE path whose Reason carries the model's own string — no index row
// was matched to correct the spelling — so it is deliberately the narrowest,
// at gapFTSLimit chunks.
func (g *Gatherer) keywordLandings(ctx context.Context, name string, stage retrieve.StagePrefixes) ([]Source, error) {
	match := retrieve.BuildFTSMatch(name)
	if match == "" {
		return nil, nil
	}
	hits, err := retrieve.NewStore(g.db).SearchKeywordIn(ctx, match, gapFTSLimit, nil, stage)
	if err != nil {
		return nil, fmt.Errorf("look up %q in the keyword lane: %w", name, err)
	}
	out := make([]Source, 0, len(hits))
	for _, h := range hits {
		out = append(out, Source{
			ChunkID: h.ChunkID, Repo: h.Repo, Branch: h.Branch, Path: h.Path,
			Symbol: h.Symbol, StartLine: h.StartLine, EndLine: h.EndLine,
			SHA: h.SHA, Text: h.RawText, Reason: "gap:" + name,
		})
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
		if n.Name == "" || len([]rune(n.Name)) > gapNameRunes || seen[n.Name] {
			continue
		}
		if strings.IndexFunc(n.Name, unicode.IsSpace) >= 0 {
			continue
		}
		seen[n.Name] = true
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
// The crossing is compared by kind and value, never as a substring of the
// reason: a crossing on "/orders/123/items" is not the route "/orders", and
// reading it as one drops the name the model asked for.
func amongSources(n GapName, sources []Source) bool {
	values := gapValues(edges.Kind(n.Kind), n.Name)
	for _, s := range sources {
		if s.Symbol == n.Name || s.Reason == "reference:"+n.Name {
			return true
		}
		kind, value, ok := edgeVia(s.Reason)
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
		b.WriteString(gapExcerpt(s.Text, share))
		b.WriteString("\n")
	}
	return b.String()
}

// gapExcerpt cuts s to at most n RUNES, marker included, so a share counted
// in runes is never overrun by one — and so a cut never lands inside a
// multi-byte character and hands the model a replacement glyph.
func gapExcerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n || n < 1 {
		return s
	}
	return string(r[:n-1]) + "…"
}
