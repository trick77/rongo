package ask

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// Source is one piece of code the answer may be built on, and the reason it is
// here. The reason is not decoration: it is what lets an answer say "this file
// came in because grant.go calls NewGrant" instead of presenting twenty files
// as equally relevant.
type Source struct {
	ChunkID   int64
	Repo      string
	Branch    string
	Path      string
	Symbol    string
	StartLine int
	EndLine   int
	// SHA is the commit the file was indexed at; see retrieve.Hit.SHA.
	SHA  string
	Text string
	// Reason is "hit" for something the search returned,
	// "reference:<symbol>" for something a symbol hop reached, or
	// "edge:<kind> <value> from <repo>/<path>" for something reached across a
	// repository boundary on a shared queue name or route.
	Reason string
	// Hop is 0 for a search hit and counts up from there.
	Hop int
	// Kind is "" for a chunk of a file and SourceCommit for an entry of the
	// commit lane, where SHA is the commit itself, Path and the lines are
	// empty, Text is the message body, and the fields below are set.
	Kind string
	// CommitID is the commits row, the way ChunkID is the chunks row; it is
	// what the record stores so a re-explain can read the commit back.
	CommitID    int64
	Subject     string
	CommittedAt time.Time
	// Paths are the files the commit changed.
	Paths []string
}

// SourceCommit is Source.Kind for a commit of the lane.
const SourceCommit = "commit"

// IsCommit reports whether the source is a commit rather than a chunk.
func (s Source) IsCommit() bool { return s.Kind == SourceCommit }

// maxDefiners is how many files may define a name before following it is
// pointless. The value and its measurement live in internal/edges, which
// walks the same rule; see edges.MaxDefiners.
const maxDefiners = edges.MaxDefiners

// GatherOptions bounds the walk. Both bounds exist because a mechanism spread
// over a handler, a service and a template is exactly what plain top-k misses —
// and following references without a cap walks the whole corpus instead.
type GatherOptions struct {
	// MaxHops is how far the reference walk may travel from a search hit.
	MaxHops int
	// TokenBudget caps the assembled context, in estimated tokens. Search hits
	// are never evicted by it: an answer cites what it was built on, and a
	// citation into dropped material is one rongo cannot stand behind.
	TokenBudget int
	// NoCrossings switches the repository crossing off, leaving the symbol
	// walk alone. The product never sets it; it exists for the evaluation
	// harness, which has to measure the walk with and without the edge table
	// on the same questions to say what the crossing buys.
	NoCrossings bool
	// RouteSuffix lets a crossing match a client's route tail against the
	// path a server serves; see edges.Match. Off until the flow corpus says
	// what it buys.
	RouteSuffix bool
	// WholeFileTokens is the size up to which the file a hit sits in is read
	// whole, in estimated tokens; a larger file contributes the rest of the
	// hit's own symbol only. Zero is off, which the eval's baseline arm uses.
	// See wholeFile.
	WholeFileTokens int
}

// Gatherer expands search hits into the material an answer is written from.
type Gatherer struct {
	db   *sql.DB
	opts GatherOptions
	// gap is the short-gate client the gap pass calls; nil is off, and off is
	// what the product ships until the arm is measured. See gap.go.
	gap *llm.Client
	// locate is the client the locate loop calls and locateSearch the
	// retriever its search and grep tools use; nil is off, on the same terms
	// as gap. Both are set together by WithLocateLoop. See locate.go.
	locate       *llm.Client
	locateSearch Searcher
	// locateRounds caps the loop for the harness arm that measures one look
	// against the loop; zero means locateMaxRounds.
	locateRounds int
	// ceiling is the turn's repositories, set per turn by within; empty
	// admits from anywhere.
	ceiling []string
	// terms are the question's words, set per turn by withTerms. A config
	// file crosses to another config file only on a key sharing one of
	// them. Nil (the harness, a bare Gather) keeps every crossing.
	terms map[string]bool
	// Log receives the warning when the gap pass keeps the sources it was
	// given; nil means the default logger.
	Log *slog.Logger
}

