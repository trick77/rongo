package eval

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// expansionsFile holds the understanding step's output for every question, so
// the comparison can be repeated without a model endpoint — and so a later run
// measures the same expansion rather than a fresh one. The model is not
// deterministic; freezing its output is what makes this a measurement of
// retrieval instead of a measurement of the model's mood.
const expansionsFile = "expansions.json"

type expansion struct {
	Question string   `json:"question"`
	Texts    []string `json:"texts"`
	// Repos is the understanding's OTHER output: the repositories the question
	// names, which pipeline.go hands to Search as a restriction. It is frozen
	// separately from Texts and is optional, because the file predates it —
	// every arm that reads Texts reads exactly the bytes it always did, which
	// is what keeps the phase 2, 3 and 4a numbers comparable. See
	// TestExpandQuestionRepos.
	Repos []string `json:"repos,omitempty"`
}

// readExpansions parses the frozen file, or skips the arm if it is not there.
func readExpansions(t *testing.T) []expansion {
	t.Helper()
	body, err := os.ReadFile(expansionsFile)
	if err != nil {
		t.Skipf("no %s yet; run TestExpandQuestions first", expansionsFile)
	}
	var list []expansion
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse %s: %v", expansionsFile, err)
	}
	return list
}

func loadExpansions(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, e := range readExpansions(t) {
		out[e.Question] = e.Texts
	}
	return out
}

// loadExpansionRepos returns the frozen repo restriction per question. A
// question with none maps to nil, which is what Search treats as "the whole
// corpus" — the same meaning an empty Understanding.Repos has in pipeline.go.
func loadExpansionRepos(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, e := range readExpansions(t) {
		out[e.Question] = e.Repos
	}
	return out
}

// TestExpandQuestions runs the understanding step over the question set once
// and freezes the result. It is the only test here that calls a model, and it
// costs one short-gate call per question.
func TestExpandQuestions(t *testing.T) {
	requireEval(t)
	base := os.Getenv("BACKEND_LLM_BASE_URL")
	if base == "" {
		t.Skip("BACKEND_LLM_BASE_URL is unset")
	}
	c := llm.NewClient(llm.Config{
		BaseURL: base,
		APIKey:  os.Getenv("BACKEND_LLM_API_KEY"),
		Timeout: 2 * time.Minute,
	}, nil)
	u := ask.NewUnderstander(c)

	// Retried, and the file is written BEFORE anything fails. The model
	// occasionally replies with something that is not JSON — one such reply on
	// the last question once threw away sixty successful calls, because the
	// whole run failed before writing. Paid-for work is kept; the gap is
	// reported instead of being paved over with the raw question, which would
	// look like an expansion that expanded nothing.
	// Whatever is already frozen is the floor. Writing only this run's
	// successes would DELETE a question's existing expansion whenever its three
	// attempts fail — and every arm that reads this file fatals on a missing
	// entry, so one bad reply would block every measurement until someone paid
	// for a fresh sweep of all 61.
	//
	// The whole RECORD is carried forward, not just its texts. Repos is frozen
	// by an arm of its own (TestExpandQuestionRepos) and rebuilding a record
	// from texts alone would drop it — silently, because a missing restriction
	// is indistinguishable from "this question names no repository". The
	// routing arms would then go back to measuring the un-narrowed router, and
	// nothing would fail.
	previous := map[string]expansion{}
	if body, err := os.ReadFile(expansionsFile); err == nil {
		var list []expansion
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("parse %s: %v", expansionsFile, err)
		}
		for _, e := range list {
			previous[e.Question] = e
		}
	}

	var out []expansion
	var failed []string
	var kept []string
	for _, q := range loadQuestions(t) {
		var texts []string
		var last error
		for attempt := 1; attempt <= expandAttempts; attempt++ {
			got, err := u.Understand(context.Background(), q.Text)
			if err == nil {
				texts = got.SearchTexts(q.Text)
				break
			}
			last = err
			t.Logf("RETRY %d/%d %s: %v", attempt, expandAttempts, short(q.Text), err)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if texts == nil {
			if old, ok := previous[q.Text]; ok {
				// The frozen expansion stays, whole. It is still a measurement
				// of a real expansion, just an older one — which is what
				// freezing them is for.
				out = append(out, old)
				kept = append(kept, q.Text)
			} else {
				failed = append(failed, q.Text)
			}
			t.Errorf("understand %q: %v", q.Text, last)
			continue
		}
		t.Logf("EXPANDED %-60s -> %v", short(q.Text), texts[1:])
		out = append(out, refreshTexts(previous[q.Text], q.Text, texts))
	}

	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(expansionsFile, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", expansionsFile, err)
	}
	if len(kept) > 0 {
		t.Logf("%d question(s) kept their previously frozen expansion: %v", len(kept), kept)
	}
	if len(failed) > 0 {
		// Loud on purpose: the arms that read this file fail on a missing
		// entry, and a measurement quietly run on 60 of 61 questions is worse
		// than one that does not run.
		t.Logf("%d question(s) have no expansion at all: %v", len(failed), failed)
	}
}

