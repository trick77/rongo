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
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

// Resolution says how a question is meant to be answered. It is the part the
// old shape could not express: "either-or, and asking is the right reaction"
// does not fit into one repository plus a list of paths.
type Resolution string

const (
	// ResolutionUnique: one candidate. Exactly one file (or a small set of
	// files inside one repository) answers the question.
	ResolutionUnique Resolution = "unique"
	// ResolutionAmbiguous: several INDEPENDENT candidates. The right reaction
	// is to ask which one is meant; answering one of them silently is wrong.
	ResolutionAmbiguous Resolution = "ambiguous"
	// ResolutionComposition: several candidates that are parts of ONE
	// mechanism. Asking would be wrong — it forces a choice between halves of
	// the truth — and every part belongs in the answer.
	ResolutionComposition Resolution = "composition"
)

// Candidate is one place that answers a question, in one repository.
//
// paths are the files whose content actually answers it — read and verified,
// never guessed: a question whose expected path was a guess makes the whole
// measurement worthless. Verified records what was read to establish that, so
// a later reader can check the claim instead of trusting it.
type Candidate struct {
	Repo     string   `json:"repo"`
	Paths    []string `json:"paths"`
	Verified string   `json:"verified,omitempty"`
}

// Question is one entry of the fixed question set.
type Question struct {
	Text       string      `json:"question"`
	Kind       string      `json:"kind"`
	Resolution Resolution  `json:"resolution"`
	Candidates []Candidate `json:"candidates"`
	// Note carries why a question is ambiguous or a composition — for the
	// composition cases, which repository depends on which, and where that is
	// declared.
	Note string `json:"note,omitempty"`
}

// paths renders the candidates for a log line.
func (q Question) paths() string {
	parts := make([]string, 0, len(q.Candidates))
	for _, c := range q.Candidates {
		parts = append(parts, c.Repo+":"+strings.Join(c.Paths, ","))
	}
	return strings.Join(parts, " | ")
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
	// firstRank scores against the FIRST candidate only. For a unique question
	// it equals rank; for the two that were reclassified as ambiguous it is
	// what the earlier documents measured, which is what makes the anchor arm
	// comparable to them.
	firstRank int
	// found is how many of the question's candidates the hit list surfaces.
	found int
	hits  int
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
		hits, err := r.Search(ctx, retrieve.Query{Text: q.Text, Question: q.Text, K: 20})
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
			q:         q,
			rank:      rankOfExpected(hits, q),
			firstRank: firstCandidateRank(hits, q),
			found:     candidatesFound(hits, q),
			hits:      len(hits),
			barred:    len(loose) - len(bounded),
			nearest:   nearest,
		})
	}

	t.Logf("")
	t.Logf("model=%s dim=%d questions=%d max_distance=%v", model, dim, len(results), r.MaxDistance)

	// The headline is the unique cohort. Mixing the other two in would silently
	// change what recall@5 means against every earlier document.
	reportRanks(t, "unique cohort", false, filterResults(results, func(res result) bool {
		return res.q.Resolution == ResolutionUnique
	}))

	// The anchor. Same questions, same scoring as phase 2/3/4a — it must come
	// back 0.679 / 0.786 / 0.476, and if it does not, nothing else on this page
	// can be compared with anything published before it.
	anchor := loadAnchorCohort(t)
	reportRanks(t, "anchor: the original 28, first candidate only", true,
		filterResults(results, func(res result) bool { return anchor[res.q.Text] }))

	reportCohorts(t, results)

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
		t.Logf("%-5s %-6d %-8s %-8.3f %s [%s %s]", rank, res.hits,
			fmt.Sprintf("%d/%d", res.barred, evalCandidates), res.nearest,
			res.q.Text, res.q.Resolution, res.q.paths())
	}
}

// evalCandidates matches retrieve's own per-lane candidate count, so the
// barred figure describes the lane the search actually used.
const evalCandidates = 40

// anchorFile names the questions the published measurements ran on.
const anchorFile = "anchor-cohort.json"