// NewGatherer builds a Gatherer.
func NewGatherer(db *sql.DB, o GatherOptions) *Gatherer {
	if o.MaxHops < 0 {
		o.MaxHops = 0
	}
	if o.TokenBudget <= 0 {
		o.TokenBudget = 24000
	}
	return &Gatherer{db: db, opts: o}
}

// Gather turns search hits into sources, following symbol references outward.
//
// No hits means no sources and no error. "No hit means no hit" is an answer the
// caller reports with the terms it tried; inventing a starting point here would
// produce a confident answer about whatever happened to be nearby.
func (g *Gatherer) Gather(ctx context.Context, hits []retrieve.Hit) ([]Source, error) {
	return g.GatherWithin(ctx, hits, nil)
}

// GatherWithin is Gather with a crossing landing only where stage allows: a
// repository declaring stages is entered only under the asked stage's
// directory, the same restriction the search ran under. Without it the
// search would honour "in production" and the crossing would land on every
// stage's line anyway, with the answer then reporting them all.
func (g *Gatherer) GatherWithin(ctx context.Context, hits []retrieve.Hit, stage retrieve.StagePrefixes) ([]Source, error) {
	return g.GatherSeeded(ctx, hits, nil, stage)
}

// GatherSeeded is GatherWithin with sources the caller has already settled
// on beside the hits: the landings of a link census. They are taken the way
// hits are — at hop 0, whole, never evicted by the budget — and after the
// hits, so the walk's frontier starts from both. A seed the hits already
// carry is not taken twice.
func (g *Gatherer) GatherSeeded(ctx context.Context, hits []retrieve.Hit, seeds []Source, stage retrieve.StagePrefixes) ([]Source, error) {
	if len(hits) == 0 && len(seeds) == 0 {
		return nil, nil
	}

	a := &admitter{seen: map[int64]bool{}, allowed: g.allowed()}

	for _, h := range hits {
		if a.seen[h.ChunkID] {
			continue
		}
		a.seen[h.ChunkID] = true
		a.out = append(a.out, Source{
			ChunkID: h.ChunkID, Repo: h.Repo, Branch: h.Branch, Path: h.Path,
			Symbol: h.Symbol, StartLine: h.StartLine, EndLine: h.EndLine,
			SHA: h.SHA, Text: h.RawText, Reason: "hit", Hop: 0,
		})
		a.spent += estimateTokens(h.RawText)
	}
	for _, s := range seeds {
		if a.seen[s.ChunkID] {
			continue
		}
		a.seen[s.ChunkID] = true
		s.Hop = 0
		a.out = append(a.out, s)
		a.spent += estimateTokens(s.Text)
	}

	// The symbol walk spends up to the budget less the crossing reserve, and
	// less the gap reserve when the gap pass is on; see crossingReserve and
	// gapReserve for why each reserve exists and what it costs. The two are
	// independent: an arm with crossings off and the gap pass on reserves for
	// the gap pass alone.
	symbolBudget := g.opts.TokenBudget
	if !g.opts.NoCrossings {
		symbolBudget -= g.opts.TokenBudget / crossingReserve
	}
	if g.gap != nil {
		symbolBudget -= g.opts.TokenBudget / gapReserve
	}
	if g.locate != nil {
		symbolBudget -= g.opts.TokenBudget / locateReserve
	}
	// The crossing runs under the full budget less the gap reserve — the
	// crossing arm spends whatever it is given on the flow corpus, and a gap
	// pass under the same ceiling would land nothing. The locate loop runs
	// last of all and is reserved for on the same terms.
	crossingBudget := g.opts.TokenBudget
	if g.gap != nil {
		crossingBudget -= g.opts.TokenBudget / gapReserve
	}
	if g.locate != nil {
		crossingBudget -= g.opts.TokenBudget / locateReserve
	}
	a.budget = symbolBudget

	// The rest of the file a hit sits in, before any symbol hop: the nearest
	// explanation of a chunk is the chunk beside it, and the two unique
	// questions the walk never reached were a constant explained one chunk
	// away from the hit with no symbol linking them. Under the symbol walk's
	// budget, and taken at hop 0 — it is the hit's own file — but never
	// evicting a hit, which take guarantees.
	if g.opts.WholeFileTokens > 0 {
		filed := map[string]bool{}
		for _, h := range hits {
			key := h.Repo + "\x00" + h.Path
			if filed[key] {
				continue
			}
			filed[key] = true
			more, err := g.wholeFile(ctx, h)
			if err != nil {
				return nil, err
			}
			for _, s := range more {
				if !a.take(s, 0) {
					break
				}
			}
		}
	}
	frontier := a.out
symbols:
	for hop := 1; hop <= g.opts.MaxHops; hop++ {
		var next []Source
		for _, from := range frontier {
			refs, err := g.referenced(ctx, from)
			if err != nil {
				return nil, err
			}
			for _, ref := range mechanismFirst(refs) {
				// Outside the ceiling: never admitted, and never followed,
				// or the next hop would gather by a reason the answer never
				// shows.
				if a.seen[ref.ChunkID] || !a.permits(ref) {
					continue
				}
				if !a.take(ref, hop) {
					break symbols
				}
				next = append(next, ref)
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	if g.opts.NoCrossings {
		return a.out, nil
	}
	a.budget = crossingBudget

	// The crossing, from EVERYTHING the walk gathered — hits and references
	// alike. Measured on the flow corpus, every edge-only miss had the same
	// shape: the literal sat in a file NEXT TO the one the answer needs.
	// OrdersController calls config.getPaymentUri(), and the string
	// "/paymentAuth" lives in the properties class the symbol walk reached at
	// hop one. Crossing only from the hits would consult the edge table only
	// for files that happen to carry their own literal.
	//
	// A crossing lands on one chunk and then takes ONE symbol hop on the far
	// side, because the far end of a queue is a Spring configuration class and
	// the file that does the work is the handler it wires up, which carries
	// no literal at all. That is the composed walk docs/measurements/
	// 2026-09-10-integration-edges.md measured: in-repo hop, crossing, in-repo
	// hop. It does not count against MaxHops — a crossing is bounded by the
	// spread ceiling and lands on a single chunk, where a symbol hop fans out
	// — and it runs under the FULL budget, which is what the reserve is for.
	//
	// Two passes over the same starts: routes and destinations from every
	// file first, property keys from every file second. The reserve was
	// measured with the first two kinds alone, and a property key is a
	// weaker link — spring.application.name is set in every service's
	// properties, and one interceptor reading it lands on three of them.
	// Taken in file order those landings spent the reserve before the
	// configuration class three files later got to cross on its routes, and
	// the flow corpus lost a part. A kind that came later may only add after
	// the measured ones have had the whole reserve.
	//
	// land takes one landing and its one in-repo hop on the far side, and
	// reports false when the budget is spent — and stopping means stopping,
	// for the reason take gives.
	land := func(landing, from Source) (bool, error) {
		// Outside the ceiling: not a landing, and its far-side hop is not
		// taken either.
		if a.seen[landing.ChunkID] || !a.permits(landing) {
			return true, nil
		}
		if !a.take(landing, from.Hop+1) {
			return false, nil
		}
		if isPropertyEdge(landing.Reason) {
			// A properties file is the far side, and it references no
			// symbol; the words in it join whatever happens to be called
			// "processor" or "cleanup" and spend the reserve on that. One
			// stage's line costs one chunk, which is what lets every stage
			// fit: the first run took intg's chunk plus its "references"
			// and had no room left for prod.
			return true, nil
		}
		inland, err := g.referenced(ctx, landing)
		if err != nil {
			return false, err
		}
		for _, ref := range mechanismFirst(inland) {
			if !a.take(ref, from.Hop+2) {
				return false, nil
			}
		}
		return true, nil
	}
	crossed := map[string]bool{}
	starts := append([]Source{}, a.out...)
	var later []crossing
	for _, from := range starts {
		key := from.Repo + "\x00" + from.Path
		if crossed[key] {
			// One file crosses once, whichever of its chunks got here.
			continue
		}
		crossed[key] = true
		far, err := g.crossings(ctx, from)
		if err != nil {
			return nil, err
		}
		for _, landing := range mechanismFirst(within(far, stage)) {
			if isPropertyEdge(landing.Reason) {
				later = append(later, crossing{from: from, landing: landing})
				continue
			}
			more, err := land(landing, from)
			if err != nil {
				return nil, err
			}
			if !more {
				return a.out, nil
			}
		}
	}
	// Property landings share the crossing reserve, never the whole
	// crossing budget: on a live turn they took 22.9k of 24k tokens with
	// settings nobody asked about. The reserve is what the digest-mail
	// measurement ran under (docs/measurements/2026-09-13-infra-stages.md).
	propertyStart, propertyLimit := a.spent, g.opts.TokenBudget/crossingReserve
	for _, c := range later {
		if a.spent-propertyStart+estimateTokens(c.landing.Text) > propertyLimit {
			continue
		}
		more, err := land(c.landing, c.from)
		if err != nil {
			return nil, err
		}
		if !more {
			return a.out, nil
		}
	}
	return a.out, nil
}

// admitter admits reached chunks under a token budget, keeping what has been
// taken, what it cost and what was seen. It reports false when the budget is
// spent, and the caller then STOPS rather than trimming what is already
// gathered, so what the answer cites is always present — and stopping means
// stopping: continuing would keep querying the rest of the frontier for rows
// that can never be taken.
//
// budget is set by the phase rather than passed per call: the symbol walk,
// the crossing and the gap pass each run under a different ceiling, and a
// phase that changed it halfway would spend another phase's reserve.
type admitter struct {
	seen   map[int64]bool
	spent  int
	budget int
	out    []Source
	// allowed is the turn's ceiling; nil admits from any repository.
	allowed map[string]bool
}

// take admits s at hop, or reports false when the budget cannot hold it. A
// chunk already taken is not admitted twice and is not a refusal.
func (a *admitter) take(s Source, hop int) bool {
	if a.seen[s.ChunkID] {
		return true
	}
	// Outside the turn's ceiling: skipped, and not a refusal. A refusal
	// means the budget is spent and stops the caller; this landing was
	// never the turn's to take.
	if a.allowed != nil && !a.allowed[s.Repo] {
		return true
	}
	cost := estimateTokens(s.Text)
	if a.spent+cost > a.budget {
		return false
	}
	a.seen[s.ChunkID] = true
	s.Hop = hop
	a.spent += cost
	a.out = append(a.out, s)
	return true
}

// permits reports whether s lies inside the turn's ceiling. Callers that go
// on to follow what they admitted check it first: take skips an outside
// chunk silently, which is right for admission and wrong for a walk that
// would then follow it.
func (a *admitter) permits(s Source) bool {
	return a.allowed == nil || a.allowed[s.Repo]
}

// crossing is a landing held back for the second pass, with the source it
// was reached from, so its hop is counted from the right place.
type crossing struct {
	from, landing Source
}

// edgeVia reads a crossing's reason, "edge:<kind> <value> from <repo>/<path>":
// the token that crossed, and the near side it was followed from. It reports
// false for a reason of any other shape.
//
// One parser, because the grammar has a value in the middle of it: a reader
// matching "route /orders" as a substring also matches the crossing that
// landed on "/orders/123/items", and calls a route nobody followed followed.
func edgeVia(reason string) (kind, value, from string, ok bool) {
	rest, ok := strings.CutPrefix(reason, "edge:")
	if !ok {
		return "", "", "", false
	}
	via, from, ok := strings.Cut(rest, " from ")
	if !ok {
		return "", "", "", false
	}
	kind, value, ok = strings.Cut(via, " ")
	if !ok || kind == "" || value == "" {
		return "", "", "", false
	}
	return kind, value, from, true
}

// isPropertyEdge reports a landing reached over a property key.
func isPropertyEdge(reason string) bool {
	kind, _, _, ok := edgeVia(reason)
	return ok && kind == string(edges.KindProperty)
}

// crossingReserve is the share of the token budget the symbol walk leaves
// untouched for repository crossings: one part in six, 4000 of the default
// 24000 tokens.
//
// Without it the crossing never runs on a corpus that fans out. Measured on
// the flow corpus (Java, Spring): twenty hits reached 141 sources at the FIRST
// symbol hop and the budget was gone before a single edge was consulted, so
// the product gathered exactly what the walk alone gathered — 20 of 30 parts,
// and the order-placement flow at 2 of 6 with payment, shipping and the queue
// consumer never in front of the model. A crossing is the one hop the symbol
// walk cannot make, and it is scarce: at most three repositories per token
// and one chunk per landing. Six chunks of room is what the flagship flow
// needs, and a corpus with no edges at all pays for the reserve with six
// fewer reference chunks out of a hundred and ten. What that costs on the Go
// corpus is measured, not assumed: TestEvalMeasureGathered, in
// internal/retrieve/eval.
const crossingReserve = 6

// gapReserve is the share of the token budget the symbol walk and the
// crossing both leave untouched for the gap pass: one part in twelve, 2000 of
// the default 24000 tokens, which is eight names at around 250 tokens each.
//
// Reserved only when the pass is on. The crossing arm spends whatever budget
// it is given — on the flow corpus the walk and the crossings together reach
// the ceiling — so a gap pass running under the same budget would resolve
// names it could then never admit, and the arm would measure as doing
// nothing. What the reserve costs the walk is the same kind of fact
// crossingReserve's is, and it is measured by the harness arms, not assumed.
const gapReserve = 12

// wholeFile returns the chunks of a hit's file the answer should read
// beside the hit: every other chunk when the file is small (at most
// WholeFileTokens in all), otherwise only the chunks that continue the
// hit's own symbol — a function cut into windows is one mechanism, and
// half of it is not. Ordered by ordinal, so the file reads in order: the
// ordinal is the chunk's position in its file, not its id, so it says the
// same thing after a re-index.
func (g *Gatherer) wholeFile(ctx context.Context, h retrieve.Hit) ([]Source, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
		FROM files f
		JOIN repo_state r ON r.name = f.repo
		JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND f.path = ?
		ORDER BY c.ordinal`, h.Repo, h.Path)
	if err != nil {
		return nil, fmt.Errorf("read the file of %s/%s: %w", h.Repo, h.Path, err)
	}
	defer func() { _ = rows.Close() }()
	var all []Source
	total := 0
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol, &s.StartLine, &s.EndLine, &s.Text); err != nil {
			return nil, fmt.Errorf("scan a chunk of %s/%s: %w", h.Repo, h.Path, err)
		}
		total += estimateTokens(s.Text)
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Source
	for _, s := range all {
		if s.ChunkID == h.ChunkID {
			continue
		}
		switch {
		case total <= g.opts.WholeFileTokens:
			s.Reason = "file:whole"
		case h.Symbol != "" && s.Symbol == h.Symbol:
			s.Reason = "file:symbol"
		default:
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// mechanismFirst orders one hop's candidates so that test files come after
// everything else. A test that references the service is a correct hop and a
// poor waypoint — the composed-walk measurement reached payment/service.go
// through component_test.go — and retrieval already demotes tests for the
// same reason (retrieve.DefaultTestDecay). Within each half the incoming
// order is kept, so the walk stays deterministic.
func mechanismFirst(ss []Source) []Source {
	out := make([]Source, 0, len(ss))
	for _, s := range ss {
		if !retrieve.IsTestPath(s.Path) {
			out = append(out, s)
		}
	}
	for _, s := range ss {
		if retrieve.IsTestPath(s.Path) {
			out = append(out, s)
		}
	}
	return out
}

// crossings finds, in OTHER repositories, the chunk holding the same queue
// name or route literal as from's file — the one link a producer and its
// consumer share when they share no import, no type and no symbol.
//
// It lands on the chunk at the token's line, never on the whole file: the far
// side of "/paymentAuth" is one handler registration in a transport file,
// and the rest of that file is no more relevant than any other. The
// selectivity rule (a token in more than three repositories is a convention,
// not a link) and the enabled-only filter live in edges.Neighbours, so this
// walk and the measurement in internal/edges cannot disagree about which
// tokens cross.
func (g *Gatherer) crossings(ctx context.Context, from Source) ([]Source, error) {
	ns, err := edges.NeighboursWith(ctx, g.db, from.Repo, from.Path, edges.Match{Suffix: g.opts.RouteSuffix})
	if err != nil {
		return nil, fmt.Errorf("cross from %s/%s: %w", from.Repo, from.Path, err)
	}
	// Routes and destinations before properties. The reserve was measured
	// with the first two alone (docs/measurements/2026-09-11-edges-in-
	// gather.md), and a file reading twenty properties would otherwise
	// spend it on configuration lines before the queue's far side is
	// reached. A newer kind may only add after the measured ones.
	sort.SliceStable(ns, func(i, j int) bool { return kindRank(ns[i].Kind) < kindRank(ns[j].Kind) })
	var out []Source
	for _, n := range ns {
		if !g.crossReason(n, from) {
			continue
		}
		s, ok, err := g.chunkAt(ctx, n.Repo, n.Path, n.Line)
		if err != nil {
			return nil, fmt.Errorf("read the far side of %s %q: %w", n.Kind, n.Value, err)
		}
		if !ok {
			continue
		}
		s.Reason = fmt.Sprintf("edge:%s %s from %s/%s", n.Kind, n.Value, from.Repo, from.Path)
		out = append(out, s)
	}
	return out, nil
}

// crossReason reports whether a neighbour is worth crossing to. Code reading
// a key, a route and a destination always are: the code is the reason. A
// config file crossing to another config file on a shared key is only when
// the key shares a word with the question: two repositories both setting
// spring.h2.console.enabled says nothing about a question on how a field is
// sent. Without question words (terms nil) every crossing stays.
func (g *Gatherer) crossReason(n edges.Neighbour, from Source) bool {
	if g.terms == nil || n.Kind != edges.KindProperty {
		return true
	}
	if !isConfigPath(from.Path) || !isConfigPath(n.Path) {
		return true
	}
	for _, w := range words(n.Value) {
		if g.terms[w] {
			return true
		}
	}
	return false
}

// isConfigPath is a file property keys are SET in: the one kind the edge
// extractor reads keys from (edges.propertyKeys).
func isConfigPath(path string) bool {
	return strings.HasSuffix(path, ".properties")
}

// withTerms is the gatherer that knows the question's words, for
// crossReason. A copy, so the shared gatherer is never changed.
func (g *Gatherer) withTerms(question string) *Gatherer {
	c := *g
	c.terms = map[string]bool{}
	for _, w := range words(question) {
		if !questionStopword[w] {
			c.terms[w] = true
		}
	}
	return &c
}

// words splits text into lowercase words: on anything not a letter or digit,
// and on a lower-to-upper case change, so "sendDigest", "send-digest" and
// "send.digest" all read as send and digest. Words of one rune are dropped.
func words(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 {
			out = append(out, strings.ToLower(string(cur)))
		}
		cur = cur[:0]
	}
	prevLower := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if prevLower && unicode.IsUpper(r) {
				flush()
			}
			cur = append(cur, r)
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return out
}

// questionStopword are words too common in a question to count as a reason
// to cross: two letters and function words, in the two languages questions
// arrive in.
var questionStopword = map[string]bool{
	"is": true, "in": true, "to": true, "an": true, "of": true, "on": true, "at": true,
	"it": true, "be": true, "by": true, "or": true, "the": true, "and": true, "how": true,
	"what": true, "where": true, "when": true, "does": true, "for": true, "with": true,
	"im": true, "am": true, "zu": true, "wo": true, "wie": true, "der": true, "die": true,
	"das": true, "und": true, "ist": true, "wird": true, "den": true, "dem": true,
	"ein": true, "eine": true, "mit": true, "von": true, "für": true, "auf": true,
}

// chunkAt is the chunk of repo/path covering line, with no reason set: what a
// landing on a token's line is, whoever found the line. Reports false when no
// chunk covers it — an overlong line that was split, or a file whose chunks
// moved since the token was written. Nothing to cite, so nothing to gather.
func (g *Gatherer) chunkAt(ctx context.Context, repo, path string, line int) (Source, bool, error) {
	var s Source
	err := g.db.QueryRowContext(ctx, `
		SELECT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
		FROM files f
		-- enabled = 1 like every other lookup here: a parked repository is
		-- not a hop target. It was harmless while every caller passed a
		-- repo and path taken from already-filtered rows; the locate loop's
		-- read tool is the first to pass a repo the MODEL named, and a
		-- parked one would be admitted as a source and cited in a fresh
		-- answer. Parking stops new answers, it does not revise old ones,
		-- so the source viewer and stored thread sources still do not filter.
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND f.path = ? AND ? BETWEEN c.start_line AND c.end_line
		-- Chunk windows OVERLAP, so a token's line is covered by more
		-- than one of them and this LIMIT 1 is a choice. The ordinal is
		-- the chunk's position in its FILE, not its id, so the same line
		-- lands on the same chunk after a re-index.
		ORDER BY c.ordinal
		LIMIT 1`, repo, path, line).Scan(
		&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol, &s.StartLine, &s.EndLine, &s.Text)
	if err == sql.ErrNoRows {
		return Source{}, false, nil
	}
	if err != nil {
		return Source{}, false, err
	}
	return s, true, nil
}

// referenced finds chunks defining a symbol that from's code actually mentions.
//
// Both halves are required, and that is the whole point: the symbols table says
// where a name is DEFINED, the source text says whether this code depends on
// it. Following every definition would drag in the corpus; following only the
// text would have nowhere to go.
func (g *Gatherer) referenced(ctx context.Context, from Source) ([]Source, error) {
	names := identifiers(from.Text)
	if len(names) == 0 {
		return nil, nil
	}
	out, err := g.definers(ctx, names, from.Repo, from.Repo, from.Path)
	if err != nil {
		return nil, fmt.Errorf("follow references from %s: %w", from.Path, err)
	}
	return out, nil
}

// definers finds the chunks that DEFINE any of names, under the selectivity
// ceiling, over enabled repositories only.
//
// home is the repository a name is resolved in when it defines it at all:
// see the rule below. notRepo/notPath is the file the names were read from,
// which never defines itself into its own result. All three empty is the
// caller that has no near side — the gap pass, which has a name and no file
// — and then every selective definer in the corpus is an answer.
func (g *Gatherer) definers(ctx context.Context, names []string, home, notRepo, notPath string) ([]Source, error) {
	if len(names) == 0 {
		return nil, nil
	}

	// Names defined all over the corpus are filtered out by how common they
	// are, not by a hand-kept stoplist. Measured on the real index: Close is
	// defined in 31 files, err in 23, Error in 10. Following those pulls in
	// alphabetically-first junk until the budget is gone, and the module the
	// question is about never gets reached. A name that thirty files define
	// says nothing about which one this code depends on.
	// `home` is the second half of "cross a repository boundary only with two
	// reasons". The name-based walk cannot tell peeq's randomToken from loom's
	// — sibling products built from the same template define the same
	// identifiers byte for byte — and the ordering below then handed the
	// answer whichever path sorted first. A reader following that citation
	// lands in the other product while the answer says this one.
	//
	// So a name that the source's OWN repository defines is resolved there and
	// nowhere else. Crossing stays possible, and stays the point: a name this
	// repository does not define at all is genuine composition — peeq calling
	// into go-sqlite3 — and still travels.
	//nolint:gosec // only fixed SQL structure is interpolated (a ?-placeholder list or a literal table name); every value is a bound ? parameter
	q := `
-- Every count here is over ENABLED repositories only. Filtering just the final
-- join would keep a parked repository out of the result while still letting it
-- shape one: its definitions would count towards maxDefiners, push a name over
-- the threshold, and drop it from selective — costing a LIVE repository a hop
-- it should have made. Parked code influences nothing.
WITH selective AS (
    SELECT s.name
    FROM symbols s
    JOIN files sf ON sf.id = s.file_id
    JOIN repo_state sr ON sr.name = sf.repo AND sr.enabled = 1
    WHERE s.name IN (` + placeholders(len(names)) + `)
    GROUP BY s.name
    HAVING COUNT(DISTINCT s.file_id) <= ?
),
home AS (
    SELECT DISTINCT s.name
    FROM symbols s
    JOIN selective sel ON sel.name = s.name
    JOIN files f ON f.id = s.file_id
    WHERE f.repo = ?
)
SELECT DISTINCT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text, s.name,
       (SELECT COUNT(DISTINCT s2.file_id) FROM symbols s2
          JOIN files f2 ON f2.id = s2.file_id
          JOIN repo_state r2 ON r2.name = f2.repo AND r2.enabled = 1
        WHERE s2.name = s.name) AS definers
FROM symbols s
JOIN selective sel ON sel.name = s.name
JOIN files f  ON f.id = s.file_id
-- enabled = 1: a parked repository is not a hop target. AGENTS.md already
-- requires the target to be indexed before crossing a boundary, and a
-- repository the reader cannot see on the Repos page is not one to pull code
-- out of on rongo's own initiative.
JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
JOIN chunks c ON c.file_id = f.id AND s.line BETWEEN c.start_line AND c.end_line
WHERE NOT (f.repo = ? AND f.path = ?)
  AND (f.repo = ? OR s.name NOT IN (SELECT name FROM home))
-- The path stays ahead of the repository: putting the repository first would
-- regroup a multi-repository result by product and change which chunks the
-- budget admits, which is a ranking change and not the ordering fix this is.
-- The repository is only the tie-break two repositories holding one path
-- need. Inside a file the ordinal keeps it in FILE order, and s.name comes
-- last of all, for two selective names landing on ONE chunk: it fixes the
-- Reason that chunk carries without ever deciding which chunk comes first.
-- Left to the rowid, each of those reads whichever was indexed first, which
-- moves on a re-index that changed no code.
ORDER BY definers ASC, f.path, f.repo, c.ordinal, s.name`

	args := make([]any, 0, len(names)+5)
	for _, n := range names {
		args = append(args, n)
	}
	args = append(args, maxDefiners, home, notRepo, notPath, home)

	rows, err := g.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("look up definers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Source
	for rows.Next() {
		var s Source
		var sym string
		var definers int
		if err := rows.Scan(&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol,
			&s.StartLine, &s.EndLine, &s.Text, &sym, &definers); err != nil {
			return nil, fmt.Errorf("scan reference: %w", err)
		}
		s.Reason = "reference:" + sym
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read definers: %w", err)
	}
	return out, nil
}

// identifiers pulls the word-shaped tokens out of source text. Deliberately
// crude — it feeds a lookup against a table of known symbol names, so a wrong
// guess finds nothing rather than fetching the wrong file.
func identifiers(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	uniq := map[string]bool{}
	for _, f := range fields {
		if len(f) > 2 {
			uniq[f] = true
		}
	}
	out := make([]string, 0, len(uniq))
	for f := range uniq {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// estimateTokens is the same ~4-characters-per-token heuristic the chunker
// uses, so a budget here means the same thing it does there.
func estimateTokens(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// inStage reports whether the stage restriction allows this path. A
// repository absent from the restriction is not narrowed; one present keeps
// only paths under its prefix.
func inStage(repo, path string, stage retrieve.StagePrefixes) bool {
	prefix, ok := stage[repo]
	return !ok || strings.HasPrefix(path, prefix)
}

// within keeps the landings the stage restriction allows, for the crossing.
func within(landings []Source, stage retrieve.StagePrefixes) []Source {
	if len(stage) == 0 {
		return landings
	}
	var out []Source
	for _, s := range landings {
		if inStage(s.Repo, s.Path, stage) {
			out = append(out, s)
		}
	}
	return out
}

// kindRank orders the edge kinds a crossing follows: the two the reserve was
// measured with first, then property keys.
func kindRank(k edges.Kind) int {
	switch k {
	case edges.KindRoute:
		return 0
	case edges.KindDestination:
		return 1
	default:
		return 2
	}
}
