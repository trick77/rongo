package ask

import (
	"context"
	"fmt"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// searchK is how many fused hits the gatherer starts from. Deeper than the ten
// a person would read, because the reference walk uses the tail: a handler that
// ranks twelfth is still the thread that leads to the service.
const searchK = 20

// Searcher is the retrieval half. An interface so the pipeline can be tested
// without an embedding endpoint.
type Searcher interface {
	Search(ctx context.Context, q retrieve.Query) ([]retrieve.Hit, error)
}

// Routes decides whether a turn can be answered from the gathered hits or
// must ask the reader to choose among candidates. An interface — satisfied by
// *Router — so the pipeline can be tested without a database or a model.
type Routes interface {
	Route(ctx context.Context, question string, lang Language, hits []retrieve.Hit) (Decision, error)
}

// Clarification is how a turn ends when it asks instead of answering. The
// understanding travels with it: a resumed turn that re-derives its own
// search terms can search differently and answer from material the card
// never showed.
type Clarification struct {
	Understanding Understanding
	Candidates    []Candidate
}

// Events is how a caller watches a turn. Both may be nil.
type Events struct {
	// OnStatus reports which step is running, for the UI to show something
	// while the slow part happens.
	OnStatus func(step string)
	// OnToken receives the answer as it is written.
	OnToken func(tok string)
}

func (e Events) status(step string) {
	if e.OnStatus != nil {
		e.OnStatus(step)
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

	texts := u.SearchTexts(question)
	ev.status("searching")
	hits, err := p.search.Search(ctx, retrieve.Query{Texts: texts, Repos: u.Repos, Question: question, K: searchK})
	if err != nil {
		return Answer{}, nil, fmt.Errorf("search: %w", err)
	}

	ev.status("routing")
	d, err := p.router.Route(ctx, question, lang, hits)
	if err != nil {
		return Answer{}, nil, err
	}
	if d.Ask {
		// The turn ends here. The understanding travels with it: a resumed
		// turn that re-derives its own terms can search differently and
		// answer from material the card never showed.
		return Answer{}, &Clarification{Understanding: u, Candidates: d.Candidates}, nil
	}

	// Gathering keeps starting from ALL hits, never from a candidate's own
	// subset. The published 0.955 was measured that way; narrowing here would
	// be an unmeasured regression. Routing decides whether to ask, not what
	// to read.
	ev.status("gathering")
	sources, err := p.gatherer.Gather(ctx, hits)
	if err != nil {
		return Answer{}, nil, err
	}
	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, texts)}, nil, nil
	}

	ev.status("answering")
	answer, err := p.answerer.Answer(ctx, question, audience, lang, sources, ev.OnToken)
	return answer, nil, err
}

// Resume continues a turn after the reader chose one candidate from a
// Clarification. It skips understanding, search and routing entirely — the
// candidate's own hits ARE the search result now — and gathers only from
// them. That is what choosing means: a resumed turn must not go looking for
// anything else.
func (p *Pipeline) Resume(ctx context.Context, question string, audience Audience, lang Language, hits []retrieve.Hit, ev Events) (Answer, error) {
	ev.status("gathering")
	sources, err := p.gatherer.Gather(ctx, hits)
	if err != nil {
		return Answer{}, err
	}

	ev.status("answering")
	return p.answerer.Answer(ctx, question, audience, lang, sources, ev.OnToken)
}

// Reexplain answers the same question for the other audience from sources a
// prior turn already gathered, without searching or gathering again.
//
// It refuses when sources is empty. A re-index can remove a chunk between the
// first turn and the re-explain request, and answering the same question from
// different code than the reader already saw would be a silent substitution —
// exactly what "never invent" forbids.
func (p *Pipeline) Reexplain(ctx context.Context, question string, audience Audience, lang Language, sources []Source, ev Events) (Answer, error) {
	if len(sources) == 0 {
		return Answer{}, fmt.Errorf("reexplain: no sources left to answer from")
	}

	ev.status("answering")
	return p.answerer.Answer(ctx, question, audience, lang, sources, ev.OnToken)
}
