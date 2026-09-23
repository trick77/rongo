package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/stages"
	"github.com/trick77/rongo/internal/units"
)

// searchK is how many fused hits the gatherer starts from. Deeper than the ten
// a person would read, because the reference walk uses the tail: a handler that
// ranks twelfth is still the thread that leads to the service.
const searchK = 20

// comparisonK caps what a comparison turn carries out of retrieval, however
// many repositories the question named: two sides at full depth, and no more.
const comparisonK = 2 * searchK

// Searcher is the retrieval half. An interface so the pipeline can be tested
// without an embedding endpoint.
type Searcher interface {
	Search(ctx context.Context, q retrieve.Query) ([]retrieve.Hit, error)
	// ResolveRepos splits the understanding's guessed repository names into
	// the ones the index carries and the ones it does not.
	ResolveRepos(ctx context.Context, want []string, question string) (known, unknown []string, err error)
	// Substring scans the raw source for one literal term, for the locate
	// loop's grep tool: it tells the model that nothing found means the
	// spelling was wrong, so the scan has to be of the text it asked for.
	Substring(ctx context.Context, term string, n int, repos []string, question string, stage retrieve.StagePrefixes) ([]retrieve.Hit, error)
}

// Routes decides whether a turn can be answered from the gathered hits or
// must ask the reader to choose among candidates. An interface — satisfied by
// *Router — so the pipeline can be tested without a database or a model.
type Routes interface {
	Route(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, namedRepos []string, allRepos bool) (Decision, error)
	// Projects is the declared grouping, read fresh per turn. It sits on this
	// interface rather than on a second dependency because the router already
	// owns the database handle the pipeline would otherwise need for nothing
	// else.
	Projects(ctx context.Context) (projects.Map, error)
	// Units is what one repository is built from, as its manifests declare
	// it. Same owner as Projects, for the same reason.
	Units(ctx context.Context, repo string) ([]units.Unit, []units.Dep, error)
	// Stages is every declared deployment stage of every enabled repository,
	// read fresh per turn like Projects. Same owner, same reason.
	Stages(ctx context.Context) (stages.Set, error)
}

// Clarification is how a turn ends when it asks instead of answering. The
// understanding travels with it: a resumed turn that re-derives its own
// search terms can search differently and answer from material the card
// never showed.
type Clarification struct {
	Understanding Understanding
	Candidates    []Candidate
	// Scope is what the question said about repositories. A turn that ends by
	// asking has one too, and it is part of the record: the candidates on the
	// card come from a corpus the named repository may have been missing from.
	Scope Scope
	// TooBroad is the turn having asked for a narrower question instead of
	// asking which candidate was meant. The candidates are then every
	// repository that matched, one row each, named by the index rather than
	// by a model.
	TooBroad bool
}

// Events is how a caller watches a turn. Both may be nil.
type Events struct {
	// OnStatus reports which step is running, for the UI to show something
	// while the slow part happens.
	OnStatus func(step string)
	// OnToken receives the answer as it is written.
	OnToken func(tok string)
	// OnNotice reports what the turn has to say about its own scope. Called
	// before the search, at most once, and not at all when there is nothing
	// to say — which is every ordinary turn.
	OnNotice func(text string)
	// OnDetail reports what a step found, once the step is done: the
	// understanding's terms and scope, hits per repository, the routing rung,
	// the sources by reason, the answer's usage. Facts the pipeline held
	// anyway and used to log for nobody; the trace draws them under the step.
	OnDetail func(step string, detail map[string]any)
	// OnMemory writes the standing instruction the understanding read out of
	// the question and reports what that left behind. The pipeline has no
	// reader and no store; the handler has both. Nil means nothing is
	// written, and a question that was only an instruction runs as a
	// question.
	OnMemory func(d memory.Directive) (memory.Added, error)
}

func (e Events) detail(step string, d map[string]any) {
	if e.OnDetail != nil && len(d) > 0 {
		e.OnDetail(step, d)
	}
}

func (e Events) notice(text string) {
	if text != "" && e.OnNotice != nil {
		e.OnNotice(text)
	}
}

func (e Events) status(step string) {
	if e.OnStatus != nil {
		e.OnStatus(step)
	}
}

// tokens wraps OnToken so the first token that arrives reports "writing".
//
// "answering" is reported before the model is called, and on a reasoning model
// the wait between the two is most of the turn — the reader was told the answer
// was being written while nothing was being written yet. Splitting the step in
// two makes the trace honest and puts a number on each half: how long it thought
// and how long it wrote.
func (e Events) tokens() func(string) {
	first := true
	return func(tok string) {
		if first {
			first = false
			e.status("writing")
		}
		if e.OnToken != nil {
			e.OnToken(tok)
		}
	}
}

// Pipeline runs a question end to end: understand, search, route, gather,
// answer.
type Pipeline struct {
	understander *Understander
	search       Searcher
	router       Routes
	gatherer     *Gatherer
	answerer     *Answerer
	// models is optional: without it a turn answers with no process listing,
	// which is every turn before process models were read.
	models Models
	// history is the commit lane, optional; see WithHistory.
	history Histories
	now     func() time.Time
	// releases is the checkout side of a release turn, optional; see
	// WithReleases.
	releases Releaser
}

// WithModels gives the pipeline the index's process models, so a turn whose
// sources include a BPMN file can tell the answer how that process is wired.
func (p *Pipeline) WithModels(m Models) *Pipeline {
	p.models = m
	return p
}

// NewPipeline wires the steps.
func NewPipeline(c *llm.Client, s Searcher, g *Gatherer, r Routes) *Pipeline {
	return &Pipeline{
		understander: NewUnderstander(c),
		search:       s,
		router:       r,
		gatherer:     g,
		answerer:     NewAnswerer(c),
	}
}

