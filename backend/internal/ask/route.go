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
// a candidate far behind the leader, and a module that is nothing but
// supporting material — test code, or documentation about the mechanism rather
// than the mechanism. The leader itself is always kept — everything scoring
// badly is not the same as nothing being the best, and a floor that can empty
// the list would turn a weak answer into no answer at all.
//
// This decides what may be OFFERED, never what may be read: Gather still
// starts from every hit (see Pipeline.Run), so a test or a README that is
// genuinely the answer is still quoted and cited.
func worthOffering(cs []Candidate) []Candidate {
	if len(cs) == 0 {
		return cs
	}
	lead := cs[0].Score
	var kept, keptWithSupporting []Candidate
	for _, c := range cs {
		if lead > 0 && c.Score < lead*candidateFloor {
			continue
		}
		keptWithSupporting = append(keptWithSupporting, c)
		if !onlySupporting(c.Hits) {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		// Everything that cleared the floor was test material or
		// documentation. That is still the best the corpus has, and someone
		// who asked how a thing is TESTED, or what the README says about it,
		// is entitled to it — including the card, when two repositories put
		// the same thing in two places. Returning the leader alone here would
		// silently answer from one of them: dominates() cannot ask about a
		// list of one.
		return keptWithSupporting
	}
	return kept
}

// onlySupporting reports whether every hit that put a candidate on the list is
// supporting material rather than the mechanism: test code or documentation. A
// module that mixes either with real code stays — dropping it would lose the
// mechanism in order to keep out its harness or its README.
//
// Test and documentation are one predicate rather than two, because
// worthOffering treats them identically and a second fallback tier would have
// to answer "is a README a better candidate than a test?", which is not a
// question the reader is ever asked.
func onlySupporting(hits []retrieve.Hit) bool {
	if len(hits) == 0 {
		return false
	}
	for _, h := range hits {
		if !retrieve.IsTestPath(h.Path) && !retrieve.IsDocPath(h.Path) {
			return false
		}
	}
	return true
}

// distinctRepos counts the repositories the candidates come from.
func distinctRepos(cs []Candidate) int {
	seen := map[string]bool{}
	for _, c := range cs {
		seen[c.Repo] = true
	}
	return len(seen)
}

// SpansRepos reports whether the repository rung is live: the question named
// no repository, and the candidates come from more than one. Exported for the
// eval harness, which has to pay for Related on exactly the turns Route pays
// for it on — its own bookkeeping is keyed off the margin, and this rung is
// not.
func SpansRepos(all []Candidate, namedRepos int) bool {
	return namedRepos == 0 && distinctRepos(all) >= 2
}

// RepoCandidates is the repository-grained regrouping Route asks Related
// about once SpansRepos is true. Exported for the same reason: the harness
// must query the same set, or it measures a manifest edge the product sees and
// it does not.
func RepoCandidates(cs []Candidate) []Candidate {
	return repoCandidates(cs)
}

// repoCandidates regroups module candidates into one entry per repository,
// best first, for the card that asks WHICH REPOSITORY was meant.
//
// It takes the candidates worthOffering already kept, so the floor and the
// test-only drop have run once and are not applied a second time at a
// different granularity. ModuleKey is left empty on purpose: that is what
// tells a resumed turn this card offered repositories rather than modules,
// and it needs no column, no flag and no migration to say so.
//
// Hits are the union of the repository's modules, sorted best first. The
// naming call reads only the first few, and unsorted they would be the first
// module's hits rather than the repository's best ones.
//
// It does NOT cap. The cap belongs to the card, and applying it here would
// hide repositories from the manifest-dependency check: five repositories
// where only the fifth depends on the first would be asked about as four, the
// edge would go unseen, and the turn would card where AGENTS.md says compose.
// Route caps with capRepoCandidates, after Related has seen every one.
func repoCandidates(cs []Candidate) []Candidate {
	index := map[string]int{}
	var out []Candidate
	for _, c := range cs {
		i, ok := index[c.Repo]
		if !ok {
			out = append(out, Candidate{Repo: c.Repo, Branch: c.Branch})
			i = len(out) - 1
			index[c.Repo] = i
		}
		out[i].Hits = append(out[i].Hits, c.Hits...)
		if c.Score > out[i].Score {
			out[i].Score = c.Score
		}
	}
	for i := range out {
		hits := out[i].Hits
		sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// capRepoCandidates cuts the regrouping to what a card may show. Separate from
// repoCandidates so the manifest-dependency check runs over every repository
// and only the question put to the reader is shortened.
func capRepoCandidates(cs []Candidate) []Candidate {
	if len(cs) > maxRepoCandidates {
		return cs[:maxRepoCandidates]
	}
	return cs
}

// withAllRepos appends the card's last entry: the reader saying they meant
// every repository after all. It is an ordinary candidate row with an empty
// Repo — not a sentinel on the wire — so the range check, the stored
// from_candidate_idx and the answer-once record model all keep working
// untouched.
func withAllRepos(cs []Candidate, lang Language) []Candidate {
	title, summary := AllReposChoice(lang)
	return append(cs, Candidate{Title: title, Summary: summary})
}

// lastSlash finds the final path separator, so a directory can be taken as a
// module key without pulling in path/filepath's OS-specific behaviour.
func lastSlash(p string) int {
	return strings.LastIndexByte(p, '/')
}

// maxCandidates is how many the card may offer. The spec says two to five;
// more than five is not a question anyone answers, it is a list.
const maxCandidates = 5

// maxRepoCandidates is how many repositories a repository card may offer. One
// less than maxCandidates: the "all repositories" entry is appended after
// them, so the card still never shows more than five buttons.
const maxRepoCandidates = 4

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

// Asked is the question's own scope: what the reader said about which system
// they meant, before any rung looked at a score, a manifest or a model. The
// three travel together because they are one thing — the reader's words — and
// they are the rungs Decide reads first.
type Asked struct {
	// NamedRepos is how many repositories the question named that the index
	// actually carries.
	NamedRepos int
	// AllRepos is the reader asking for the whole corpus without naming any
	// of it.
	AllRepos bool
	// WholeSystem is the reader asking about a system entire rather than
	// about a mechanism inside it.
	WholeSystem bool
}

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
// ladder is ordered to avoid on the common path: Route calls it when Dominates
// has said no, OR when the repository rung is live (SpansRepos), because that
// rung sits below composition and above the margin. Callers that reproduce the
// ladder outside Route (the eval harness's margin sweep) must keep BOTH
// conditions — paying for it on the margin alone measures a card where the
// product composes, and calling it unconditionally is the regression this
// method's separation from Rank exists to prevent.
//
// What is passed matters as much as when. On the repository rung it takes
// RepoCandidates over the UNCAPPED ranking: a capped list can leave out the
// one repository holding the manifest edge.
//
// There is a third case in which Route does not pay for it at all: a
// whole-system question inside one repository returns above this, the same
// way a dominant margin does. Both answers would have been compose, so the
// ordering is invisible from the outside, and the harness never meets the
// case — it passes a zero Asked and measures the module rungs.
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

// Decide is the ladder's decision, given what each rung found: the scope the
// question named wins first, then a manifest dependency, then the repository
// rung, then the scope the question meant entire, then the margin, then the
// judge's answer, and last whether the reader's role can answer the card at
// all. Neither of the last two is ever defaulted.
//
// The reader's own words are read in two places rather than one, and the
// repository rung is why: Asked.NamedRepos and Asked.AllRepos say which
// system, so nothing below them can matter, while Asked.WholeSystem says only
// that no part of one was meant — a question that says it about no named
// repository still has to be asked which repository.
//
// It is a pure function of the rungs precisely so that it can be the ONLY
// place this policy is written down. Route calls it, and so does the eval
// harness's margin sweep, which needs to re-decide at six margins from rungs
// it paid for once. Before this existed the harness carried its own copy,
// which meant a change to the rung order here left the harness compiling and
// silently measuring a policy the product no longer ran.
//
// Asked.NamedRepos is how many repositories the question named that the index
// actually carries. Two or more means the reader asked about both, and a card
// offering a choice between them is a question they already answered — worse,
// choosing forecloses the other half, because a resumed turn reads only the
// chosen candidate's hits. This rung is deterministic and sits in front of
// everything else; it is NOT the phase 4c evidence lever, which fed the judge
// a "the question named this repository" signal and was measured twice
// without landing (docs/measurements/2026-08-19-candidates.md). The judge's
// prompt is untouched.
//
// Asked.AllRepos is the reader having asked for the whole corpus. Like
// Asked.NamedRepos it is the question's own scope, and it says the same thing
// from the other side: the reader already decided, so there is nothing to ask.
//
// Asked.WholeSystem is the third shape those words come in, and the one the
// card had no answer for: the reader meant a system entire, not a mechanism
// inside it. Every candidate is then a part of what was asked about, and a
// card offering parts asks a question that was already answered. Neither
// model rung below can see this. The judge is asked whether the CODE is
// ambiguous, and the parts of one product are independent mechanisms by any
// reading of it; the role gate reads titles the naming call wrote with the
// question in front of it, so a product-level question comes back as five
// product-level topics and looks maximally tellable apart. It sits BELOW the
// repository rung on purpose: "as a whole" says the reader meant no part of
// something, never which something they meant, so a whole-system question
// naming no repository is still asked which one.
//
// The repository rung — no repository named, candidates spanning two or more —
// is deterministic and sits ABOVE the margin, not below it. A leader clear of
// the runner-up says which module scored best, never which repository the
// reader meant, and the corpus is a family of similar systems: pricing,
// usage and token accounting exist in several of them, and a margin that
// dominates picks one at random from the reader's point of view. The judge is
// not asked either — "same mechanism in two products" is exactly what it
// answers "compose" to, and composing across repositories the reader did not
// name is the failure this rung exists to stop.
//
// It sits BELOW the repo_deps rung, and that ordering is the whole of the
// exception: repositories joined by a manifest edge are one mechanism, and
// AGENTS.md has said so since phase 4. loom requiring what peeq publishes is
// composition, not a choice.
//
// roleCanChoose is whether the reader's role can answer the card the judge's
// "ask" would produce. The judge decides whether the CODE is ambiguous; this
// decides whether the PERSON can resolve it. For the Developer it is always
// true — that reader picks between two packages without effort — so the
// Developer's decision, and every number measured against it, is unchanged.
// The eval harness's sweep is audience-neutral and passes true as well.
//
// It gates the JUDGE's card only, never the repository rung above it. That
// rung's options are products, which is the one thing an Analyst can always
// tell apart — the gate exists for options separable only by implementation,
// layer or package, and a repository is none of those. The card also carries
// "all repositories", so the reader who does not care always has an answer.
// Refusing it would put the Analyst back on exactly the cross-repository
// answer this rung exists to stop, which is the opposite of what the gate is
// for.
func Decide(all []Candidate, margin float64, related, judged bool, asked Asked, roleCanChoose bool) bool {
	if asked.NamedRepos >= 2 {
		return false
	}
	if asked.AllRepos {
		return false
	}
	if related {
		return false
	}
	if asked.NamedRepos == 0 && distinctRepos(all) >= 2 {
		return true
	}
	if asked.WholeSystem {
		return false
	}
	if Dominates(all, margin) {
		return false
	}
	return judged && roleCanChoose
}

// Route runs the ladder: the question's own scope, the manifest, the
// repository rung, the margin, the reader having meant a system entire, then
// — only in the rest case — the model, twice for the Analyst. Which rungs are RUN is decided here, so the common fast
// path — one candidate clearly ahead inside one repository — still does no
// database query and no model call; what the run rungs then MEAN is Decide's,
// and only Decide's.
//
// The Analyst's rung needs the card in front of it, so naming runs before the
// decision rather than after it. That is the one piece of work this ordering
// can waste: a card the reader could not have answered is named and then not
// shown. It is a ShortGate call per candidate, it runs concurrently, and it
// is what the gate judges — the reader's view of the ambiguity, not the
// module keys underneath it. A repository card skips the gate entirely, so it
// is never named twice over.
func (r *Router) Route(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, asked Asked) (Decision, error) {
	ranked, err := r.Rank(ctx, hits)
	if err != nil {
		return Decision{}, err
	}
	// Asked about both, or asked for all of them: the reader has already
	// answered the only question a card could put to them, so no rung below
	// can change the outcome and none is worth a query or a model call.
	if asked.NamedRepos >= 2 || asked.AllRepos {
		return Decision{Ask: false, Candidates: ranked.All}, nil
	}

	// The repository rung is live when the question named no repository and
	// the candidates span more than one. It is the only reason Related is
	// worth paying for on an otherwise dominant turn, so it is computed before
	// the margin short-circuit rather than after it.
	spans := asked.NamedRepos == 0 && distinctRepos(ranked.All) >= 2
	if !spans && Dominates(ranked.All, r.margin) {
		// The fast path is unchanged: hits inside one repository with a clear
		// leader still reach an answer without a query or a model call.
		return Decision{Ask: false, Candidates: ranked.All}, nil
	}
	if !spans && asked.WholeSystem {
		// The reader meant the system entire, and which system is settled —
		// so nothing below can turn this into a card, exactly as on the
		// margin's line above. Returning here is what keeps the judge, the
		// naming and the role gate unpaid on a question none of them can
		// read correctly.
		return Decision{Ask: false, Candidates: ranked.All}, nil
	}

	// Over EVERY repository when the repository rung is live, not over the
	// capped module list and not over the capped repository list either. The
	// top five modules can sit in two repositories while a third repository's
	// best module ranks sixth, and the fifth repository can be the only one
	// with an edge to the first: a capped list misses that edge and cards
	// where AGENTS.md says compose. anyDependency skips same-repo pairs, so
	// one entry per repository is fewer queries than the module list, not
	// more. The cap is applied to the card alone, below.
	repos := repoCandidates(ranked.All)
	cs := ranked.Capped
	depsOver := cs
	if spans {
		depsOver = repos
	}
	related, err := r.Related(ctx, depsOver)
	if err != nil {
		return Decision{}, err
	}
	judged := false
	if !related && !spans && !Dominates(ranked.All, r.margin) {
		// The judge is the first paid rung, and a manifest dependency, the
		// repository rung above it, or a dominant margin have each already
		// settled the question without it.
		judged, err = r.Judge(ctx, question, cs)
		if err != nil {
			return Decision{}, err
		}
	}

	// Nothing below can turn a "no" into a card, so a decision already made
	// pays for neither naming nor the role gate.
	if !Decide(ranked.All, r.margin, related, judged, asked, true) {
		return Decision{Ask: false, Candidates: cs}, nil
	}

	// A repository card is settled: the rung is deterministic, and the role
	// gate does not apply to it (see Decide). Name the repositories and ask.
	if spans {
		named, _, err := r.name(ctx, question, audience, lang, capRepoCandidates(repos))
		if err != nil {
			return Decision{}, err
		}
		return Decision{Ask: true, Candidates: withAllRepos(named, lang)}, nil
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
	if !Decide(ranked.All, r.margin, related, judged, asked, roleCanChoose) {
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
			// A naming call that fails leaves something readable behind: the
			// module key, or for a repository entry the repository itself.
			c.Title = c.ModuleKey
			if c.Title == "" {
				c.Title = c.Repo
			}
			c.Summary = ""

			var b strings.Builder
			if c.ModuleKey == "" {
				// A repository entry: there is no module to name, and saying
				// "module " with nothing after it reads as a missing value.
				fmt.Fprintf(&b, "Question: %s\n\nCandidate: repository %s\n", question, c.Repo)
			} else {
				fmt.Fprintf(&b, "Question: %s\n\nCandidate: repository %s, module %s\n", question, c.Repo, c.ModuleKey)
			}
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
