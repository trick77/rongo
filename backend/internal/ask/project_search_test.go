package ask

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store/storetest"
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
		{"named members without the library are a comparison of those members", []string{"shop-ui", "shop-backend"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			search := &fakeSearch{}
			p := newTestPipeline(t, withUpstream(twoStepUpstream(t, namingRepos(t, c.named...), "So.")),
				withSearch(search), withRouter(&fakeRouter{projects: libraryMap(t)}))

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

// expandingSearch resolves a project's name to all its members, libraries
// included, the way the retriever's knownRepos does.
type expandingSearch struct {
	indexedSearch
	projects map[string][]string
}

func (s expandingSearch) ResolveRepos(_ context.Context, want []string, _ string) (known, unknown []string, err error) {
	seen := map[string]bool{}
	for _, w := range want {
		for _, r := range append([]string{w}, s.projects[w]...) {
			if !seen[r] && slices.Contains(s.indexed, r) {
				seen[r] = true
				known = append(known, r)
			}
		}
	}
	return known, nil, nil
}

// lettersMap is letters, a product named like its one repository, built on
// two libraries, base and forms.
func lettersMap(t *testing.T) projects.Map {
	t.Helper()
	db := storetest.Open(t, 1536)
	for _, r := range [][3]string{{"letters", "letters", "0"}, {"base", "base", "1"}, {"forms", "forms", "1"}} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, project, library) VALUES (?, ?, ?, ?)`,
			r[0], "https://example.invalid/"+r[0]+".git", r[1], r[2]); err != nil {
			t.Fatalf("seed %s: %v", r[0], err)
		}
	}
	for _, u := range []string{"base", "forms"} {
		if _, err := db.Exec(`INSERT INTO repo_uses (repo, uses) VALUES (?, ?)`, "letters", u); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}
	m, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load() err = %v", err)
	}
	return m
}

// A card entry for letters stores the members that had hits; resolving it
// names the project and so expands to every member, both libraries included.
// That expanded list is what is searched, and it is the whole product: one
// search. Decided on the stored list instead, it was a comparison of four
// sides and the libraries took three quarters of the cut.
func TestResumeRepo_aChoiceResolvedToTheWholeProductIsOneSearch(t *testing.T) {
	for _, chosen := range [][]string{{"letters"}, {"base", "letters"}} {
		t.Run(strings.Join(chosen, "+"), func(t *testing.T) {
			var queries []retrieve.Query
			search := expandingSearch{
				indexedSearch: indexedSearch{indexed: []string{"letters", "base", "forms"}, searchFunc: func(q retrieve.Query) ([]retrieve.Hit, error) {
					queries = append(queries, q)
					return []retrieve.Hit{{ChunkID: 1, Repo: q.Repos[0], Path: "a.mtxt"}}, nil
				}},
				projects: map[string][]string{"letters": {"letters", "base", "forms"}},
			}
			p := newTestPipeline(t, func(f *pipelineFakes) {
				f.search = search
				f.router = &fakeRouter{projects: lettersMap(t)}
			})
			scope, thread := ResumeUnder(Scope{}, true, chosen, Thread{})

			if _, err := p.ResumeRepo(context.Background(), "which letter goes out?",
				Understanding{Terms: []string{"letter"}}, chosen, AudienceBA, LanguageDE, scope, thread, Events{}); err != nil {
				t.Fatalf("resume repo: %v", err)
			}
			if len(queries) != 1 {
				t.Fatalf("ran %d searches, want one over the product: %+v", len(queries), queries)
			}
			if got := slices.Sorted(slices.Values(queries[0].Repos)); !slices.Equal(got, []string{"base", "forms", "letters"}) || queries[0].K != comparisonK {
				t.Errorf("searched %v at depth %d, want the whole product at %d", got, queries[0].K, comparisonK)
			}
		})
	}
}

// The search decision, the structure block and the gather ceiling all need
// the project map; one turn reads it once.
func TestRun_readsTheProjectMapOnce(t *testing.T) {
	router := &fakeRouter{projects: libraryMap(t)}
	p := newTestPipeline(t, withUpstream(twoStepUpstream(t, namingRepos(t, "shop-ui", "shop-backend", "acme-commons"), "So.")),
		withRouter(router))

	if _, _, err := p.Run(context.Background(), "How?", AudienceDev, LanguageEN, Thread{}, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if router.projectReads != 1 {
		t.Errorf("read the project map %d times, want once", router.projectReads)
	}
}