// Thread is what earlier turns of a conversation left behind, and the whole of
// what a later turn inherits from them. Zero is the first turn of a thread,
// which inherits nothing and behaves exactly as every turn did before this
// existed.
//
// It is two different things on purpose, because they are read by different
// steps and answer different questions:
//
//   - Pin is WHERE the turn may look. A ceiling on the repositories, applied
//     after the question has been understood, and the reason a follow-up is
//     never asked which repository was meant.
//   - Question and Answer are WHAT the turn is about. They reach the
//     understanding step and nothing else, so that "show me that as a diagram"
//     resolves to the subject the reader is following up on instead of being
//     searched for as the word "diagram".
//
// Answer is the previous answer's TEXT, and it is deliberately kept out of the
// answering prompt of an ordinary follow-up: sources are the truth, and a
// model handed its own earlier prose beside sources it was NOT written from
// ends up citing them for it. Only the previous QUESTION goes there, as the
// thing a pronoun points at.
//
// The one turn that does read the answer is a rework ("summarize", "as a
// table"): the answer IS what that turn is about, and it is handed over
// together with Sources, the material it was written from, so every claim in
// the reworked text still has its source in front of the model.
type Thread struct {
	// Pin is the repositories the thread has already narrowed to.
	Pin []string
	// Question is the last question this thread asked and got an answer to.
	Question string
	// Answer is the answer that question got.
	Answer string
	// Sources is what Answer was written from, as the record resolves them
	// now, and SourcesTotal is how many the record holds. A re-index between
	// the turns drops chunks from Sources and not from SourcesTotal, which
	// is how a rework tells a whole basis from a partial one.
	Sources      []Source
	SourcesTotal int
}

// Run answers one question, or ends the turn by asking which of several
// independent candidates was meant. Exactly one of the returned Answer and
// *Clarification is meaningful: a non-nil Clarification means the turn ended
// with a question, and the Answer is the zero value.
//
// A turn that finds nothing ends with "nothing found" AND the terms that were
// tried, never with an answer assembled from whatever was in context. Naming
// the terms is the difference between a dead end someone can act on — the
// vocabulary was wrong, ask differently — and a shrug.
//
// t is what earlier turns of this thread left behind, zero for the first one.
func (p *Pipeline) Run(ctx context.Context, question string, audience Audience, lang Language, t Thread, ev Events) (Answer, *Clarification, error) {
	pin := t.Pin
	ev.status("understanding")
	declared := p.declaredStages(ctx)
	u, err := p.understander.Understand(ctx, question, t, declared.Names())
	if err != nil {
		return Answer{}, nil, err
	}
	// A rework the guard refuses is an ordinary question from here on, in
	// the record too: the intent rides the scope onto the row, and a
	// re-explain of that row keys on it to rework again.
	if u.Intent == IntentRework && !isRework(u, t) {
		u.Intent = ""
	}
	// The instruction is written before anything else runs, so the answer
	// of this same turn is already under it. A turn that was ONLY the
	// instruction ends here, templated: there is nothing to search for. One
	// the understanding called a memory but that carried nothing to keep
	// runs as the ordinary question it must then have been. A write that
	// failed beside a question is the trace's to report, not the turn's to
	// die of; alone, it is the whole turn.
	remembered, memErr := p.remember(ctx, u, ev)
	if u.Intent == IntentMemory {
		switch {
		case memErr != nil && !errors.Is(memErr, memory.ErrFull):
			return Answer{}, nil, memErr
		case remembered == nil && memErr == nil:
			u.Intent = ""
		default:
			return answerMemory(lang, remembered, memErr), nil, nil
		}
	}
	// The stage before the repositories: a reader writing "in production"
	// gets it guessed as a repository name as often as not, and unresolved
	// it would become a false "no project called production in the index".
	stage, stageDetail := resolveStage(question, u.Stage, declared)
	u.Repos = withoutStageWords(u.Repos, declared)

	known, unknown, err := p.search.ResolveRepos(ctx, u.Repos, question)
	if err != nil {
		return Answer{}, nil, fmt.Errorf("resolve the named repositories: %w", err)
	}
	// The thread's own narrowing wins over anything this question says about
	// repositories, in one direction only: it can take repositories away, it
	// can never add one. A follow-up names nothing because the reader already
	// named it a turn ago, and answering it across the corpus — or asking
	// which repository was meant — throws away the one thing the thread had
	// established. AllRepos goes with it: "in all repos" is a widening, and a
	// thread does not widen.
	outside := outsideThePin(known, pin)
	all := u.AllRepos
	allDenied := false
	if len(pin) > 0 {
		// The pin comes off a stored row, and between the turn that wrote it
		// and this one the repository can leave repos.yaml or be renamed. A
		// restriction the index cannot resolve is not a narrow search, it is no
		// search at all: knownRepos drops a name it does not carry and an empty
		// restriction means the whole corpus, so the turn would answer from
		// everything while the notice, the pills and the record all said one
		// repository. Same reasoning as ResumeRepo's, and the same outcome — the
		// turn fails rather than substituting a corpus for a thread.
		live, _, err := p.search.ResolveRepos(ctx, pin, "")
		if err != nil {
			return Answer{}, nil, fmt.Errorf("resolve the thread's repositories: %w", err)
		}
		if len(live) == 0 {
			return Answer{}, nil, fmt.Errorf("this thread is about %s, which the index no longer carries",
				strings.Join(pin, ", "))
		}
		pin = live
		// Narrowing, not replacing. A thread pinned to two repositories by a
		// comparison is still a thread, and "and in rongo?" inside it has to
		// reach rongo alone. Naming nothing the pin holds — the ordinary
		// follow-up, and the one that named a repository the thread left
		// behind — falls back to the whole pin, because the alternative is an
		// empty restriction, which means the whole corpus.
		if narrowed := intersect(known, pin); len(narrowed) > 0 {
			known = narrowed
		} else {
			known = pin
		}
		// "In allen Repositories" is a widening, and a thread does not widen.
		// Refusing it is right; refusing it silently is not — the question
		// asked for the whole corpus and the answer will cover one thread's
		// worth, which is exactly the kind of quiet substitution Scope.Outside
		// exists to stop. Recorded so the reader is told and the model is
		// forbidden to fill the gap from its own training.
		allDenied = all
		all = false
	}
	// The intent travels in the scope rather than as an argument of its own:
	// it is part of the record, so a resumed turn reads it off the stored
	// clarification and a re-explained one off the stored row.
	scope := Scope{Known: known, Unknown: unknown, Outside: outside, AllDenied: allDenied, All: all,
		Stage: stage, Intent: u.Intent, Census: u.Census}
	// The rung above routing. A question that names a repository the index does
	// not carry arrives at Route as "named nothing" and cards on the repository
	// rung; without this line the route log reports a question that named no
	// repository, and the reader is certain they named one. The pin is logged
	// beside it for the same reason: under one, "known" is the thread's doing
	// rather than the question's.
	slog.Info("scope", "thread", llm.ThreadID(ctx), "known", known, "unknown", unknown,
		"outside", outside, "pin", pin, "all_repos", all, "all_denied", allDenied, "stage", stage)
	// Sent before the search rather than with the answer: it is already known
	// here, and a turn that goes on to fail or to ask has still told the
	// reader what its scope was.
	ev.notice(ScopeNotice(lang, scope))
	ev.detail("understanding", withStageDetail(understandingDetail(u, scope, pin, t.Sources), stageDetail))

	// A changes question leaves here: its sources are commits, and neither
	// the fused search nor the routing ladder has anything to say about a
	// date window.
	if p.isChanges(u) {
		answer, err := p.answerChanges(ctx, question, audience, lang, u, scope, t.Question, ev)
		return answer, nil, err
	}
	// A release question leaves here for the same reason: its sources are
	// the commits between two deployed versions.
	if p.isRelease(u) {
		answer, err := p.answerRelease(ctx, question, audience, lang, u, scope, t.Question, ev)
		return answer, nil, err
	}
	// A rework leaves here too: the previous answer and its own sources are
	// the whole of what it reads, and a search on "summarize" has nothing to
	// add but a second, different answer.
	if isRework(u, t) {
		answer, err := p.answerRework(ctx, question, audience, lang, t, scope, ev)
		return answer, nil, err
	}

	texts := u.SearchTexts(question)
	scope.words = strings.Join(texts, " ")
	ev.status("searching")
	// The question is left out of a pinned search, for the reason searchScoped's
	// comparison loop leaves it out: knownRepos UNIONS in every indexed
	// repository the question names as a whole word, and that union is what
	// makes a guess unable to exclude what the reader typed. Under a pin it
	// would undo the pin — "und wie macht das loom?" would search loom while
	// the notice said loom was not searched and the prompt said the turn knows
	// nothing about it. The narrowing has to be a fact, not a sentence.
	scopedQuestion := question
	if len(pin) > 0 {
		scopedQuestion = ""
	}
	hits, err := p.searchScoped(ctx, scopedQuestion, u.Prior, texts, u.CodeText(), known, declared.Prefixes(stage))
	if err != nil {
		return Answer{}, nil, fmt.Errorf("search: %w", err)
	}
	ev.detail("searching", searchDetail(hits))

	ev.status("routing")
	d, err := p.router.Route(ctx, question, audience, lang, hits, known, all)
	if err != nil {
		return Answer{}, nil, err
	}
	ev.detail("routing", routingDetail(d))
	if d.Ask {
		// The turn ends here. The understanding travels with it: a resumed
		// turn that re-derives its own terms can search differently and
		// answer from material the card never showed.
		return Answer{}, &Clarification{Understanding: u, Candidates: d.Candidates, Scope: scope, TooBroad: d.TooBroad}, nil
	}

	// Gathering keeps starting from ALL hits, never from a candidate's own
	// subset. The published 0.955 was measured that way; narrowing here would
	// be an unmeasured regression. Routing decides whether to ask, not what
	// to read.
	// Reported without the previous question: "nothing found, searched for:"
	// is what the reader asked this turn, and the thread's older question is
	// search material they did not type here. Naming it would say the turn
	// went looking for something they asked a turn ago.
	answer, err := p.gatherAndAnswer(ctx, question, audience, lang, hits, scope, withoutPrior(texts, u.Prior), t.Question, ev)
	return answer, nil, err
}