// expandAttempts is how often one question's understanding step is retried
// before it counts as missing.
const expandAttempts = 3

// refreshTexts writes a fresh expansion into an existing record, keeping every
// other frozen field. Building a new record from the texts alone is the whole
// point of this being a function: Repos is frozen by a separate arm and a
// rebuild would drop it silently, because "no restriction recorded" and "this
// question names no repository" look the same to every reader of the file.
func refreshTexts(prev expansion, question string, texts []string) expansion {
	prev.Question = question
	prev.Texts = texts
	return prev
}

// TestRefreshTextsKeepsTheFrozenRepoRestriction runs WITHOUT an endpoint. The
// two freezing arms write the same file and must not undo each other: whoever
// re-runs the expansion sweep would otherwise silently revert the routing arms
// to searching the whole corpus, which is exactly the harness/product
// divergence phase 4c set out to close.
func TestRefreshTextsKeepsTheFrozenRepoRestriction(t *testing.T) {
	prev := expansion{
		Question: "Which PRAGMAs does peeq set when opening the database?",
		Texts:    []string{"old"},
		Repos:    []string{"peeq"},
	}

	got := refreshTexts(prev, prev.Question, []string{"new", "also new"})

	if len(got.Repos) != 1 || got.Repos[0] != "peeq" {
		t.Errorf("Repos = %v, want the frozen restriction carried forward", got.Repos)
	}
	if len(got.Texts) != 2 || got.Texts[0] != "new" {
		t.Errorf("Texts = %v, want the fresh expansion", got.Texts)
	}
	if got.Question != prev.Question {
		t.Errorf("Question = %q, want it unchanged", got.Question)
	}

	// A question that never had a restriction still has none — an empty Repos
	// must not become a phantom entry.
	fresh := refreshTexts(expansion{}, "new question", []string{"x"})
	if len(fresh.Repos) != 0 {
		t.Errorf("Repos = %v, want none for a record that never had one", fresh.Repos)
	}
}

