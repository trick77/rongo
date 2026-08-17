package eval

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/retrieve"
)

// evalCandidates in eval_test.go is the lane depth for the distance-bound
// measurement. moduleCandidates is a different thing: how deep Search goes
// BEFORE reranking, so that reordering can still change what lands in the
// top 20.
//
// Reranking the top 20 in place would leave recall@20 identical by
// construction — the same set, in a different order — and the arm would report
// a difference of zero no matter how well the module score worked. That is the
// same mistake the phase-2 `barred` metric made, and it is worth restating:
// every arm here returns 20 hits, and all three metrics are free to move.
const moduleCandidates = 60

// moduleOpts reads the clustering constants. They are calibrated against the
// real corpus with TestModuleList and then written into the measurement
// document; the defaults are a starting point, not a finding.
func moduleOpts(t *testing.T) modules.Opts {
	t.Helper()
	return modules.Opts{
		MinChunks: envIntOr(t, "BACKEND_MODULE_MIN_CHUNKS", 8),
		MaxChunks: envIntOr(t, "BACKEND_MODULE_MAX_CHUNKS", 150),
	}
}

func envIntOr(t *testing.T, key string, fallback int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s = %q, want a number", key, v)
	}
	return n
}

// clusterAll clusters every repository the database knows about.
func clusterAll(t *testing.T, db *sql.DB, o modules.Opts) []modules.Module {
	t.Helper()
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `SELECT name FROM repo_state ORDER BY name`)
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	defer rows.Close()
	var repos []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan repository: %v", err)
		}
		repos = append(repos, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read repositories: %v", err)
	}

	var all []modules.Module
	for _, repo := range repos {
		mods, err := modules.Cluster(ctx, db, repo, o)
		if err != nil {
			t.Fatalf("cluster %s: %v", repo, err)
		}
		all = append(all, mods...)
	}
	return all
}

// TestModuleList prints the module cut of the indexed corpus. This is the step
// that settles the granularity the spec deliberately left open: the constants
// are chosen by looking at this list, not by argument.
func TestModuleList(t *testing.T) {
	requireEval(t)
	db := evalDB(t, embedDim(t))
	o := moduleOpts(t)
	mods := clusterAll(t, db, o)

	byRepo := map[string]int{}
	oversized := 0
	chunks := 0
	for _, m := range mods {
		byRepo[m.Repo]++
		chunks += m.ChunkCount
		if m.Oversized {
			oversized++
		}
	}

	t.Logf("")
	t.Logf("min_chunks=%d max_chunks=%d modules=%d chunks=%d oversized=%d",
		o.MinChunks, o.MaxChunks, len(mods), chunks, oversized)
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		t.Logf("  %-12s %d modules", r, byRepo[r])
	}

	t.Logf("")
	t.Logf("%-8s %-6s %-9s %s", "chunks", "files", "oversized", "module")
	sort.SliceStable(mods, func(i, j int) bool { return mods[i].ChunkCount > mods[j].ChunkCount })
	for _, m := range mods {
		flag := ""
		if m.Oversized {
			flag = "OVERSIZED"
		}
		t.Logf("%-8d %-6d %-9s %s/%s", m.ChunkCount, len(m.Paths), flag, m.Repo, m.Key)
	}
}

// arm is one ranking strategy measured against the question set.
type arm struct {
	name  string
	score retrieve.ModuleScore
	alpha float64
}

type armResult struct {
	recall5, recall20 int
	mrr               float64
	rank              map[string]int // question -> rank of the expected file
}

// TestEvalMeasureModules compares plain chunk ranking against module-aware
// ranking on the same questions, the same index and the same candidate depth.
//
// It asserts nothing. The numbers are an input to a decision a human makes, and
// an assertion here would only invite tuning a constant until it passes.
func TestEvalMeasureModules(t *testing.T) {
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

	o := moduleOpts(t)
	mods := clusterAll(t, db, o)
	idx := retrieve.NewModuleIndex(mods)
	questions := loadQuestions(t)

	// Fixed before the run so the outcome cannot be picked afterwards: the
	// module layer is worth building on only if an arm beats the baseline's
	// recall@5. Two blend strengths are a coarse sweep, not a hunt for the
	// number that flatters the result — no further alphas are tried.
	arms := []arm{
		{name: "baseline", score: ""},
		{name: "module-sum", score: retrieve.ScoreSum},
		{name: "module-best", score: retrieve.ScoreBest},
		{name: "module-count", score: retrieve.ScoreCount},
		{name: "blend-0.15", score: retrieve.ScoreBlend, alpha: 0.15},
		{name: "blend-0.40", score: retrieve.ScoreBlend, alpha: 0.40},
	}
	out := map[string]*armResult{}
	for _, a := range arms {
		out[a.name] = &armResult{rank: map[string]int{}}
	}

	for _, q := range questions {
		// One search per question, reused by every arm: the arms must differ in
		// how they rank, not in what they were given.
		hits, err := r.Search(ctx, retrieve.Query{Text: q.Text, K: moduleCandidates})
		if err != nil {
			t.Fatalf("search %q: %v", q.Text, err)
		}
		for _, a := range arms {
			ranked := hits
			if a.score != "" {
				ranked = retrieve.RerankByModule(hits, idx, retrieve.RerankOpts{Score: a.score, Alpha: a.alpha})
			}
			out[a.name].add(q, cut(ranked, 20))
		}
	}

	n := float64(len(questions))
	t.Logf("")
	t.Logf("model=%s dim=%d questions=%d candidates=%d min_chunks=%d max_chunks=%d modules=%d",
		model, dim, len(questions), moduleCandidates, o.MinChunks, o.MaxChunks, len(mods))
	t.Logf("")
	t.Logf("%-14s %-10s %-10s %s", "arm", "recall@5", "recall@20", "MRR")
	for _, a := range arms {
		res := out[a.name]
		t.Logf("%-14s %-10.3f %-10.3f %.3f",
			a.name, float64(res.recall5)/n, float64(res.recall20)/n, res.mrr/n)
	}

	// The four questions that missed under both embedding models in phase 2 are
	// the reason this phase exists. Their individual movement is the finding —
	// an aggregate over 28 questions hides a change in four of them.
	t.Logf("")
	t.Logf("per-question rank, baseline first (0 = not in the top %d)", 20)
	t.Logf("%-10s %-12s %-12s %s", "baseline", "blend-0.15", "blend-0.40", "question")
	base := out["baseline"]
	sort.SliceStable(questions, func(i, j int) bool {
		return rankOrLast(base.rank[questions[i].Text]) > rankOrLast(base.rank[questions[j].Text])
	})
	for _, q := range questions {
		t.Logf("%-10d %-12d %-12d %s",
			base.rank[q.Text],
			out["blend-0.15"].rank[q.Text],
			out["blend-0.40"].rank[q.Text],
			short(q.Text))
	}
}

func (a *armResult) add(q Question, hits []retrieve.Hit) {
	rank := rankOfExpected(hits, q)
	a.rank[q.Text] = rank
	if rank <= 0 {
		return
	}
	a.mrr += 1 / float64(rank)
	if rank <= 20 {
		a.recall20++
	}
	if rank <= 5 {
		a.recall5++
	}
}

func cut(hits []retrieve.Hit, k int) []retrieve.Hit {
	if len(hits) <= k {
		return hits
	}
	return hits[:k]
}

func short(s string) string {
	const max = 64
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return fmt.Sprintf("%s…", string(r[:max-1]))
}