// hitRepoNames is the repositories the search hits came from, deduplicated.
func hitRepoNames(hits []retrieve.Hit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		if h.Repo != "" && !seen[h.Repo] {
			seen[h.Repo] = true
			out = append(out, h.Repo)
		}
	}
	return out
}

// hitRepos is the repositories a turn's SEARCH HITS came from, deduplicated
// and sorted. Hop 0 only: a reference walk and a crossing reach files the
// question never asked for, and counting those would name a repository the
// turn merely passed through.
func hitRepos(sources []Source) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sources {
		if s.Hop != 0 || s.Repo == "" || seen[s.Repo] {
			continue
		}
		seen[s.Repo] = true
		out = append(out, s.Repo)
	}
	sort.Strings(out)
	return out
}

// withoutPrior drops the previous-question lane from the list a failed search
// reports back. The lane earns its place in the search, never in the sentence.
// Filtered from index 1 and at most once: texts[0] is the reader's own
// question, and a reader who asks the same thing twice in one thread makes
// the two strings equal. SearchTexts adds no lane in that case, so there is
// nothing here to remove and removing by value would take the reader's own
// question out of the sentence instead.
func withoutPrior(texts []string, prior string) []string {
	if prior = strings.TrimSpace(prior); prior == "" {
		return texts
	}
	for i := 1; i < len(texts); i++ {
		if texts[i] == prior {
			out := make([]string, 0, len(texts)-1)
			out = append(out, texts[:i]...)
			return append(out, texts[i+1:]...)
		}
	}
	return texts
}