// TestExpandQuestionRepos freezes the understanding step's OTHER output — the
// repositories a question names — onto the existing records, and touches
// nothing else in them.
//
// It exists because the routing arms were measuring a router the product does
// not run. pipeline.go searches with Repos: u.Repos, so a question naming a
// system never sees the other repositories at all; the frozen file recorded
// only Texts, so every published routing row came from a candidate set
// production would not have produced. 9 of the 44 unique and 1 of the 12
// ambiguous questions name a repository outright.
//
// Texts are deliberately NOT regenerated here. They are the frozen input the
// phase 3 expansion and phase 4a gathering documents were measured on, and
// re-rolling them would break the comparison those documents rest on for a
// field they do not use. Costs one short-gate call per question.
func TestExpandQuestionRepos(t *testing.T) {
	requireEval(t)
	base := os.Getenv("BACKEND_LLM_BASE_URL")
	if base == "" {
		t.Skip("BACKEND_LLM_BASE_URL is unset")
	}
	// The records to attach to must already exist: this arm adds a field, it
	// does not create the freeze.
	existing := readExpansions(t)
	byQuestion := map[string]expansion{}
	for _, e := range existing {
		byQuestion[e.Question] = e
	}

	c := llm.NewClient(llm.Config{
		BaseURL: base,
		APIKey:  os.Getenv("BACKEND_LLM_API_KEY"),
		Timeout: 2 * time.Minute,
	}, nil)
	u := ask.NewUnderstander(c)

	var out []expansion
	var failed []string
	named := 0
	for _, q := range loadQuestions(t) {
		rec, ok := byQuestion[q.Text]
		if !ok {
			t.Errorf("no frozen expansion for %q — run TestExpandQuestions first", q.Text)
			continue
		}
		var repos []string
		var last error
		for attempt := 1; attempt <= expandAttempts; attempt++ {
			got, err := u.Understand(context.Background(), q.Text)
			if err == nil {
				repos = got.Repos
				last = nil
				break
			}
			last = err
			t.Logf("RETRY %d/%d %s: %v", attempt, expandAttempts, short(q.Text), err)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if last != nil {
			// The record survives with whatever it already carried. A missing
			// restriction means "the whole corpus", which is the same thing
			// the arms did before this field existed — a gap here degrades
			// the measurement, it does not invalidate the file.
			failed = append(failed, q.Text)
			t.Errorf("understand %q: %v", q.Text, last)
			out = append(out, rec)
			continue
		}
		rec.Repos = repos
		if len(repos) > 0 {
			named++
			t.Logf("REPOS %-60s -> %v", short(q.Text), repos)
		}
		out = append(out, rec)
	}

	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(expansionsFile, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", expansionsFile, err)
	}
	t.Logf("%d of %d questions carry a repo restriction", named, len(out))
	if len(failed) > 0 {
		t.Logf("%d question(s) kept no restriction because understanding failed: %v", len(failed), failed)
	}

	// A name the index does not know is not a narrowing, it is a wipe: Search
	// puts it straight into `WHERE f.repo IN (…)`, so an invented repository
	// returns zero hits and the turn reports "nothing found". Report it here
	// rather than letting it show up later as an unexplained retrieval miss.
	db := evalDB(t, embedDim(t))
	known := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM repo_state`)
	if err != nil {
		t.Fatalf("read repo_state: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan repo_state: %v", err)
		}
		known[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read repo_state: %v", err)
	}
	for _, e := range out {
		for _, name := range e.Repos {
			if !known[name] {
				t.Logf("UNKNOWN REPO %q named for %q — Search would return nothing for this question", name, short(e.Question))
			}
		}
	}
}

// TestEvalMeasureExpansion compares searching the raw question against
// searching the question plus its expansion, on the same index.
//
// The criterion was fixed before the run: the six questions that miss today are
// the target, and no question that works may fall out.
func TestEvalMeasureExpansion(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil)
	r := retrieve.New(db, client)
	expansions := loadExpansions(t)
	questions := loadQuestions(t)

	type row struct {
		q        Question
		raw, exp int
	}
	var rows []row
	var rawR5, expR5, rawR20, expR20 int
	var rawMRR, expMRR float64

	for _, q := range questions {
		texts, ok := expansions[q.Text]
		if !ok {
			t.Fatalf("no expansion recorded for %q", q.Text)
		}
		raw, err := r.Search(ctx, retrieve.Query{Text: q.Text, K: 20})
		if err != nil {
			t.Fatalf("raw search: %v", err)
		}
		exp, err := r.Search(ctx, retrieve.Query{Texts: texts, K: 20})
		if err != nil {
			t.Fatalf("expanded search: %v", err)
		}
		rr, er := rankOfExpected(raw, q), rankOfExpected(exp, q)
		rows = append(rows, row{q: q, raw: rr, exp: er})

		if rr > 0 {
			rawMRR += 1 / float64(rr)
			rawR20++
			if rr <= 5 {
				rawR5++
			}
		}
		if er > 0 {
			expMRR += 1 / float64(er)
			expR20++
			if er <= 5 {
				expR5++
			}
		}
	}

	n := float64(len(questions))
	t.Logf("")
	t.Logf("%-12s %-10s %-10s %s", "arm", "recall@5", "recall@20", "MRR")
	t.Logf("%-12s %-10.3f %-10.3f %.3f", "raw", float64(rawR5)/n, float64(rawR20)/n, rawMRR/n)
	t.Logf("%-12s %-10.3f %-10.3f %.3f", "expanded", float64(expR5)/n, float64(expR20)/n, expMRR/n)

	t.Logf("")
	t.Logf("%-6s %-10s %s", "raw", "expanded", "question")
	sort.SliceStable(rows, func(i, j int) bool {
		return rankOrLast(rows[i].raw) > rankOrLast(rows[j].raw)
	})
	for _, r := range rows {
		t.Logf("%-6d %-10d %s", r.raw, r.exp, short(r.q.Text))
	}
}
