package ask

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// namingRepos is the understanding fixture with its repos replaced.
func namingRepos(t *testing.T, repos ...string) string {
	t.Helper()
	b, err := json.Marshal(repos)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := strings.Replace(appleTVReply, `"repos": ["peeq"]`, `"repos": `+string(b), 1)
	if out == appleTVReply {
		t.Fatal("the understanding fixture no longer names peeq; update the replacement")
	}
	return out
}

// A project turn is one product, not a comparison of its own repositories: one
// fused search over its members, so every hit is ranked on one scale and the
// reranker's order survives. Searched per repository, each member's rank-0
// chunk scored like any other's, and a small off-topic repository filled half
// the cut.
func TestRun_aNamedProjectIsOneSearchOverItsMembers(t *testing.T) {
	cases := []struct {
		name    string
		named   []string
		queries int
	}{
		{"one whole product, library included", []string{"shop-ui", "shop-backend", "acme-commons"}, 1},
		{"two products are a comparison", []string{"shop-ui", "shop-backend", "acme-commons", "legacy-crm"}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			search := &fakeSearch{}
			p := NewPipeline(twoStepUpstream(t, namingRepos(t, c.named...), "So."), search,
				NewGatherer(gatherDB(t), GatherOptions{MaxHops: 1, TokenBudget: 5000}),
				&fakeRouter{projects: libraryMap(t)})

			if _, _, err := p.Run(context.Background(), "How?", AudienceDev, LanguageEN, Thread{}, Events{}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(search.queries) != c.queries {
				t.Fatalf("ran %d searches, want %d: %+v", len(search.queries), c.queries, search.queries)
			}
			if c.queries != 1 {
				return
			}
			q := search.queries[0]
			got := append([]string(nil), q.Repos...)
			slices.Sort(got)
			want := append([]string(nil), c.named...)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("searched %v, want the whole product %v", got, want)
			}
			if q.K != comparisonK {
				t.Errorf("K = %d, want the project turn's depth %d", q.K, comparisonK)
			}
		})
	}
}

// Choosing a whole product off a card re-searches it the same way.
func TestResumeRepo_aChosenProductIsOneSearch(t *testing.T) {
	var queries []retrieve.Query
	p := newTestPipeline(t, withIndexedSearcher([]string{"shop-ui", "shop-backend", "acme-commons"},
		func(q retrieve.Query) ([]retrieve.Hit, error) {
			queries = append(queries, q)
			return []retrieve.Hit{{ChunkID: 1, Repo: q.Repos[0], Path: "a.go"}}, nil
		}), func(f *pipelineFakes) { f.router = &fakeRouter{projects: libraryMap(t)} })

	chosen := []string{"shop-ui", "shop-backend", "acme-commons"}
	if _, err := p.ResumeRepo(context.Background(), "how is retry done?",
		Understanding{CodeTerms: []string{"retry"}}, chosen,
		AudienceBA, LanguageEN, Scope{Known: chosen}, Thread{}, Events{}); err != nil {
		t.Fatalf("resume repo: %v", err)
	}
	if len(queries) != 1 || len(queries[0].Repos) != 3 {
		t.Fatalf("queries = %+v, want one search over the product", queries)
	}
}

// The search decision, the structure block and the gather ceiling all need
// the project map; one turn reads it once.
func TestRun_readsTheProjectMapOnce(t *testing.T) {
	router := &fakeRouter{projects: libraryMap(t)}
	p := NewPipeline(twoStepUpstream(t, namingRepos(t, "shop-ui", "shop-backend", "acme-commons"), "So."),
		&fakeSearch{}, NewGatherer(gatherDB(t), GatherOptions{MaxHops: 1, TokenBudget: 5000}), router)

	if _, _, err := p.Run(context.Background(), "How?", AudienceDev, LanguageEN, Thread{}, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if router.projectReads != 1 {
		t.Errorf("read the project map %d times, want once", router.projectReads)
	}
}