// outsideThePin is the repositories the question named, the index carries, and
// the thread does not — the ones a pinned turn will not search after all.
//
// It is not an error and not a mishearing: the name was right and the code is
// indexed. It is the funnel's cost, and it is said out loud above the answer
// (Scope.Outside) for the same reason an unindexed name is: an answer that
// silently dropped half of what was asked about reads exactly like an answer
// that covered it.
func outsideThePin(named, pin []string) []string {
	if len(pin) == 0 || len(named) == 0 {
		return nil
	}
	in := make(map[string]bool, len(pin))
	for _, p := range pin {
		in[p] = true
	}
	var out []string
	for _, n := range named {
		if !in[n] {
			out = append(out, n)
		}
	}
	return out
}

// intersect is the names in both, in the order the question named them — the
// thread narrowing further inside what it already carries.
func intersect(named, pin []string) []string {
	in := make(map[string]bool, len(pin))
	for _, p := range pin {
		in[p] = true
	}
	var out []string
	for _, n := range named {
		if in[n] {
			out = append(out, n)
		}
	}
	return out
}

// describeProjects fills in the two project fields of a scope: which projects
// this turn covers whole, and the structure block describing them.
//
// Only a NARROWED scope gets them. A corpus-wide turn leaves Known empty, and
// filling Projects with every project would hand the answer prompt the
// comparison rule on an ordinary question — "cover every one of them, say
// plainly where they differ" about a corpus the reader never asked to compare.
// The cost is that a corpus-wide question about a single-project corpus gets no
// structure block; the block describes the scope a turn is confined to, and
// that turn is confined to nothing.
//
// A failure here is logged and swallowed. The grouping decides how well an
// answer is phrased, not whether it is correct, and losing a whole turn because
// repo_uses could not be read would be the worse trade.
func (p *Pipeline) describeProjects(ctx context.Context, scope Scope) Scope {
	// The declared stages ride on the scope for the answer: they label a
	// source under a stage directory and narrow the crossing when one was
	// asked. Read here rather than in Run because every entry point — a
	// resume, a re-explain — comes through here and needs them alike.
	scope.Stages = p.declaredStages(ctx)
	if len(scope.Known) == 0 {
		return scope
	}
	pm, err := p.router.Projects(ctx)
	if err != nil {
		slog.Warn("projects unavailable, answering without the structure block",
			"thread", llm.ThreadID(ctx), "err", err)
		return scope
	}
	scope.Projects = pm.Covered(scope.Known)
	var ps []projects.Project
	accounted := map[string]bool{}
	for _, name := range scope.Projects {
		pr, ok := pm.Project(name)
		if !ok {
			continue
		}
		ps = append(ps, pr)
		for _, m := range pr.Members {
			accounted[m.Name] = true
		}
	}
	scope.Loose = nil
	for _, r := range scope.Known {
		if !accounted[r] {
			scope.Loose = append(scope.Loose, r)
		}
	}
	// What each repository in scope is built from: the apps and services a
	// person names, and which uses which. Templated from the manifests the
	// indexer read (internal/units), never a source, closed by the same rule
	// the project block is. A repository of one build adds nothing.
	var parts string
	for _, repo := range scope.Known {
		us, deps, err := p.router.Units(ctx, repo)
		if err != nil {
			slog.Warn("units unavailable, answering without them", "thread", llm.ThreadID(ctx), "repo", repo, "err", err)
			continue
		}
		parts += units.Describe(repo, us, deps)
	}
	scope.Structure = withParts(StructureBlock(ps), parts)
	return scope
}

// declaredStages reads the stage declarations for this turn. Unavailable
// reads as none declared, and says so: a turn then answers without a stage,
// which is the ordinary turn, rather than failing.
func (p *Pipeline) declaredStages(ctx context.Context) stages.Set {
	declared, err := p.router.Stages(ctx)
	if err != nil {
		slog.Warn("stages unavailable, answering without them", "thread", llm.ThreadID(ctx), "err", err)
		return nil
	}
	return declared
}

// resolveStage settles which stage a turn is about, from the reader's own
// words first and the understanding step's field second, and reports how
// for the trace.
//
// The reader's wording wins: a declared stage name or alias in the question
// is the reader narrowing the turn, the same way a named repository is. The
// model's field covers the phrasings no alias can — "the integration
// environment" for intg — and it is only ever one of the declared names, or
// dropped. When the two disagree the turn is NOT narrowed and the trace says
// so: narrowing on a guess against the reader's own words would hand back
// one stage's value as the answer to a question about another.
//
// Two stages named at once is a question about both, and both is what an
// unnarrowed turn gathers.
func resolveStage(question, guessed string, declared stages.Set) (string, map[string]any) {
	detail := map[string]any{}
	named := declared.Mentioned(question)
	model, ok := declared.Resolve(guessed)
	if guessed != "" && !ok {
		detail["stage_dropped"] = guessed
	}
	switch {
	case len(named) >= 2:
		detail["stages_named"] = named
		return "", detail
	case len(named) == 1 && ok && model != named[0]:
		detail["stage_conflict"] = []string{named[0], model}
		return "", detail
	case len(named) == 1:
		return named[0], detail
	case ok:
		return model, detail
	}
	return "", detail
}

// withoutStageWords drops a stage name or alias from the guessed
// repositories: "production" is a stage, and left in it would resolve to no
// repository and be reported as one the index lacks.
func withoutStageWords(repos []string, declared stages.Set) []string {
	var out []string
	for _, r := range repos {
		if _, isStage := declared.Resolve(r); !isStage {
			out = append(out, r)
		}
	}
	return out
}

// withStageDetail folds the stage resolution into the understanding step's
// trace detail.
func withStageDetail(d, stage map[string]any) map[string]any {
	for k, v := range stage {
		d[k] = v
	}
	return d
}

// withParts appends the units paragraphs to a structure block so that the
// never-cite sentence closes the whole block once, last. StructureBlock
// closes its block with that sentence; it is moved behind the paragraphs
// rather than left in the middle, where the parts list would sit outside
// the rule.
func withParts(structure, parts string) string {
	if parts == "" {
		return structure
	}
	return strings.TrimSuffix(structure, structureIsConfiguration) + parts + structureIsConfiguration
}

