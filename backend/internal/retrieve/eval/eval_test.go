// Package eval measures retrieval quality against a fixed question set, so the
// choice between text-embedding-3-small and -large is settled by measurement
// rather than by argument.
//
// Everything here is skipped unless BACKEND_EVAL=1: it needs a real embedding
// endpoint and a real corpus, which no ordinary test may touch. Run it as:
//
//	BACKEND_EVAL=1 \
//	BACKEND_EMBED_BASE_URL=... BACKEND_EMBED_API_KEY=... \
//	BACKEND_EMBED_MODEL=text-embedding-3-small BACKEND_EMBED_DIM=1536 \
//	BACKEND_EVAL_DB=/tmp/rongo-eval-small.db \
//	BACKEND_REPOS_FILE=../../../../repos.yaml BACKEND_REPO_ROOT=/tmp/rongo-eval-repos \
//	go test -v -timeout 60m -run TestEval ./internal/retrieve/eval/
//
// TestEvalIndex builds the corpus, TestEvalMeasure reports the numbers. Run
// once per model into a SEPARATE database file: the vec0 table's width is fixed
// at creation, and the embedding cache is keyed by (content_hash, model) so the
// second model genuinely re-embeds instead of reusing the first one's vectors.
package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

// Question is one entry of the fixed question set. expect_paths are the files
// whose content actually answers it — read and verified, never guessed: a
// question whose expected path was a guess makes the whole measurement
// worthless.
type Question struct {
	Text        string   `json:"question"`
	ExpectRepo  string   `json:"expect_repo"`
	ExpectPaths []string `json:"expect_paths"`
	Kind        string   `json:"kind"`
}

