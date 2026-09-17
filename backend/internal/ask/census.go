package ask

import (
	"context"
	"fmt"
	"strings"

	"github.com/trick77/rongo/internal/edges"
)

// The link census: "what does this user interface link to" is a listing,
// not a mechanism, and search is built for mechanisms — twenty chunks, then
// a walk. A listing is read from the index instead: every navigation site
// the indexer recorded for the repository (edges.KindLink), each landing on
// the chunk that holds it, before the symbol walk, so a site written as
// `${environment.portalUrl}/claims` pulls the environment file in through
// the ordinary walk. Rongo never resolves the URL itself: the answer model
// reads the sites and the configuration and says which lead outside.
//
// Two things reach the answer. The landings are sources, cited like any
// other. The listing is a prompt block naming EVERY site with its place,
// past the landing cap included; it is read from the index, never written
// by a model, so it sits outside the rule against stored model text — and
// like the structure block it is never cited, the landing is.

// censusMaxLandings caps the sources a census seeds, one per file. Past it
// the listing still names the site.
const censusMaxLandings = 40

// censusShare is the fraction of the gather budget the landings may fill:
// a third. Landings are seeded whole and never evicted, and a chunk is up
// to 600 tokens, so forty of them would be the whole budget and the walk
// could take nothing — not even the environment file the sites name, which
// is the point of landing on them. A third leaves the walk and the crossing
// what they had.
const censusShare = 3

// censusMaxListed caps the listing block: two hundred lines is a page of
// prompt, and a repository with more navigation sites than that is telling
// the reader something the tail would not add to.
const censusMaxListed = 200

// Census is what the link census found: the landings to seed the gather
// with, the listing block for the prompt, and the count for the trace.
type Census struct {
	Landings []Source
	Listing  string
	Sites    int
}

// LinkCensus reads every link site of the named repositories. Nothing named
// is nothing found: a census over the corpus is the spread the repository
// rung refuses, and it refuses it here the same way.
func (g *Gatherer) LinkCensus(ctx context.Context, repos []string) (Census, error) {
	var c Census
	var rows []edges.Neighbour
	for _, repo := range repos {
		ns, err := edges.InRepo(ctx, g.db, repo, edges.KindLink)
		if err != nil {
			return Census{}, fmt.Errorf("read the link sites of %s: %w", repo, err)
		}
		rows = append(rows, ns...)
	}
	c.Sites = len(rows)
	if c.Sites == 0 {
		return c, nil
	}
	files := map[string]bool{}
	budget := g.opts.TokenBudget / censusShare
	spent := 0
	for _, n := range rows {
		if len(c.Landings) >= censusMaxLandings {
			break
		}
		key := n.Repo + "\x00" + n.Path
		if files[key] {
			continue
		}
		s, ok, err := g.chunkAt(ctx, n.Repo, n.Path, n.Line)
		if err != nil {
			return Census{}, fmt.Errorf("read the landing of link %q: %w", n.Value, err)
		}
		if !ok {
			continue
		}
		cost := estimateTokens(s.Text)
		if spent+cost > budget {
			break
		}
		files[key] = true
		spent += cost
		// integration_tokens.value: the index's spelling, never a model's.
		s.Reason = "link:" + n.Value
		c.Landings = append(c.Landings, s)
	}
	c.Listing = linkListing(rows)
	return c, nil
}

// linkListing renders the sites for the prompt, one per line, the value
// first and its place after, in the index's path order.
func linkListing(rows []edges.Neighbour) string {
	var b strings.Builder
	for i, n := range rows {
		if i == censusMaxListed {
			fmt.Fprintf(&b, "... and %d more\n", len(rows)-i)
			break
		}
		fmt.Fprintf(&b, "%s  %s/%s:%d\n", n.Value, n.Repo, n.Path, n.Line)
	}
	return b.String()
}
