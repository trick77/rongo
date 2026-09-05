package ask

import (
	"context"
	"fmt"
	"sort"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
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
}

// Routes decides whether a turn can be answered from the gathered hits or
// must ask the reader to choose among candidates. An interface — satisfied by
// *Router — so the pipeline can be tested without a database or a model.
type Routes interface {
	Route(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, asked Asked) (Decision, error)
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

// Run answers one question, or ends the turn by asking which of several
// independent candidates was meant. Exactly one of the returned Answer and
// *Clarification is meaningful: a non-nil Clarification means the turn ended
// with a question, and the Answer is the zero value.
//
// A turn that finds nothing ends with "nothing found" AND the terms that were
// tried, never with an answer assembled from whatever was in context. Naming
// the terms is the difference between a dead end someone can act on — the
// vocabulary was wrong, ask differently — and a shrug.
func (p *Pipeline) Run(ctx context.Context, question string, audience Audience, lang Language, ev Events) (Answer, *Clarification, error) {
	ev.status("understanding")
	u, err := p.understander.Understand(ctx, question)
	if err != nil {
		return Answer{}, nil, err
	}

	known, unknown, err := p.search.ResolveRepos(ctx, u.Repos, question)
	if err != nil {
		return Answer{}, nil, fmt.Errorf("resolve the named repositories: %w", err)
	}
	scope := Scope{Known: known, Unknown: unknown, All: u.AllRepos}
	// Sent before the search rather than with the answer: it is already known
	// here, and a turn that goes on to fail or to ask has still told the
	// reader what its scope was.
	ev.notice(ScopeNotice(lang, scope))

	texts := u.SearchTexts(question)
	ev.status("searching")
	hits, err := p.searchScoped(ctx, question, texts, known)
	if err != nil {
		return Answer{}, nil, fmt.Errorf("search: %w", err)
	}

	ev.status("routing")
	d, err := p.router.Route(ctx, question, audience, lang, hits, Asked{
		NamedRepos:  len(known),
		AllRepos:    u.AllRepos,
		WholeSystem: u.WholeSystem,
	})
	if err != nil {
		return Answer{}, nil, err
	}
	if d.Ask {
		// The turn ends here. The understanding travels with it: a resumed
		// turn that re-derives its own terms can search differently and
		// answer from material the card never showed.
		return Answer{}, &Clarification{Understanding: u, Candidates: d.Candidates, Scope: scope}, nil
	}

	// Gathering keeps starting from ALL hits, never from a candidate's own
	// subset. The published 0.955 was measured that way; narrowing here would
	// be an unmeasured regression. Routing decides whether to ask, not what
	// to read.
	answer, err := p.gatherAndAnswer(ctx, question, audience, lang, hits, scope, texts, ev)
	return answer, nil, err
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
	hits []retrieve.Hit, scope Scope, terms []string, ev Events) (Answer, error) {

	ev.status("gathering")
	sources, err := p.gatherer.Gather(ctx, hits)
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
	if scope.DocsOnly = DocsOnly(sources); scope.DocsOnly {
		ev.notice(ScopeNotice(lang, scope))
	}

	ev.status("answering")
	answer, err := p.answerer.Answer(ctx, question, audience, lang, sources, scope, ev.tokens())
	answer.Scope = scope
	return answer, err
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
func (p *Pipeline) searchScoped(ctx context.Context, question string, texts []string, known []string) ([]retrieve.Hit, error) {
	if len(known) < 2 {
		return p.search.Search(ctx, retrieve.Query{Texts: texts, Repos: known, Question: question, K: searchK})
	}
	var all []retrieve.Hit
	for _, repo := range known {
		// Question is left out on purpose: it names every one of these
		// repositories, and knownRepos would union them all back in, undoing
		// the one-repository-at-a-time cut this exists for.
		hits, err := p.search.Search(ctx, retrieve.Query{Texts: texts, Repos: []string{repo}, K: searchK})
		if err != nil {
			return nil, err
		}
		all = append(all, hits...)
	}
	// Ordered best first across the repositories, as one search would be: the
	// router ranks candidates by their best hit and the gatherer walks in
	// order, and neither should see the repositories' turn order instead.
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
func (p *Pipeline) Resume(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, scope Scope, ev Events) (Answer, error) {
	return p.gatherAndAnswer(ctx, question, audience, lang, hits, scope, nil, ev)
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
// repo empty is the card's "all repositories" entry: the whole corpus, exactly
// the search the turn would have run without a card at all. The question goes
// back in there because knownRepos may narrow on what it names; in the scoped
// case it is left out for the reason searchScoped gives, or the other
// repositories would be unioned straight back in.
func (p *Pipeline) ResumeRepo(ctx context.Context, question string, u Understanding, repo string,
	audience Audience, lang Language, scope Scope, ev Events) (Answer, error) {

	texts := u.SearchTexts(question)
	q := retrieve.Query{Texts: texts, Question: question, K: searchK}
	if repo != "" {
		// A restriction the index cannot resolve is not a narrow search, it is
		// no search at all: knownRepos drops a name it does not carry, and an
		// empty restriction means the whole corpus. Between the card being
		// asked and the choice being made the repository can leave repos.yaml
		// or be renamed, and without this the turn would answer from every
		// repository while the record and the notice both say it answered from
		// the one that was picked — the exact substitution this rung exists to
		// prevent. Failing leaves the card open and ochre for another choice,
		// which is what a failed turn is for.
		known, _, err := p.search.ResolveRepos(ctx, []string{repo}, "")
		if err != nil {
			return Answer{}, fmt.Errorf("resolve the chosen repository: %w", err)
		}
		if len(known) == 0 {
			return Answer{}, fmt.Errorf("the chosen repository %q is no longer in the index", repo)
		}
		q = retrieve.Query{Texts: texts, Repos: known, K: searchK}
	}
	ev.status("searching")
	hits, err := p.search.Search(ctx, q)
	if err != nil {
		return Answer{}, fmt.Errorf("search: %w", err)
	}

	ev.status("gathering")
	sources, err := p.gatherer.Gather(ctx, hits)
	if err != nil {
		return Answer{}, err
	}
	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, texts), Scope: scope}, nil
	}

	ev.status("answering")
	answer, err := p.answerer.Answer(ctx, question, audience, lang, sources, scope, ev.tokens())
	answer.Scope = scope
	return answer, err
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

	ev.status("answering")
	return p.answerer.Answer(ctx, question, audience, lang, sources, scope, ev.tokens())
}
