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
}

func loadExpansions(t *testing.T) map[string][]string {
	t.Helper()
	body, err := os.ReadFile(expansionsFile)
	if err != nil {
		t.Skipf("no %s yet; run TestExpandQuestions first", expansionsFile)
	}
	var list []expansion
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse %s: %v", expansionsFile, err)
	}
	out := map[string][]string{}
	for _, e := range list {
		out[e.Question] = e.Texts
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
	previous := map[string][]string{}
	if body, err := os.ReadFile(expansionsFile); err == nil {
		var list []expansion
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("parse %s: %v", expansionsFile, err)
		}
		for _, e := range list {
			previous[e.Question] = e.Texts
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
				// The frozen expansion stays. It is still a measurement of a
				// real expansion, just an older one — which is what freezing
				// them is for.
				out = append(out, expansion{Question: q.Text, Texts: old})
				kept = append(kept, q.Text)
			} else {
				failed = append(failed, q.Text)
			}
			t.Errorf("understand %q: %v", q.Text, last)
			continue
		}
		t.Logf("EXPANDED %-60s -> %v", short(q.Text), texts[1:])
		out = append(out, expansion{Question: q.Text, Texts: texts})
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