// gather is the reading step every entry point runs: the walk and the
// crossings, then the gap pass over what they produced, reported as one step
// because a reader is told what was read, not how many lookups it took.
//
// One function rather than two copies: the gap pass has to run on a resumed
// turn as well, and a step a resume skips is a turn answered from less code
// than the same question answered a minute earlier.
func (p *Pipeline) gather(ctx context.Context, question string, hits []retrieve.Hit, scope Scope, ev Events) ([]Source, Scope, error) {
	ev.status("gathering")
	census, err := p.census(ctx, scope)
	if err != nil {
		return nil, scope, err
	}
	scope.Links = census.Listing
	sources, located, err := p.gatherSeeded(ctx, question, hits, scope, census, ev)
	// What the locate loop concluded, carried to the answer prompt. Empty
	// whenever the loop is off or looked at nothing.
	scope.Located = located
	return sources, scope, err
}

// gatherSeeded is the gather under a census that has already been read. It
// returns the sources and, when the locate loop ran, the sentence it wrote
// about what it found: a pointer the answer prompt carries.
func (p *Pipeline) gatherSeeded(ctx context.Context, question string, hits []retrieve.Hit, scope Scope, census Census, ev Events) ([]Source, string, error) {
	stage := scope.Stages.Prefixes(scope.Stage)
	// The turn's ceiling, from what the question named or what its search
	// hit, before anything is gathered: the walk, the crossings, the gap pass
	// and the loop all run under it. See turnCeiling.
	pm, perr := p.router.Projects(ctx)
	if perr != nil {
		slog.Warn("projects unavailable, gathering confined to the repositories named or hit",
			"thread", llm.ThreadID(ctx), "err", perr)
	}
	ceiling := turnCeiling(scope.Known, hitRepoNames(hits), pm)
	words := scope.words
	if words == "" {
		words = question
	}
	g := p.gatherer.forTurn(scope.Resumed).within(ceiling).withTerms(words)
	sources, err := g.GatherSeeded(ctx, hits, census.Landings, stage)
	if err != nil {
		return nil, "", err
	}
	sources, report, err := g.FillGaps(ctx, question, sources, stage)
	if err != nil {
		return nil, "", err
	}
	// The locate loop runs last: it reads what everything before it gathered
	// and looks again for the one place a locate question turns on. Off
	// unless a client was given, and it never fails the turn.
	// The loop's search tools stay narrowed to what the question named or
	// the thread pinned; what it admits stays inside the turn's ceiling.
	searchIn := scope.Known
	if len(searchIn) == 0 {
		searchIn = ceiling
	}
	sources, locate, err := p.gatherer.within(ceiling).Locate(ctx, question, sources, searchIn, scope.Resumed, stage)
	if err != nil {
		return nil, "", err
	}
	ev.detail("gathering", withLocateDetail(
		withCensusDetail(gatherDetail(sources, p.gatherer.opts.TokenBudget, report), census), locate))
	return sources, locate.Found, nil
}

// withLocateDetail adds what the loop did, and only on a turn that ran one:
// the trace is stored per message, and "locate_rounds: 0" on every turn the
// loop is off for would be a record of an absence. Every key is a fact the
// loop already held — no second model call describes it.
func withLocateDetail(d map[string]any, r LocateReport) map[string]any {
	// A turn the loop never looked at leaves no block: "Located in 0 rounds,
	// resumed" would be the record of an absence.
	if r.Skipped == "off" || r.Skipped == "resumed" || r.Skipped == "no sources" {
		return d
	}
	// A reason and the counts TOGETHER, never the reason alone: a round that
	// landed something and a later round that failed both happen on the same
	// turn, and reporting only "call failed" leaves the step saying it found
	// nothing beside an answer citing what it found. Every step reports what
	// it found.
	if r.Skipped != "" {
		d["locate"] = r.Skipped
	}
	if r.Rounds == 0 {
		return d
	}
	d["locate_rounds"] = r.Rounds
	if len(r.Calls) > 0 {
		d["locate_calls"] = r.Calls
	}
	if len(r.Landed) > 0 {
		d["locate_landed"] = r.Landed
	}
	// What found nothing is the half a reader most wants: it is why the next
	// call was spelled differently.
	if len(r.Empty) > 0 {
		d["locate_empty"] = r.Empty
	}
	if len(r.Refused) > 0 {
		d["locate_refused"] = r.Refused
	}
	if r.Concluded {
		d["locate_concluded"] = true
	}
	// The pointer the answer prompt carried, so a turn read back shows what
	// the answer was pointed at. A debug record like the rest of the trace:
	// never embedded, never handed to a model, never on a shared page.
	if r.Found != "" {
		d["locate_found"] = r.Found
	}
	return d
}

// census reads the link sites of the named repositories when the question
// asked for a listing, and nothing otherwise. Nothing named is nothing read:
// a census over the corpus is the spread the repository rung refuses, and an
// "all repositories" permission does not open it either — forty landings
// per repository across the estate is not an answer about anything.
func (p *Pipeline) census(ctx context.Context, scope Scope) (Census, error) {
	if scope.Census != CensusLink || len(scope.Known) == 0 {
		return Census{}, nil
	}
	return p.gatherer.LinkCensus(ctx, scope.Known)
}

// withCensusDetail adds what the census found to the gathering step, only
// on a turn that ran one: the trace is stored per message, and "links: 0"
// on every mechanism question would be a record of an absence.
func withCensusDetail(d map[string]any, c Census) map[string]any {
	if c.Sites == 0 {
		return d
	}
	d["link_sites"] = c.Sites
	d["links"] = len(c.Landings)
	return d
}

