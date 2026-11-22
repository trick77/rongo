package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trick77/toolkit/llmwire"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/llm"
)

// The diagram prompt arm: the answer prompt alone, on a FIXED context. Each
// question of diagram-questions.json is answered from the hand-picked
// excerpts frozen for it in diagram-context.json (Sock Shop at the flow
// corpus's pins), through the product's Answerer, so the system prompt is
// exactly the one the checkout builds. No index, no retrieval, no
// embedding call anywhere on the path: what differs between two checkouts
// is the prompt, and nothing else (2026-11-22-lanes-and-classes.md).
//
// Every answer is judged against its rubric on the product's gate model,
// as TestEvalMeasureAnswers judges. Spend is capped: before each call the
// run stops once the spend so far reaches BACKEND_EVAL_COST_CAP_USD, or the
// tokens reach BACKEND_EVAL_TOKEN_CAP. The spend is what llmwire priced,
// plus, for a call it could not price, the tokens at the operator's
// BACKEND_EVAL_RATE_IN_PER_M and _OUT_PER_M. The last log line, "SPENT arm
// …", is the arm's total. The answers are written to BACKEND_EVAL_OUT_DIR (default the
// temp dir) as diagram-prompt-<BACKEND_EVAL_ARM>.json, which
// TestEvalTallyDiagramShapes reads.
//
//	BACKEND_EVAL=1 BACKEND_LLM_MODEL=… BACKEND_LLM_GATE_MODEL=… go test ./internal/retrieve/eval/ -run 'TestEvalMeasureDiagramPrompt$' -v -count=1
//
// The file builds against an older checkout's harness too, so the baseline
// arm is this file and its three JSON files (diagram-questions.json,
// diagram-rubrics.json, diagram-context.json) copied into a worktree of the
// base commit.

// contextSource is one frozen excerpt.
type contextSource struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
}

type fixedContext struct {
	Question string          `json:"question"`
	Sources  []contextSource `json:"sources"`
}

func diagramContextFile() string {
	return envOr("BACKEND_EVAL_DIAGRAM_CONTEXT", "diagram-context.json")
}

// loadFixedContext is the frozen sources per question, as the Answerer
// takes them.
func loadFixedContext(t *testing.T) map[string][]ask.Source {
	t.Helper()
	body, err := os.ReadFile(diagramContextFile())
	if err != nil {
		t.Fatalf("read %s: %v", diagramContextFile(), err)
	}
	var list []fixedContext
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse %s: %v", diagramContextFile(), err)
	}
	out := map[string][]ask.Source{}
	for _, c := range list {
		for _, s := range c.Sources {
			out[c.Question] = append(out[c.Question], ask.Source{
				Repo: s.Repo, Path: s.Path, StartLine: s.StartLine, EndLine: s.EndLine,
				Text: s.Text, Reason: "hit",
			})
		}
	}
	return out
}

// meter is what the arm has spent: tokens in and out, what llmwire priced,
// and for a call llmwire could not price (a subscription plan has no rate in
// its table) an estimate at the operator's rates per million tokens, which
// come from the environment, never from this code.
type meter struct {
	in, out  int
	nanoUSD  int64
	unpriced int
	estUSD   float64
	// rateIn, rateOut are dollars per million tokens for an unpriced call.
	rateIn, rateOut float64
}

func (m *meter) add(u llm.Usage) {
	m.in += u.Prompt
	m.out += u.Completion
	if u.CostNanoUSD != nil {
		m.nanoUSD += *u.CostNanoUSD
		return
	}
	m.unpriced++
	m.estUSD += (float64(u.Prompt)*m.rateIn + float64(u.Completion)*m.rateOut) / 1e6
}

// usd is the spend: priced plus estimated.
func (m *meter) usd() float64 { return float64(m.nanoUSD)/1e9 + m.estUSD }

func (m *meter) String() string {
	return fmt.Sprintf("%d tokens in, %d out; $%.4f (llmwire priced $%.4f, %d calls unpriced, estimated $%.4f)",
		m.in, m.out, m.usd(), float64(m.nanoUSD)/1e9, m.unpriced, m.estUSD)
}

// over says the cap is reached: no further call is made.
func (m *meter) over(capUSD float64, capTokens int) bool {
	if capUSD > 0 && m.usd() >= capUSD {
		return true
	}
	return capTokens > 0 && m.in+m.out >= capTokens
}