// loadAnchorCohort reads the question texts the earlier documents measured.
func loadAnchorCohort(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(anchorFile)
	if err != nil {
		t.Fatalf("read %s: %v", anchorFile, err)
	}
	var doc struct {
		Questions []string `json:"questions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse %s: %v", anchorFile, err)
	}
	out := map[string]bool{}
	for _, q := range doc.Questions {
		out[q] = true
	}
	return out
}

func filterResults(in []result, keep func(result) bool) []result {
	var out []result
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// reportRanks prints recall@5, recall@20 and MRR for one cohort.
//
// firstOnly scores against the first candidate instead of any of them. It is a
// parameter and not a guess from the label: keying the anchor arm's scoring off
// its own log text would mean that renaming the line silently changes the two
// reclassified questions' scores, which is the exact comparability that arm
// exists to guarantee.
func reportRanks(t *testing.T, label string, firstOnly bool, rows []result) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	var recall5, recall20 int
	var mrr float64
	for _, res := range rows {
		rank := res.rank
		if firstOnly {
			rank = res.firstRank
		}
		if rank > 0 {
			mrr += 1 / float64(rank)
			if rank <= 20 {
				recall20++
			}
			if rank <= 5 {
				recall5++
			}
		}
	}
	n := float64(len(rows))
	t.Logf("")
	t.Logf("--- %s (n=%d)", label, len(rows))
	t.Logf("recall@5  = %.3f (%d/%d)", float64(recall5)/n, recall5, len(rows))
	t.Logf("recall@20 = %.3f (%d/%d)", float64(recall20)/n, recall20, len(rows))
	t.Logf("MRR       = %.3f", mrr/n)
}

// reportCohorts prints what the two new kinds of question ask of the search.
//
// They are NOT recall numbers and must not be read as such. An ambiguous
// question is served when the search puts at least two of its alternatives on
// the table; a composition question when it puts all of the parts there.
func reportCohorts(t *testing.T, results []result) {
	t.Helper()
	var ambigN, ambigServed, ambigFound, ambigTotal int
	var compN, compComplete, compFound, compTotal int
	for _, res := range results {
		switch res.q.Resolution {
		case ResolutionAmbiguous:
			ambigN++
			ambigFound += res.found
			ambigTotal += len(res.q.Candidates)
			if res.found >= 2 {
				ambigServed++
			}
		case ResolutionComposition:
			compN++
			compFound += res.found
			compTotal += len(res.q.Candidates)
			if res.found == len(res.q.Candidates) {
				compComplete++
			}
		}
	}
	if ambigN > 0 {
		t.Logf("")
		t.Logf("--- ambiguous (n=%d): two or more alternatives in the top 20 = %.3f (%d/%d), candidates %d/%d",
			ambigN, float64(ambigServed)/float64(ambigN), ambigServed, ambigN, ambigFound, ambigTotal)
	}
	if compN > 0 {
		t.Logf("--- composition (n=%d): all parts in the top 20 = %.3f (%d/%d), parts %d/%d",
			compN, float64(compComplete)/float64(compN), compComplete, compN, compFound, compTotal)
	}
}

func rankOrLast(rank int) int {
	if rank == 0 {
		return 1 << 30
	}
	return rank
}

// rankOfExpected returns the 1-based rank of the first hit belonging to ANY of
// the question's candidates, or 0.
//
// For a unique question that is exactly what it always was. For the other two
// it is the weakest possible reading — "the search found one of the places" —
// and the cohort metrics below say the sharper thing.
func rankOfExpected(hits []retrieve.Hit, q Question) int {
	best := 0
	for _, c := range q.Candidates {
		r := rankOfCandidate(hits, c)
		if r > 0 && (best == 0 || r < best) {
			best = r
		}
	}
	return best
}

// firstCandidateRank scores against the first candidate, or 0 when a question
// has none. A candidate-less question is what TestQuestionSetIsWellFormed
// reports in plain words; indexing into the slice here would beat it to it with
// an index-out-of-range panic in the middle of a measurement run.
func firstCandidateRank(hits []retrieve.Hit, q Question) int {
	if len(q.Candidates) == 0 {
		return 0
	}
	return rankOfCandidate(hits, q.Candidates[0])
}

// rankOfCandidate returns the 1-based rank of the first hit that is one of this
// candidate's files in this candidate's repository, or 0.
func rankOfCandidate(hits []retrieve.Hit, c Candidate) int {
	for i, h := range hits {
		if h.Repo != c.Repo {
			continue
		}
		for _, want := range c.Paths {
			if h.Path == want {
				return i + 1
			}
		}
	}
	return 0
}

// candidatesFound counts how many of a question's candidates the hit list
// surfaces at all. For an ambiguous question this is the number that matters:
// a search that returns only one of the alternatives gives the clarification
// step nothing to ask about.
func candidatesFound(hits []retrieve.Hit, q Question) int {
	n := 0
	for _, c := range q.Candidates {
		if rankOfCandidate(hits, c) > 0 {
			n++
		}
	}
	return n
}

// TestQuestionSetIsWellFormed runs WITHOUT an endpoint: it is the guard that a
// question set edited months from now still has the shape the harness reads.
//
// The resolution rules are the substance here. An ambiguous question with one
// candidate is not ambiguous, and a composition question with one candidate is
// not a composition — either would be scored as an ordinary question and quietly
// report the wrong thing.
func TestQuestionSetIsWellFormed(t *testing.T) {
	questions := loadQuestions(t)
	// The floor the two measurement documents ask for. At 28 a single question
	// was 3.6 points, which is why "three gained, three lost" could not be read
	// either way.
	if len(questions) < 50 {
		t.Errorf("question set has %d entries, want at least 50", len(questions))
	}
	seen := map[string]bool{}
	repoCount := map[string]int{}
	byResolution := map[Resolution]int{}

	for _, q := range questions {
		if q.Text == "" || q.Kind == "" {
			t.Errorf("incomplete question: %+v", q)
			continue
		}
		if seen[q.Text] {
			t.Errorf("duplicate question: %q", q.Text)
		}
		seen[q.Text] = true

		switch q.Resolution {
		case ResolutionUnique:
			if len(q.Candidates) != 1 {
				t.Errorf("%q is unique but has %d candidates", q.Text, len(q.Candidates))
			}
		case ResolutionAmbiguous, ResolutionComposition:
			if len(q.Candidates) < 2 {
				t.Errorf("%q is %s but has %d candidate(s) — one candidate is neither",
					q.Text, q.Resolution, len(q.Candidates))
			}
			if q.Note == "" {
				// Why a question is ambiguous, or which repository depends on
				// which, is the part a later reader cannot reconstruct.
				t.Errorf("%q is %s and carries no note", q.Text, q.Resolution)
			}
		default:
			t.Errorf("%q has resolution %q, want unique, ambiguous or composition", q.Text, q.Resolution)
		}
		byResolution[q.Resolution]++

		inQuestion := map[string]bool{}
		for _, c := range q.Candidates {
			if c.Repo == "" || len(c.Paths) == 0 {
				t.Errorf("%q has an incomplete candidate: %+v", q.Text, c)
			}
			key := c.Repo + "\x00" + strings.Join(c.Paths, ",")
			if inQuestion[key] {
				t.Errorf("%q names the same candidate twice: %s %v", q.Text, c.Repo, c.Paths)
			}
			inQuestion[key] = true
			repoCount[c.Repo]++
		}
	}

	if len(repoCount) < 3 {
		t.Errorf("questions cover %d repositories (%v), want the whole dev corpus", len(repoCount), repoCount)
	}
	// Phase 4b is graded on these two, so a set without them cannot grade it.
	if byResolution[ResolutionAmbiguous] == 0 {
		t.Error("no ambiguous question: the clarification step cannot be measured against this set")
	}
	if byResolution[ResolutionComposition] == 0 {
		t.Error("no composition question: nothing checks that rongo does NOT ask when the parts belong together")
	}
	fmt.Fprintf(os.Stderr, "question set: %d questions across %v, by resolution %v\n",
		len(questions), repoCount, byResolution)
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