// gatherAndAnswer is the tail both entry points share: expand the hits, settle
// what the turn has to say about its own footing, and answer under that.
//
// It is one function rather than two similar ones because DocsOnly is only
// knowable after the gather, and Resume gathers too. A flag set in Run alone
// would be absent from every resumed turn — the clarification's scope is
// stored before anything has been gathered, so it can only ever say false —
// and the reader would be told nothing on exactly the turns a card sent them
// into a single module.
//
// terms are the search terms for the "nothing found" answer; a resume has none
// to report, having searched nothing.
func (p *Pipeline) gatherAndAnswer(ctx context.Context, question string, audience Audience, lang Language,
	hits []retrieve.Hit, scope Scope, terms []string, followingUp string, ev Events) (Answer, error) {

	scope = p.describeProjects(ctx, scope)

	sources, scope, err := p.gather(ctx, question, hits, scope, ev)
	if err != nil {
		return Answer{}, err
	}
	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, terms), Scope: scope}, nil
	}

	// Re-sent rather than sent once: the scope sentence went out before the
	// search, because a turn that fails or asks has still told the reader what
	// it was looking at, and only now is there a second thing to say. A later
	// notice replaces the earlier one in the UI, so this carries both.
	scope.Processes = p.describeProcesses(ctx, sources)
	if scope.DocsOnly = DocsOnly(sources); scope.DocsOnly {
		ev.notice(ScopeNotice(lang, scope))
	}

	return p.answer(ctx, question, audience, lang, sources, scope, followingUp, ev)
}

// answer is the one Pro call every entry point ends in, with the writing
// step's detail attached once the stream has closed: what it cost, and how
// many of the sources in front of the model it actually cited.
func (p *Pipeline) answer(ctx context.Context, question string, audience Audience, lang Language,
	sources []Source, scope Scope, followingUp string, ev Events) (Answer, error) {
	ev.status("answering")
	answer, err := p.answerer.Answer(ctx, question, audience, lang, sources, scope, followingUp, ev.tokens())
	answer.Scope = scope
	if err == nil {
		ev.detail("writing", writingDetail(answer, len(sources)))
	}
	return answer, err
}

// writingDetail is what the writing step found: what the call cost, how many
// of the sources in front of the model it cited, and how the stream itself
// went. Facts the step already held — no second call is made to report them.
func writingDetail(answer Answer, sources int) map[string]any {
	d := map[string]any{
		"prompt_tokens":     answer.Usage.Prompt,
		"completion_tokens": answer.Usage.Completion,
		"cited":             len(answer.Citations),
		"sources":           sources,
		// The prompt by section, measured rather than billed — the split
		// the endpoint never reports. The reader is told which is which
		// where it is drawn.
		"prompt_system":   answer.Prompt.System,
		"prompt_sources":  answer.Prompt.Sources,
		"prompt_question": answer.Prompt.Question,
	}
	// Only past one: a turn that took a second call is worth a line in the
	// timeline, and saying "1 attempt" on every other turn is noise. The
	// client counts it, so every step could report it the same way.
	if answer.Usage.Attempts > 1 {
		d["attempts"] = answer.Usage.Attempts
	}
	// How many of the reader's standing instructions the prompt carried.
	// Only past zero: a reader with none is every reader before memory.
	if answer.Memories > 0 {
		d["memories"] = answer.Memories
	}
	// How much of the prompt the endpoint had read before and did not
	// charge full price for again. Absent when the reply carried no
	// details object: that is unknown, not zero.
	if answer.Usage.PromptDetails != nil {
		d["cached_tokens"] = answer.Usage.PromptDetails.Cached
	}
	return d
}

// understandingDetail is what the first step found: the phrasings the search
// will run, the identifiers the model guessed, and the scope the turn settled
// on. Terms and code terms are the model's; the scope is the index's answer
// to them, which is why both are shown — a guess that missed the index is
// exactly what a reader wants to see when a search came back thin.
func understandingDetail(u Understanding, scope Scope, pin []string, prior []Source) map[string]any {
	d := map[string]any{}
	// What the turn below this one actually answered out of, recorded so the
	// question "should a follow-up inherit its predecessor's repository" can
	// be settled with a number instead of an argument. Hop 0 is a search hit;
	// Reason is rewritten on promoted hits and cannot be used for this.
	// Reported only, never applied: scope is read from the question and never
	// inferred from the hits.
	if repos := hitRepos(prior); len(repos) > 0 {
		d["prior_repos"] = repos
	}
	if u.Intent != "" {
		d["intent"] = u.Intent
	}
	if len(u.Terms) > 0 {
		d["terms"] = u.Terms
	}
	if len(u.CodeTerms) > 0 {
		d["code_terms"] = u.CodeTerms
	}
	if len(scope.Known) > 0 {
		d["repos"] = scope.Known
	}
	if len(scope.Unknown) > 0 {
		d["unknown_repos"] = scope.Unknown
	}
	if len(scope.Outside) > 0 {
		d["outside_repos"] = scope.Outside
	}
	if len(pin) > 0 {
		d["pinned"] = true
	}
	if scope.All {
		d["all_repos"] = true
	}
	if scope.Stage != "" {
		d["stage"] = scope.Stage
	}
	return d
}

// searchDetail is what the search returned: how many hits, per repository,
// and the best one with the lanes that found it — "keyword strict" against
// "semantic" is the difference between a literal match and a guess.
func searchDetail(hits []retrieve.Hit) map[string]any {
	d := map[string]any{"hits": len(hits)}
	perRepo := map[string]int{}
	for _, h := range hits {
		perRepo[h.Repo]++
	}
	if len(perRepo) > 0 {
		d["per_repo"] = perRepo
	}
	if len(hits) > 0 {
		best := map[string]any{"repo": hits[0].Repo, "path": hits[0].Path}
		if len(hits[0].Lanes) > 0 {
			best["lanes"] = hits[0].Lanes
		}
		d["best"] = best
	}
	return d
}

// routingDetail is the decision and the rung that made it, plus what the
// rung counted. The rung name is the ladder's own (route.go); the trace
// turns it into a sentence a reader understands.
func routingDetail(dec Decision) map[string]any {
	d := map[string]any{"rung": dec.Rung}
	switch {
	case dec.TooBroad:
		d["decision"] = "too_broad"
	case dec.Ask:
		d["decision"] = "ask"
	default:
		d["decision"] = "answer"
	}
	if dec.Ask && len(dec.Candidates) > 0 {
		names := make([]string, 0, len(dec.Candidates))
		for _, c := range dec.Candidates {
			if c.Repo != "" {
				names = append(names, c.Repo)
			}
		}
		d["candidates"] = names
	}
	if dec.Projects > 0 {
		d["projects"] = dec.Projects
	}
	if dec.Repos > 0 {
		d["repos"] = dec.Repos
	}
	return d
}

