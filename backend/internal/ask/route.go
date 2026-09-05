package ask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repodeps"
	"github.com/trick77/rongo/internal/retrieve"
)

// Candidate is one place an answer could come from: a module in a repository,
// with the hits that put it on the list.
type Candidate struct {
	Repo      string
	Branch    string
	ModuleKey string
	// Title and Summary are written per turn by the naming call, and only when
	// the reader will actually see them. Bare module keys do not work as
	// titles: peeq and loom both have httpapi, and a card offering "httpapi"
	// against "httpapi" is a question without content.
	Title   string
	Summary string
	// Score is the candidate's BEST hit, never the sum. Summing rewards size,
	// and phase 3 measured that doing so makes the ranking worse.
	Score float64
	Hits  []retrieve.Hit
}

// candidates groups hits into the units routing reasons about, best first.
func candidates(hits []retrieve.Hit, moduleOf func(repo, path string) string) []Candidate {
	index := map[string]int{}
	var out []Candidate
	for _, h := range hits {
		key := moduleOf(h.Repo, h.Path)
		id := h.Repo + "\x00" + key
		i, ok := index[id]
		if !ok {
			out = append(out, Candidate{Repo: h.Repo, Branch: h.Branch, ModuleKey: key})
			i = len(out) - 1
			index[id] = i
		}
		out[i].Hits = append(out[i].Hits, h)
		if h.Score > out[i].Score {
			out[i].Score = h.Score
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// dominates reports whether the leading candidate is far enough ahead to answer
// without asking. Relative, not absolute: fused scores have no fixed range, so
// a constant gap would mean something different for every question.
func dominates(cs []Candidate, margin float64) bool {
	if len(cs) < 2 {
		return true
	}
	if cs[0].Score <= 0 {
		return false
	}
	return (cs[0].Score-cs[1].Score)/cs[0].Score > margin
}

// candidateFloor is how far behind the leader a candidate may fall and still
// be worth putting to the reader, as a share of the leader's score.
//
// Grouping had no absolute floor: the top five modules of the fused twenty
// became the card, whatever they were, and the judge is only ever asked "ask
// or compose" — "these are noise" is not an answer it may give, and its
// instruction is to ask when in doubt. So a straggler that shared nothing with
// the question but a language reached the card at full standing. The floor is
// relative for the same reason the margin is: fused scores have no fixed
// range, and a constant would mean something different for every question.
const candidateFloor = 0.4

// worthOffering drops what the reader should never be asked to choose between:
// a candidate far behind the leader, and a module that is nothing but test
// code. The leader itself is always kept — everything scoring badly is not the
// same as nothing being the best, and a floor that can empty the list would
// turn a weak answer into no answer at all.
//
// This decides what may be OFFERED, never what may be read: Gather still
// starts from every hit (see Pipeline.Run), so a test that is genuinely the
// answer is still quoted and cited.
func worthOffering(cs []Candidate) []Candidate {
	if len(cs) == 0 {
		return cs
	}
	lead := cs[0].Score
	var kept, keptWithTests []Candidate
	for _, c := range cs {
		if lead > 0 && c.Score < lead*candidateFloor {
			continue
		}
		keptWithTests = append(keptWithTests, c)
		if !onlyTests(c.Hits) {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		// Everything that cleared the floor was test material. That is still
		// the best the corpus has, and someone who asked how a thing is TESTED
		// is entitled to it — including the card, when two repositories test
		// the same thing in two places. Returning the leader alone here would
		// silently answer from one of them: dominates() cannot ask about a
		// list of one.
		return keptWithTests
	}
	return kept
}

// onlyTests reports whether every hit that put a candidate on the list is test
// material. A module that mixes the two stays: dropping it would lose the
// mechanism in order to keep out its harness.
func onlyTests(hits []retrieve.Hit) bool {
	if len(hits) == 0 {
		return false
	}
	for _, h := range hits {
		if !retrieve.IsTestPath(h.Path) {
			return false
		}
	}
	return true
}

// lastSlash finds the final path separator, so a directory can be taken as a
// module key without pulling in path/filepath's OS-specific behaviour.
func lastSlash(p string) int {
	return strings.LastIndexByte(p, '/')
}

// maxCandidates is how many the card may offer. The spec says two to five;
// more than five is not a question anyone answers, it is a list.
const maxCandidates = 5

// routeMaxTokens caps the judgement, nameMaxTokens the per-candidate naming.
// Both replies are short structured objects; a longer one means the model
// started explaining itself to nobody.
const (
	routeMaxTokens = 128
	nameMaxTokens  = 160
)

// judgeMarker is a phrase unique to the judge's prompt. Tests use it to tell
// the two calls apart.
const judgeMarker = "alternatives or parts of one whole"

// judgeSystem is phase 4b's wording, kept after phase 4c measured a
// replacement and could not show it earning its keep — see
// docs/measurements/2026-08-19-candidates.md, "The change that did not land".
const judgeSystem = `You decide whether two or more hits are ` + judgeMarker + `.

Answer with JSON ONLY: {"decision":"ask"} or {"decision":"compose"}.

  ask      The hits are independent mechanisms that do the same thing.
           Exactly one of them is meant, and guessing would be wrong.
  compose  The hits are parts of ONE mechanism: one calls the other, one is
           the shared library, one is the facade.

When in doubt "ask": a follow-up question costs one click, an answer composed
across independent mechanisms is simply wrong.`

// choosableMarker is a phrase unique to the role gate's prompt, the way
// judgeMarker is to the judge's. Tests use it to tell the calls apart.
const choosableMarker = "can choose between these options"

// choosableSystem is the role rung: the judge has already said these are
// independent alternatives, and this asks whether the person who has to pick
// one is equipped to. It runs for the Analyst only — a Developer reading the
// same card sees two packages and knows which one they meant.
//
// It judges the CARD, not the code: the titles and summaries the naming call
// just wrote are what the reader will actually see, and a choice that cannot
// be made from them cannot be made at all.
//
// The safe side is the judge's, mirrored. The judge defaults to asking
// because composing independent mechanisms is simply wrong; here the reader
// cannot answer, so the default is to compose — a question nobody can answer
// ends the turn with nothing.
const choosableSystem = `You decide whether a reader who does not read code ` + choosableMarker + `.

Answer with JSON ONLY: {"decision":"choose"} or {"decision":"cannot"}.

  choose  The options do different things in the business domain. Someone who
          knows the business but not the code can tell which one the question
          meant.
  cannot  The options are only tellable apart by code: the same thing for the
          business, differing in implementation, layer, package or technology.
          The reader would be guessing.

When in doubt "cannot": an answer covering all of them costs the reader a
paragraph, a question they cannot answer costs them the turn.`

// nameSystem takes the language name as its one format argument: the card is
// read by the person who asked, in the language they asked for.
const nameSystem = `You name a candidate for a follow-up question, in %s.

Answer with JSON ONLY: {"title":"...","summary":"..."}

  title    At most six words, domain wording. NOT the directory name.
           The reader already sees the repository next to it.
  summary  One sentence on what this code does.

Two candidates have to be tellable apart by their titles, even when both sit
in the same package name.`

// nameBA is added for the Analyst, and only for the Analyst: the Developer's
// naming prompt is what it has always been. The card this writes is also what
// the role gate reads, so a title that names a package rather than a business
// step both misinforms the reader and hides the ambiguity from the rung that
// decides whether to ask at all.
const nameBA = `

Audience: business analyst. The reader does not read code. Title and summary
name what this does for the business - the process, the actor, the rule -
never a package, a type, a file or a directory. Two candidates are told apart
by what they do differently in the domain, not by where they live.`

// nameLanguage closes the Analyst's naming prompt, so the language is named
// first and last around the block above it — the same rule answerLanguage
// applies to the answer.
const nameLanguage = `

Title and summary are written in %s. Repository names stay as they are.`

// Decision is what routing produced: either an answer can be composed from
// every candidate, or the reader has to be asked which one is meant.
type Decision struct {
	Ask        bool
	Candidates []Candidate
}

// Router decides whether a turn can be answered or has to ask.
type Router struct {
	llm    *llm.Client
	db     *sql.DB
	margin float64
	// clusterOpts is the SAME module cut the Repos page counts with — it comes
	// from config, not a value this package invents for itself, so routing
	// never sees a module nobody else in the product can see.
	clusterOpts modules.Opts
	// judgeDeployment selects which MiMo deployment decides ask-vs-compose.
	//
	// Production runs on Pro — the ONE exception to "Pro only where a human
	// reads", and it is written down because it overturns a measurement
	// rather than ignoring one. Phase 4b measured the two deployments a
	// question apart and wrote non-Pro in on that basis. Phase 4c found the
	// reason: no call carried a temperature, so both arms were re-rolling by
	// about three questions per run and the difference was inside the noise.
	// Pinned (see gateTemperature) and run twice, Pro routes 48/61 and 50/61
	// against non-Pro's 42/61 and 43/61 — six to seven questions, against a
	// residual spread of one to two.
	//
	// The bar for the cheap lane is "the output is an id or a label", and the
	// judge's output is one word. That word is the difference between the
	// reader getting an answer and getting a question back, which is why this
	// one is bought on the expensive queue and understanding, naming and the
	// thread title still are not. WithJudgeDeployment overrides it so the
	// eval harness can keep measuring the choice.
	judgeDeployment llm.Option
}

// NewRouter builds a Router. The judge runs on Pro, matching what is
// deployed; see WithJudgeDeployment to change that for a measurement.
func NewRouter(c *llm.Client, db *sql.DB, margin float64, mo modules.Opts) *Router {
	return &Router{llm: c, db: db, margin: margin, clusterOpts: mo, judgeDeployment: nil}
}

// WithJudgeDeployment returns a COPY of the Router with the ask-vs-compose
// judge's deployment overridden — pass llm.ShortGate() for the cheap lane,
// nil for the client's default, which is Pro.
// It does not mutate the receiver: nothing in the product may change the
// shared, production Router's deployment by accident, so selecting a
// different judge deployment means deliberately building a second Router
// rather than silently changing the one everything else uses. This exists for
// the eval harness, which the spec requires to keep measuring the judgement
// against both deployments — production never calls it.
func (r *Router) WithJudgeDeployment(opt llm.Option) *Router {
	cp := *r
	cp.judgeDeployment = opt
	return &cp
}

// Ranked is the routing ladder's grouping rung: every hit gathered into a
// candidate, both uncapped and cut to what a card could ever show. It never
// runs a database query or a model call.
//
// Exported so the eval harness's margin sweep can compute this ONCE per
// question and reuse it at every margin: the grouping does not depend on
// margin, only whether the ladder goes on past it does.
type Ranked struct {
	// All is every candidate worth offering, uncapped — what Dominates tests
	// against, same as Route. Uncapped is not unfiltered: worthOffering has
	// already dropped the stragglers and the test-only modules, and the
	// harness must test the margin against exactly that list, because Route
	// does.
	All []Candidate
	// Capped is All cut to maxCandidates: what the manifest check and the
	// judge see, exactly as Route uses it.
	Capped []Candidate
}

// Rank runs the ladder's first rung: grouping hits into candidates. It calls
// no model and makes no manifest-dependency query — see Related for that,
// which Route only calls once the margin does NOT dominate. Keeping the query
// out of Rank is what keeps the common fast path free of it.
func (r *Router) Rank(ctx context.Context, hits []retrieve.Hit) (Ranked, error) {
	moduleOf, err := r.moduleLookup(ctx, hits)
	if err != nil {
		return Ranked{}, err
	}
	all := worthOffering(candidates(hits, moduleOf))
	capped := all
	if len(capped) > maxCandidates {
		capped = capped[:maxCandidates]
	}
	return Ranked{All: all, Capped: capped}, nil
}

// Dominates reports whether the leading candidate in cs is far enough ahead of
// the runner-up to answer without asking, at the given margin — the rule
// Route applies at its first rung. Exported for the eval harness's margin
// sweep.
func Dominates(cs []Candidate, margin float64) bool {
	return dominates(cs, margin)
}

// Related reports whether any two of cs are joined by a manifest dependency —
// Route's composition rung. This is the expensive, O(n²) database query the
// ladder is ordered to avoid on the common path: Route only calls it after
// Dominates has already said no, and callers that reproduce the ladder
// outside Route (the eval harness's margin sweep) must keep that ordering —
// calling it unconditionally is exactly the regression this method's
// separation from Rank exists to prevent.
func (r *Router) Related(ctx context.Context, cs []Candidate) (bool, error) {
	return r.anyDependency(ctx, cs)
}

// Judge asks the model whether cs are independent alternatives or parts of one
// mechanism — the rung that decides whether the CODE is ambiguous. Exported so
// the eval harness can call it once per question and reuse the answer across
// every margin in its sweep, rather than paying for the model call at each one.
func (r *Router) Judge(ctx context.Context, question string, cs []Candidate) (bool, error) {
	return r.judge(ctx, question, cs)
}

// Choosable asks whether the named candidates are a choice the Analyst can
// make — the rung that decides whether the READER is equipped to answer what
// the judge found ambiguous. Route runs it for the Analyst only, over
// candidates that have already been named. Exported for the eval harness.
func (r *Router) Choosable(ctx context.Context, question string, cs []Candidate) (bool, error) {
	return r.choosable(ctx, question, cs)
}

// Decide is the ladder's decision, given what each rung found: the question's
// own scope wins first, then the margin, then a manifest dependency, then the
// judge's answer, and last whether the reader's role can answer the card at
// all. Neither of the last two is ever defaulted.
//
// It is a pure function of the rungs precisely so that it can be the ONLY
// place this policy is written down. Route calls it, and so does the eval
// harness's margin sweep, which needs to re-decide at six margins from rungs
// it paid for once. Before this existed the harness carried its own copy,
// which meant a change to the rung order here left the harness compiling and
// silently measuring a policy the product no longer ran.
//
// namedRepos is how many repositories the question named that the index
// actually carries. Two or more means the reader asked about both, and a card
// offering a choice between them is a question they already answered — worse,
// choosing forecloses the other half, because a resumed turn reads only the
// chosen candidate's hits. This rung is deterministic and sits in front of
// everything else; it is NOT the phase 4c evidence lever, which fed the judge
// a "the question named this repository" signal and was measured twice
// without landing (docs/measurements/2026-08-19-candidates.md). The judge's
// prompt is untouched.
//
// roleCanChoose is whether the reader's role can answer the card the judge's
// "ask" would produce. The judge decides whether the CODE is ambiguous; this
// decides whether the PERSON can resolve it. For the Developer it is always
// true — that reader picks between two packages without effort — so the
// Developer's decision, and every number measured against it, is unchanged.
// The eval harness's sweep is audience-neutral and passes true as well.
func Decide(all []Candidate, margin float64, related, judged bool, namedRepos int, roleCanChoose bool) bool {
	if namedRepos >= 2 {
		return false
	}
	if Dominates(all, margin) {
		return false
	}
	if related {
		return false
	}
	return judged && roleCanChoose
}

// Route runs the ladder: margin, then the manifest, then — only in the rest
// case — the model, twice for the Analyst. Which rungs are RUN is decided
// here, so the common fast path — one candidate clearly ahead — still does no
// database query and no model call; what the run rungs then MEAN is Decide's,
// and only Decide's.
//
// The Analyst's rung needs the card in front of it, so naming runs before the
// decision rather than after it. That is the one piece of work this ordering
// can waste: a card the reader could not have answered is named and then not
// shown. It is a ShortGate call per candidate, it runs concurrently, and it
// is what the gate judges — the reader's view of the ambiguity, not the
// module keys underneath it.
func (r *Router) Route(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, namedRepos []string) (Decision, error) {
	ranked, err := r.Rank(ctx, hits)
	if err != nil {
		return Decision{}, err
	}
	// Asked about both, so neither rung below can change the outcome and
	// neither is worth a query or a model call.
	if len(namedRepos) >= 2 {
		return Decision{Ask: false, Candidates: ranked.All}, nil
	}
	if Dominates(ranked.All, r.margin) {
		return Decision{Ask: false, Candidates: ranked.All}, nil
	}
	cs := ranked.Capped

	related, err := r.Related(ctx, cs)
	if err != nil {
		return Decision{}, err
	}
	judged := false
	if !related {
		// The judge is the first paid rung: a manifest dependency has already
		// settled the question without it.
		judged, err = r.Judge(ctx, question, cs)
		if err != nil {
			return Decision{}, err
		}
	}

	// Nothing below can turn a "no" into a card, so a decision already made
	// pays for neither naming nor the role gate.
	if !Decide(ranked.All, r.margin, related, judged, len(namedRepos), true) {
		return Decision{Ask: false, Candidates: cs}, nil
	}

	named, allNamed, err := r.name(ctx, question, audience, lang, cs)
	if err != nil {
		return Decision{}, err
	}

	roleCanChoose := true
	if audience != AudienceDev {
		// A candidate whose naming call failed still carries its module key as
		// a title — a directory path, which is the one thing an Analyst's card
		// must never show. That is settled without a model call.
		if !allNamed {
			roleCanChoose = false
		} else {
			roleCanChoose, err = r.Choosable(ctx, question, named)
			if err != nil {
				return Decision{}, err
			}
		}
	}
	if !Decide(ranked.All, r.margin, related, judged, len(namedRepos), roleCanChoose) {
		// The turn goes on to answer, and gathering starts from ALL hits, not
		// from these candidates (pipeline.Run) — so what travels back is the
		// same "not asking" every other rung returns. The named copies are
		// carried rather than dropped only because a caller that reproduces
		// the ladder reads Candidates on both branches.
		return Decision{Ask: false, Candidates: named}, nil
	}
	return Decision{Ask: true, Candidates: named}, nil
}

// moduleLookup builds the moduleOf closure candidates() needs, from a single
// Cluster call per distinct repository in hits. A path the cluster does not
// know (because indexing never wrote it, or because the cluster query found
// nothing for the repo at all) falls back to its own directory.
func (r *Router) moduleLookup(ctx context.Context, hits []retrieve.Hit) (func(repo, path string) string, error) {
	byRepo := map[string]map[string]string{}
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.Repo] {
			continue
		}
		seen[h.Repo] = true
		mods, err := modules.Cluster(ctx, r.db, h.Repo, r.clusterOpts)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", h.Repo, err)
		}
		paths := map[string]string{}
		for _, m := range mods {
			for _, p := range m.Paths {
				paths[p] = m.Key
			}
		}
		byRepo[h.Repo] = paths
	}
	return func(repo, path string) string {
		if key, ok := byRepo[repo][path]; ok {
			return key
		}
		if i := lastSlash(path); i >= 0 {
			return path[:i]
		}
		return "."
	}, nil
}

// anyDependency reports whether any ordered pair of distinct repositories
// among the candidates is joined by a manifest edge. This is a hard signal:
// when one repo requires what another publishes, the two are parts of one
// mechanism, and no model needs to be asked.
func (r *Router) anyDependency(ctx context.Context, cs []Candidate) (bool, error) {
	for i, a := range cs {
		for j, b := range cs {
			if i == j || a.Repo == b.Repo {
				continue
			}
			ok, err := repodeps.DependsOn(ctx, r.db, a.Repo, b.Repo)
			if err != nil {
				return false, fmt.Errorf("depends on %s -> %s: %w", a.Repo, b.Repo, err)
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// judgeDecision is the shape of the judge's reply.
type judgeDecision struct {
	Decision string `json:"decision"`
}

// judge asks the model whether the candidates are alternatives or parts of one
// mechanism. A reply that fails to decode means ask, never a crash: asking
// costs the reader one click, silently composing unrelated mechanisms does
// not recover.
//
// Phase 4c tried showing it more — the repository the question named, each
// candidate's share of the hits, which expected identifiers landed where —
// and measured it twice without being able to show a gain; the loose version
// cost the ambiguous cohort 7/12 to 4/12, and the strict version reproduced
// the baseline exactly. The evidence is a real lever on the unambiguous
// cohort and the threshold is the unsolved part; that is written up rather
// than left half-wired here.
func (r *Router) judge(ctx context.Context, question string, cs []Candidate) (bool, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nCandidates:\n", question)
	for i, c := range cs {
		fmt.Fprintf(&b, "%d. Repository %s, module %s\n", i+1, c.Repo, c.ModuleKey)
		for _, h := range firstN(c.Hits, 2) {
			excerpt := excerptOf(h.RawText, 200)
			fmt.Fprintf(&b, "   - %s: %s\n", h.Path, excerpt)
		}
	}

	opts := []llm.Option{llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(routeMaxTokens), llm.WithStep("route")}
	if r.judgeDeployment != nil {
		opts = append(opts, r.judgeDeployment)
	}
	out, _, err := r.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: judgeSystem},
		{Role: "user", Content: b.String()},
	}, opts...)
	if err != nil {
		return false, fmt.Errorf("judge candidates: %w", err)
	}

	var got judgeDecision
	if err := json.Unmarshal([]byte(stripFence(out)), &got); err != nil {
		// Not a crash and not a silent compose: a decision that could not be
		// read is treated exactly like "ask", the safe side of the ladder.
		return true, nil
	}
	return got.Decision != "compose", nil
}

// choosableDecision is the shape of the role gate's reply.
type choosableDecision struct {
	Decision string `json:"decision"`
}

// choosable asks whether the card just named is one the Analyst can answer. It
// sees the titles and summaries, not the code: that is the reader's view of
// the ambiguity, and a choice that cannot be made from it cannot be made.
//
// A reply that fails to decode means compose, the mirror of judge's fallback:
// there the safe side is asking, because a composed answer across independent
// mechanisms is wrong; here it is answering, because a question the reader
// cannot answer ends the turn with nothing at all.
func (r *Router) choosable(ctx context.Context, question string, cs []Candidate) (bool, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nOptions:\n", question)
	for i, c := range cs {
		fmt.Fprintf(&b, "%d. %s - %s\n", i+1, c.Title, c.Summary)
	}

	out, _, err := r.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: choosableSystem},
		{Role: "user", Content: b.String()},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature),
		llm.WithMaxTokens(routeMaxTokens), llm.WithStep("route"))
	if err != nil {
		return false, fmt.Errorf("judge whether the role can choose: %w", err)
	}

	var got choosableDecision
	if err := json.Unmarshal([]byte(stripFence(out)), &got); err != nil {
		return false, nil
	}
	return got.Decision == "choose", nil
}

