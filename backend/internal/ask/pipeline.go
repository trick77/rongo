package ask

import (
	"context"
	"fmt"
	"strings"

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

// Pipeline runs a question end to end: understand, search, gather, answer.
type Pipeline struct {
	understander *Understander
	search       Searcher
	gatherer     *Gatherer
	answerer     *Answerer
}

// NewPipeline wires the four steps.
func NewPipeline(c *llm.Client, s Searcher, g *Gatherer) *Pipeline {
	return &Pipeline{
		understander: NewUnderstander(c),
		search:       s,
		gatherer:     g,
		answerer:     NewAnswerer(c),
	}
}

// Run answers one question.
//
// A turn that finds nothing ends with "nothing found" AND the terms that were
// tried, never with an answer assembled from whatever was in context. Naming
// the terms is the difference between a dead end someone can act on — the
// vocabulary was wrong, ask differently — and a shrug.
func (p *Pipeline) Run(ctx context.Context, question string, audience Audience, ev Events) (Answer, error) {
	ev.status("verstehen")
	u, err := p.understander.Understand(ctx, question)
	if err != nil {
		return Answer{}, err
	}

	texts := u.SearchTexts(question)
	ev.status("suchen")
	hits, err := p.search.Search(ctx, retrieve.Query{Texts: texts, Repos: u.Repos, K: searchK})
	if err != nil {
		return Answer{}, fmt.Errorf("search: %w", err)
	}

	ev.status("sammeln")
	sources, err := p.gatherer.Gather(ctx, hits)
	if err != nil {
		return Answer{}, err
	}
	if len(sources) == 0 {
		return Answer{Text: nothingFound + " Gesucht wurde nach: " + strings.Join(texts, " · ") + "."}, nil
	}

	ev.status("antworten")
	return p.answerer.Answer(ctx, question, audience, sources, ev.OnToken)
}