// gatherDetail is what reached the answer and how: hits, symbol references,
// crossings on a queue or route, the repositories they span, the budget
// used, and each boundary crossed with the token that crossed it.
func gatherDetail(sources []Source, budget int, gaps GapReport) map[string]any {
	d := map[string]any{"sources": len(sources)}
	var hits, refs, crossings, gapped, tokens int
	repos := map[string]bool{}
	var crossed []map[string]string
	seenCrossing := map[string]bool{}
	for _, s := range sources {
		tokens += estimateTokens(s.Text)
		repos[s.Repo] = true
		kind, value, from, isEdge := edgeVia(s.Reason)
		switch {
		case s.Reason == "hit":
			hits++
		case isEdge:
			crossings++
			via := kind + " " + value
			fromRepo, _, _ := strings.Cut(from, "/")
			key := fromRepo + "->" + s.Repo + " " + via
			if !seenCrossing[key] {
				seenCrossing[key] = true
				crossed = append(crossed, map[string]string{"from": fromRepo, "to": s.Repo, "via": via})
			}
		case strings.HasPrefix(s.Reason, "gap:"):
			// Counted apart from the references: a chunk fetched by name
			// after the walk had stopped is not something the walk reached.
			gapped++
		case strings.HasPrefix(s.Reason, "link:"):
			// A census landing is not a reference either: it was seeded,
			// not reached. Reported by withCensusDetail beside the count
			// of sites it was drawn from.
		case strings.HasPrefix(s.Reason, "locate:"):
			// Nor is a locate landing: the model asked for it by name after
			// reading the sources. withLocateDetail reports the loop's own
			// calls beside this, so counting it here too would inflate the
			// walk's number.
		default:
			refs++
		}
	}
	d["hits"] = hits
	d["references"] = refs
	d["crossings"] = crossings
	d["tokens"] = tokens
	if budget > 0 {
		d["budget"] = budget
	}
	d["repos"] = len(repos)
	if len(crossed) > 0 {
		d["crossed"] = crossed
	}
	// Nothing about a pass that is switched off, the count of what it landed
	// included. The trace is stored per message, and "gaps: 0" on every turn
	// of a deployment that never had the pass would be a record of an absence.
	if gaps.Skipped == "off" {
		return d
	}
	d["gaps"] = gapped
	if len(gaps.Asked) > 0 {
		asked := make([]string, 0, len(gaps.Asked))
		for _, n := range gaps.Asked {
			asked = append(asked, n.Name+" ("+n.Kind+")")
		}
		d["gap_asked"] = asked
	}
	if len(gaps.Landed) > 0 {
		d["gap_landed"] = gaps.Landed
	}
	if len(gaps.Unresolved) > 0 {
		d["gap_unresolved"] = gaps.Unresolved
	}
	if len(gaps.Refused) > 0 {
		d["gap_refused"] = gaps.Refused
	}
	if gaps.Skipped != "" {
		d["gap_skipped"] = gaps.Skipped
	}
	return d
}

// searchScoped runs the search the turn's scope calls for.
//
// One search over the whole corpus, or over the one repository the question
// named, is the ordinary case and is unchanged. Two or more named repositories
// is not: the fused list is cut to searchK and nothing reserves room in it per
// repository — RepoDecay ships off, measured a wash in
// docs/measurements/2026-08-22-repo-diversity.md — so one repository can fill
// the cut and the "comparison" would have only one side to compare. Searching
// each named repository separately makes the representation a fact rather
// than a hope, and costs nothing but the extra query: no new retrieval
// machinery, no knob, Query.Repos as it already is.
//
// searchK per repository, not searchK divided among them: each side gets the
// same depth it would have got as the only named one, and gather applies no
// cap to hits by design.
func (p *Pipeline) searchScoped(ctx context.Context, question, prior string, texts []string, code string, known []string, stage retrieve.StagePrefixes) ([]retrieve.Hit, error) {
	if len(known) < 2 {
		return p.search.Search(ctx, retrieve.Query{Texts: texts, Code: code, Repos: known, Question: question, Prior: prior, K: searchK, Stage: stage})
	}
	var all []retrieve.Hit
	for _, repo := range known {
		// Question is left out on purpose: it names every one of these
		// repositories, and knownRepos would union them all back in, undoing
		// the one-repository-at-a-time cut this exists for.
		hits, err := p.search.Search(ctx, retrieve.Query{Texts: texts, Code: code, Repos: []string{repo}, Prior: prior, K: searchK, Stage: stage})
		if err != nil {
			return nil, err
		}
		all = append(all, hits...)
	}
	// Ordered best first across the repositories, as one search would be: the
	// router ranks candidates by their best hit and the gatherer walks in
	// order, and neither should see the repositories' turn order instead.
	// Stable over an input each search already ordered by address, so an equal
	// score falls back to the index's name order, the order knownRepos returns
	// the repositories in, and never to a chunk id.
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	// Bounded whatever the understanding guessed. Gather never evicts a search
	// hit — an answer cites what it was built on — so every hit here becomes a
	// source, and four named repositories would inline eighty chunks of raw
	// code into the answer prompt with nothing to stop them. Two repositories'
	// worth is the depth this was measured at; a third and fourth side compete
	// for the same room rather than adding more.
	if len(all) > comparisonK {
		all = all[:comparisonK]
	}
	return all, nil
}