// nameResult is the shape of a naming reply.
type nameResult struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// name runs one Complete per candidate concurrently, each seeing that
// candidate's top three hits. A candidate whose naming call fails keeps its
// module key as the title and an empty summary rather than failing the turn;
// the second return value is false when that happened to any of them, which
// is what lets the Analyst's rung refuse a card carrying a path.
//
// The Analyst's prompt carries nameBA, the Developer's is unchanged.
func (r *Router) name(ctx context.Context, question string, audience Audience, lang Language, cs []Candidate) ([]Candidate, bool, error) {
	name := languageName(lang)
	system := fmt.Sprintf(nameSystem, name)
	if audience != AudienceDev {
		system += nameBA + fmt.Sprintf(nameLanguage, name)
	}
	system += languageStyle(lang)
	named := make([]Candidate, len(cs))
	copy(named, cs)
	ok := make([]bool, len(cs))

	var wg sync.WaitGroup
	for i := range named {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := &named[i]
			c.Title = c.ModuleKey
			c.Summary = ""

			var b strings.Builder
			fmt.Fprintf(&b, "Question: %s\n\nCandidate: repository %s, module %s\n", question, c.Repo, c.ModuleKey)
			for _, h := range firstN(c.Hits, 3) {
				excerpt := excerptOf(h.RawText, 200)
				fmt.Fprintf(&b, "- %s: %s\n", h.Path, excerpt)
			}

			out, _, err := r.llm.Complete(ctx, []llm.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: b.String()},
			}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(nameMaxTokens), llm.WithStep("name"))
			if err != nil {
				return
			}
			var got nameResult
			if err := json.Unmarshal([]byte(stripFence(out)), &got); err != nil {
				return
			}
			if got.Title != "" {
				c.Title = got.Title
				ok[i] = true
			}
			c.Summary = got.Summary
		}(i)
	}
	wg.Wait()

	allNamed := true
	for _, ok := range ok {
		if !ok {
			allNamed = false
			break
		}
	}
	return named, allNamed, nil
}

// firstN returns at most n hits.
func firstN(hits []retrieve.Hit, n int) []retrieve.Hit {
	if len(hits) <= n {
		return hits
	}
	return hits[:n]
}

// excerptOf trims raw chunk text to a short excerpt, so a judge or naming
// prompt does not carry whole files.
//
// The bound is in bytes and the cut is on a rune boundary. Cutting at the
// byte offset alone splits any multi-byte rune that straddles it — routine in
// this corpus, whose comments are German — and JSON encoding then replaces
// the half rune with U+FFFD on the way to the model.
func excerptOf(s string, n int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