func TestMeter_stopsAtEitherCap(t *testing.T) {
	cost := int64(100_000_000) // $0.10
	m := &meter{rateIn: 0.5, rateOut: 2}
	m.add(llm.Usage{Prompt: 800, Completion: 100, CostNanoUSD: &cost})
	m.add(llm.Usage{Prompt: 20_000, Completion: 20_000})
	// 20k in at $0.50/M + 20k out at $2/M = $0.05, beside the priced $0.10.
	if m.in != 20_800 || m.out != 20_100 || m.unpriced != 1 || m.usd() < 0.1499 || m.usd() > 0.1501 {
		t.Fatalf("meter = %+v, usd %f", *m, m.usd())
	}
	for name, tc := range map[string]struct {
		usd    float64
		tokens int
		want   bool
	}{
		"under both":   {0.20, 50_000, false},
		"cost reached": {0.15, 50_000, true},
		"tokens":       {0.20, 40_900, true},
		"no caps":      {0, 0, false},
	} {
		if got := m.over(tc.usd, tc.tokens); got != tc.want {
			t.Errorf("%s: over = %v, want %v", name, got, tc.want)
		}
	}
	want := "20800 tokens in, 20100 out; $0.1500 (llmwire priced $0.1000, 1 calls unpriced, estimated $0.0500)"
	if got := m.String(); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

// A cap or a rate that is not a finite, non-negative number never guards
// spend: NaN compares false to everything, Inf never trips.
func TestParseDecimal_refusesWhatCannotCap(t *testing.T) {
	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "-1", "x", "0.4x"} {
		if _, err := parseDecimal(bad); err == nil {
			t.Errorf("parseDecimal(%q) accepted", bad)
		}
	}
	if f, err := parseDecimal("0.40"); err != nil || f != 0.4 {
		t.Errorf("parseDecimal(0.40) = %v, %v", f, err)
	}
}

// envFloat is a decimal setting, 0 when unset.
func envFloat(t *testing.T, key string) float64 {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	f, err := parseDecimal(v)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, v, err)
	}
	return f
}

// parseDecimal is a finite, non-negative decimal.
func parseDecimal(v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("want a finite decimal of 0 or more")
	}
	return f, nil
}

// The frozen context covers every diagram question, and every excerpt says
// where it came from: a question without one would be answered from
// nothing, which the Answerer turns into the fixed nothing-found text.
func TestDiagramContextCoversEveryQuestion(t *testing.T) {
	t.Setenv("BACKEND_EVAL_FLOW_QUESTIONS", "diagram-questions.json")
	ctx := loadFixedContext(t)
	for _, q := range loadFlowQuestions(t) {
		srcs := ctx[q.Text]
		if len(srcs) == 0 {
			t.Errorf("%q has no frozen context", q.Text)
		}
		for _, s := range srcs {
			if s.Repo == "" || s.Path == "" || s.StartLine < 1 || s.EndLine < s.StartLine || s.Text == "" {
				t.Errorf("%q: excerpt %s/%s:%d-%d is incomplete", q.Text, s.Repo, s.Path, s.StartLine, s.EndLine)
			}
		}
	}
}