// Resume continues a turn after the reader chose one candidate from a
// Clarification. It skips understanding, search and routing entirely — the
// candidate's own hits ARE the search result now — and gathers only from
// them. That is what choosing means: a resumed turn must not go looking for
// anything else.
//
// t is what earlier turns of this thread left behind, the way Run takes it. A
// turn that went through a card is still a turn of the thread, and the reader
// who typed "und wo wird das entschieden?" gets it answered by a clarification
// and then by an answer: without the thread the answer prompt loses the rule
// that says what "das" points at. Only t.Question reaches the prompt — see
// answerFollowUp for why the previous answer's text does not.
func (p *Pipeline) Resume(ctx context.Context, question string, audience Audience, lang Language,
	hits []retrieve.Hit, scope Scope, t Thread, ev Events) (Answer, error) {

	// Marked as resumed so the locate loop stays out of it: this path replays
	// the candidate's stored hits and searches for nothing more.
	scope.Resumed = true
	return p.gatherAndAnswer(ctx, question, audience, lang, hits, scope, nil, t.Question, ev)
}

// ResumeRepo continues a turn after the reader chose a REPOSITORY off a
// clarification card, rather than a module.
//
// It searches again, which no other resume path does. A module card stores the
// hits its candidate was built from and replays them, because the answer has
// to be built from exactly what was offered. A repository card cannot work
// that way: the hits it grouped came from one fused list of searchK, and a
// list skewed to the leading repository leaves the runner-up two or three
// chunks to answer from. Searching the chosen repository on its own gives it
// the same depth a question naming it would have got — the same reasoning, and
// the same searchK, as searchScoped's per-repository pass.
//
// More than one repository is the too-broad panel's own resume: the reader
// picked a handful off it, which is the same thing as a question that named
// them, so it searches each one at full depth the way a comparison does.
//
// repos empty is the card's "all repositories" entry: the whole corpus, exactly
// the search the turn would have run without a card at all. The question goes
// back in there because knownRepos may narrow on what it names; in the scoped
// case it is left out for the reason searchScoped gives, or the other
// repositories would be unioned straight back in.
//
// t is what Resume's is: what earlier turns of this thread left behind.
func (p *Pipeline) ResumeRepo(ctx context.Context, question string, u Understanding, repos []string,
	audience Audience, lang Language, scope Scope, t Thread, ev Events) (Answer, error) {

	texts := u.SearchTexts(question)
	stage := p.declaredStages(ctx).Prefixes(scope.Stage)
	var hits []retrieve.Hit
	if len(repos) > 0 {
		// A restriction the index cannot resolve is not a narrow search, it is
		// no search at all: knownRepos drops a name it does not carry, and an
		// empty restriction means the whole corpus. Between the card being
		// asked and the choice being made the repository can leave repos.yaml
		// or be renamed, and without this the turn would answer from every
		// repository while the record and the notice both say it answered from
		// the one that was picked — the exact substitution this rung exists to
		// prevent. Failing leaves the card open and ochre for another choice,
		// which is what a failed turn is for.
		known, _, err := p.search.ResolveRepos(ctx, repos, "")
		if err != nil {
			return Answer{}, fmt.Errorf("resolve the chosen repositories: %w", err)
		}
		if len(known) != len(repos) {
			// Not just "all of them gone": a subset is worse, because the turn
			// would run and look right. The scope, the notice and the prompt
			// rules were all written from the full list a few lines up in the
			// handler, so searching the survivors answers from two
			// repositories while the record says three — the substitution this
			// check exists to stop, one repository at a time.
			return Answer{}, fmt.Errorf("the chosen repositories %s are no longer in the index",
				strings.Join(repos, ", "))
		}
		ev.status("searching")
		// searchScoped, not one fused search: more than one chosen repository
		// is a comparison, and a single cut lets one side fill it. The
		// question is left out for the reason the single-repository search
		// left it out — knownRepos would union the other repositories back in
		// and undo the choice.
		hits, err = p.searchScoped(ctx, "", u.Prior, texts, u.CodeText(), known, stage)
		if err != nil {
			return Answer{}, fmt.Errorf("search: %w", err)
		}
	} else {
		ev.status("searching")
		var err error
		hits, err = p.search.Search(ctx, retrieve.Query{Texts: texts, Code: u.CodeText(), Question: question, Prior: u.Prior, K: searchK, Stage: stage})
		if err != nil {
			return Answer{}, fmt.Errorf("search: %w", err)
		}
	}

	ev.detail("searching", searchDetail(hits))
	scope = p.describeProjects(ctx, scope)

	sources, scope, err := p.gather(ctx, question, hits, scope, ev)
	if err != nil {
		return Answer{}, err
	}
	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, withoutPrior(texts, u.Prior)), Scope: scope}, nil
	}

	return p.answer(ctx, question, audience, lang, sources, scope, t.Question, ev)
}

// Reexplain answers the same question for the other audience from sources a
// prior turn already gathered, without searching or gathering again.
//
// It refuses when sources is empty. A re-index can remove a chunk between the
// first turn and the re-explain request, and answering the same question from
// different code than the reader already saw would be a silent substitution —
// exactly what "never invent" forbids.
func (p *Pipeline) Reexplain(ctx context.Context, question string, audience Audience, lang Language, sources []Source, scope Scope, ev Events) (Answer, error) {
	if len(sources) == 0 {
		return Answer{}, fmt.Errorf("reexplain: no sources left to answer from")
	}

	// Rebuilt here too. Structure is never persisted, so a re-explain that
	// skipped this would answer the same question from the same sources with
	// the project structure missing — the two-backends disambiguation present
	// in the first answer and gone from the second.
	scope = p.describeProjects(ctx, scope)
	// The link listing too, for the same reason: it is never persisted, and
	// the record says the turn was a census. Read again from the index, not
	// re-gathered — the sources are the first turn's.
	census, err := p.census(ctx, scope)
	if err != nil {
		return Answer{}, err
	}
	scope.Links = census.Listing
	// No follow-up rule, on purpose: a re-explain answers the SAME question
	// again for the other audience, and the first answer is right above it in
	// the thread. Telling the model not to restate what was already explained
	// would forbid the one thing this path exists to do.
	return p.answer(ctx, question, audience, lang, sources, scope, "", ev)
}