func requireEval(t *testing.T) {
	t.Helper()
	if os.Getenv("BACKEND_EVAL") != "1" {
		t.Skip("set BACKEND_EVAL=1 to run the retrieval evaluation (needs a real embedding endpoint)")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func embedDim(t *testing.T) int {
	t.Helper()
	n, err := strconv.Atoi(envOr("BACKEND_EMBED_DIM", "1536"))
	if err != nil || n <= 0 {
		t.Fatalf("BACKEND_EMBED_DIM = %q, want a positive number", os.Getenv("BACKEND_EMBED_DIM"))
	}
	return n
}

func loadQuestions(t *testing.T) []Question {
	t.Helper()
	body, err := os.ReadFile("questions.json")
	if err != nil {
		t.Fatalf("read questions.json: %v", err)
	}
	var qs []Question
	if err := json.Unmarshal(body, &qs); err != nil {
		t.Fatalf("parse questions.json: %v", err)
	}
	return qs
}

func evalDB(t *testing.T, dim int) *sql.DB {
	t.Helper()
	path := envOr("BACKEND_EVAL_DB", "")
	if path == "" {
		t.Fatal("BACKEND_EVAL_DB is required: each model needs its own database file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the database directory: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, dim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	built, err := store.BuiltDim(db)
	if err != nil {
		t.Fatalf("read the built dimension: %v", err)
	}
	if built != dim {
		t.Fatalf("%s was built for %d dimensions but BACKEND_EMBED_DIM is %d — use a separate file per model",
			path, built, dim)
	}
	return db
}

// TestEvalIndex builds the corpus the measurement runs against.
func TestEvalIndex(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	specs, err := repos.Load(envOr("BACKEND_REPOS_FILE", "../../../../repos.yaml"))
	if err != nil {
		t.Fatalf("load the repository list: %v", err)
	}
	state := indexer.NewStateStore(db)
	if err := state.SyncSpecs(ctx, specs); err != nil {
		t.Fatalf("record the repository list: %v", err)
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	ctagsBin, err := exec.LookPath("ctags")
	if err != nil {
		t.Fatalf("ctags: %v", err)
	}
	gitc := gitrepo.New(gitBin, envOr("BACKEND_REPO_ROOT", "/tmp/rongo-eval-repos"))
	model := envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small")
	pipeline := indexer.New(indexer.Deps{
		DB:      db,
		Git:     gitc,
		Symbols: symbols.NewExtractor(ctagsBin),
		Embedder: embed.NewClient(embed.Config{
			BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
			APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
			Model:   model,
			Dim:     dim,
		}, nil),
		Cache:  embed.NewCache(db, model, dim),
		Writer: indexer.NewWriter(db),
		Chunk:  evalChunkOptions(),
	})

	active, err := state.Active(ctx)
	if err != nil {
		t.Fatalf("read the repository state: %v", err)
	}
	for _, st := range active {
		spec := specToIndex(st)
		token := os.Getenv(tokenEnvOf(specs, st.Name))
		if err := gitc.EnsureCloned(ctx, spec, token); err != nil {
			t.Fatalf("clone %s: %v", st.Name, err)
		}
		branch := st.Branch
		if branch == "" {
			branch, err = gitc.DefaultBranch(ctx, spec, token)
			if err != nil {
				t.Fatalf("resolve the branch of %s: %v", st.Name, err)
			}
			if err := state.SetBranch(ctx, st.Name, branch); err != nil {
				t.Fatalf("record the branch of %s: %v", st.Name, err)
			}
			st.Branch = branch
			spec.Branch = branch
		}
		if err := gitc.Fetch(ctx, spec, token); err != nil {
			t.Fatalf("fetch %s: %v", st.Name, err)
		}
		head, err := gitc.HeadSHA(ctx, spec, branch)
		if err != nil {
			t.Fatalf("head of %s: %v", st.Name, err)
		}
		counts, err := pipeline.IndexRepo(ctx, st, head, nil)
		if err != nil {
			t.Fatalf("index %s: %v", st.Name, err)
		}
		if err := state.MarkIndexed(ctx, st.Name, head, counts); err != nil {
			t.Fatalf("record %s: %v", st.Name, err)
		}
		t.Logf("indexed %-12s branch=%-8s files=%-5d chunks=%d", st.Name, branch, counts.Files, counts.Chunks)
	}
}

func specToIndex(st indexer.RepoState) repos.Spec {
	return repos.Spec{Name: st.Name, CloneURL: st.CloneURL, Branch: st.Branch, Enabled: true}
}

func tokenEnvOf(specs []repos.Spec, name string) string {
	for _, s := range specs {
		if s.Name == name {
			return s.TokenEnv
		}
	}
	return ""
}

// result is one question's outcome.
type result struct {
	q    Question
	rank int // 1-based rank of the first expected hit; 0 means not found
	hits int
	// barred is how many rows the distance bound dropped from the SEMANTIC
	// LANE — measured on the lane itself, never on Search's output.
	//
	// Measuring it through Search reports nothing: each lane fetches
	// evalCandidates rows and Search then truncates to K, so a bounded and an
	// unbounded search both come back with exactly K on a corpus this size and
	// the difference is 0 by construction rather than by observation. The
	// distinction matters: a recall failure that is really this constant has to
	// be visible as such, or the model comparison measures the constant.
	barred int
	// nearest is the distance of the closest chunk in the whole corpus for this
	// question, which says whether the bound is anywhere near binding.
	nearest float64
}

// TestEvalMeasure reports recall@5, recall@20 and MRR for the configured model.
//
// It never asserts a threshold. The number this produces is an input to a
// decision a human makes, and a failing assertion here would only encourage
// tuning the constant until the test passes.
func TestEvalMeasure(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	model := envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small")

	client := embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   model,
		Dim:     dim,
	}, nil)
	r := retrieve.New(db, client)
	if v := os.Getenv("BACKEND_SEARCH_MAX_DISTANCE"); v != "" {
		d, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("BACKEND_SEARCH_MAX_DISTANCE = %q, want a number", v)
		}
		r.MaxDistance = d
	}
	lanes := retrieve.NewStore(db)

	questions := loadQuestions(t)
	results := make([]result, 0, len(questions))
	for _, q := range questions {
		hits, err := r.Search(ctx, retrieve.Query{Text: q.Text, K: 20})
		if err != nil {
			t.Fatalf("search %q: %v", q.Text, err)
		}
		// The same lane, once bounded and once not, at the same candidate
		// count: the difference IS what the bound removed.
		vecs, err := client.Embed(ctx, []string{q.Text})
		if err != nil {
			t.Fatalf("embed %q: %v", q.Text, err)
		}
		bounded, err := lanes.SearchVector(ctx, vecs[0], evalCandidates, r.MaxDistance, nil)
		if err != nil {
			t.Fatalf("bounded lane %q: %v", q.Text, err)
		}
		loose, err := lanes.SearchVector(ctx, vecs[0], evalCandidates, -1, nil)
		if err != nil {
			t.Fatalf("unbounded lane %q: %v", q.Text, err)
		}
		var nearest float64
		if len(loose) > 0 {
			nearest = loose[0].Distance
		}
		results = append(results, result{
			q:       q,
			rank:    rankOfExpected(hits, q),
			hits:    len(hits),
			barred:  len(loose) - len(bounded),
			nearest: nearest,
		})
	}

	var recall5, recall20 int
	var mrr float64
	for _, res := range results {
		if res.rank > 0 {
			mrr += 1 / float64(res.rank)
			if res.rank <= 20 {
				recall20++
			}
			if res.rank <= 5 {
				recall5++
			}
		}
	}
	n := float64(len(results))

	t.Logf("")
	t.Logf("model=%s dim=%d questions=%d max_distance=%v", model, dim, len(results), r.MaxDistance)
	t.Logf("recall@5  = %.3f (%d/%d)", float64(recall5)/n, recall5, len(results))
	t.Logf("recall@20 = %.3f (%d/%d)", float64(recall20)/n, recall20, len(results))
	t.Logf("MRR       = %.3f", mrr/n)

	// Per-question detail, worst first: a bad QUESTION has to stay
	// distinguishable from bad retrieval, and only the list makes that visible.
	sort.SliceStable(results, func(i, j int) bool {
		return rankOrLast(results[i].rank) > rankOrLast(results[j].rank)
	})
	t.Logf("")
	t.Logf("%-5s %-6s %-8s %-8s %s", "rank", "hits", "barred", "nearest", "question")
	for _, res := range results {
		rank := "MISS"
		if res.rank > 0 {
			rank = strconv.Itoa(res.rank)
		}
		t.Logf("%-5s %-6d %-8s %-8.3f %s [%s %v]", rank, res.hits,
			fmt.Sprintf("%d/%d", res.barred, evalCandidates), res.nearest,
			res.q.Text, res.q.ExpectRepo, res.q.ExpectPaths)
	}
}

// evalCandidates matches retrieve's own per-lane candidate count, so the
// barred figure describes the lane the search actually used.
const evalCandidates = 40

func rankOrLast(rank int) int {
	if rank == 0 {
		return 1 << 30
	}
	return rank
}

// rankOfExpected returns the 1-based rank of the first hit that is one of the
// question's expected files in the expected repository, or 0.
func rankOfExpected(hits []retrieve.Hit, q Question) int {
	for i, h := range hits {
		if h.Repo != q.ExpectRepo {
			continue
		}
		for _, want := range q.ExpectPaths {
			if h.Path == want {
				return i + 1
			}
		}
	}
	return 0
}

// TestQuestionSetIsWellFormed runs WITHOUT an endpoint: it is the guard that a
// question set edited months from now still has the shape the harness reads.
func TestQuestionSetIsWellFormed(t *testing.T) {
	questions := loadQuestions(t)
	if len(questions) < 20 {
		t.Errorf("question set has %d entries, want at least 20", len(questions))
	}
	seen := map[string]bool{}
	repoCount := map[string]int{}
	for _, q := range questions {
		if q.Text == "" || q.ExpectRepo == "" || len(q.ExpectPaths) == 0 {
			t.Errorf("incomplete question: %+v", q)
		}
		if seen[q.Text] {
			t.Errorf("duplicate question: %q", q.Text)
		}
		seen[q.Text] = true
		repoCount[q.ExpectRepo]++
	}
	if len(repoCount) < 3 {
		t.Errorf("questions cover %d repositories (%v), want the whole dev corpus", len(repoCount), repoCount)
	}
	fmt.Fprintf(os.Stderr, "question set: %d questions across %v\n", len(questions), repoCount)
}

// evalChunkOptions honours BACKEND_INDEX_COMMENTS so the comment-free arm can
// be indexed from the same harness. Stripping changes every content hash, so
// that arm needs its OWN database file: reusing this one would serve vectors
// computed with comments and the arm would measure nothing.
func evalChunkOptions() indexer.ChunkOptions {
	o := indexer.DefaultChunkOptions()
	o.StripComments = os.Getenv("BACKEND_INDEX_COMMENTS") == "0"
	return o
}