func TestEvalMeasureDiagramPrompt(t *testing.T) {
	requireEval(t)
	t.Setenv("BACKEND_EVAL_FLOW_QUESTIONS", envOr("BACKEND_EVAL_FLOW_QUESTIONS", "diagram-questions.json"))
	t.Setenv("BACKEND_EVAL_RUBRICS", envOr("BACKEND_EVAL_RUBRICS", "diagram-rubrics.json"))
	questions := loadFlowQuestions(t)
	rubrics := loadRubrics(t)
	sources := loadFixedContext(t)
	c := answerLLM(t)
	judge := judgeLLM(t)
	answerer := ask.NewAnswerer(c)
	runs := envIntOr(t, "BACKEND_EVAL_ANSWER_RUNS", 2)
	audience := ask.Audience(envOr("BACKEND_EVAL_AUDIENCE", string(ask.AudienceBA)))
	arm := envOr("BACKEND_EVAL_ARM", "arm")
	capTokens := envIntOr(t, "BACKEND_EVAL_TOKEN_CAP", 0)
	capUSD := envFloat(t, "BACKEND_EVAL_COST_CAP_USD")
	if capUSD == 0 && capTokens == 0 {
		t.Fatal("set BACKEND_EVAL_COST_CAP_USD or BACKEND_EVAL_TOKEN_CAP: this arm spends money")
	}
	ctx := context.Background()
	spent := &meter{
		rateIn:  envFloat(t, "BACKEND_EVAL_RATE_IN_PER_M"),
		rateOut: envFloat(t, "BACKEND_EVAL_RATE_OUT_PER_M"),
	}
	var records []answerRecord
	stopped := false
	t.Logf("arm %s, audience %s, %d runs, cap $%.2f / %d tokens", arm, audience, runs, capUSD, capTokens)
	for run := 1; run <= runs && !stopped; run++ {
		var present, must, contra, asserted, diagrams int
		for _, q := range questions {
			if spent.over(capUSD, capTokens) {
				t.Logf("  CAP REACHED before %q: %s", short(q.Text), spent)
				stopped = true
				break
			}
			r, ok := rubrics[q.Text]
			if !ok {
				t.Fatalf("no rubric for %q", q.Text)
			}
			rec := answerRecord{Run: run, Question: q.Text}
			a, err := answerer.Answer(ctx, q.Text, audience, ask.ParseLanguage(r.Lang), sources[q.Text], ask.Scope{}, ask.FollowUp{}, nil)
			spent.add(a.Usage)
			if err != nil {
				rec.Err = err.Error()
				t.Logf("  %-70s FAILED: %v", short(q.Text), err)
				records = append(records, rec)
				continue
			}
			rec.Answer, rec.Tokens = a.Text, a.Usage.Total
			rec.Diagram = ask.DiagramKind(a.Text)
			if rec.Diagram != "" {
				diagrams++
			}
			// The judge is a call too: the cap is asked again before it, and
			// an answer it stops at is kept, unjudged.
			if spent.over(capUSD, capTokens) {
				rec.Err = "cap reached before the judge"
				records = append(records, rec)
				t.Logf("  CAP REACHED before judging %q: %s", short(q.Text), spent)
				stopped = true
				break
			}
			v, ju, err := judgeDiagramAnswer(ctx, judge, r, a.Text)
			spent.add(ju)
			if err != nil {
				rec.Err = "judge: " + err.Error()
			}
			rec.Must, rec.MustNot = v.Must, v.MustNot
			for _, m := range v.Must {
				switch m {
				case "present":
					rec.Present++
				case "contradicted":
					rec.Contra++
				}
			}
			for _, m := range v.MustNot {
				if m == "asserted" {
					rec.Asserted++
				}
			}
			present += rec.Present
			must += len(r.Must)
			contra += rec.Contra
			asserted += rec.Asserted
			records = append(records, rec)
			t.Logf("  %-70s rubric %d/%d, %d contradicted, %d forbidden; diagram %q; %d tokens",
				short(q.Text), rec.Present, len(r.Must), rec.Contra, rec.Asserted, rec.Diagram, rec.Tokens)
		}
		t.Logf("  run %d: rubric %d/%d present, %d contradicted, %d forbidden; diagrams %d; spent so far %s",
			run, present, must, contra, asserted, diagrams, spent)
	}
	t.Logf("SPENT arm %s: %s", arm, spent)
	dir := envOr("BACKEND_EVAL_OUT_DIR", os.TempDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "diagram-prompt-"+arm+".json")
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("answers written to %s", out)
}

// judgeDiagramAnswer is judgeAnswer's call with the judge's usage kept, so
// the cap counts the judge too. A copy rather than a change to judgeAnswer:
// the baseline arm builds this file against the base commit's harness.
func judgeDiagramAnswer(ctx context.Context, c *llm.Client, r Rubric, answer string) (verdict, llm.Usage, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nAnswer:\n%s\n\nRubric, claims a correct answer makes:\n", r.Question, answer)
	for i, m := range r.Must {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m)
	}
	b.WriteString("\nForbidden claims:\n")
	if len(r.MustNot) == 0 {
		b.WriteString("(none)\n")
	}
	for i, m := range r.MustNot {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m)
	}
	out, u, err := c.Complete(ctx, []llm.Message{
		{Role: "system", Content: judgeAnswerSystem},
		{Role: "user", Content: b.String()},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithGateTemperature(), llm.WithMaxAnswerTokens(512), llm.WithStep("judge-answer"))
	if err != nil {
		return verdict{}, u, err
	}
	body, _ := llmwire.JSONObject(out)
	var v verdict
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return verdict{}, u, fmt.Errorf("judge reply was not JSON: %w: %s", err, excerpt(out, 200))
	}
	return v, u, nil
}
