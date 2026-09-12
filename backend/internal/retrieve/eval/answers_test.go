// The answer-quality arm: the product pipeline end to end, then a judge over
// what it wrote.
//
// Every other arm in this package scores hits, gathered files or routing
// decisions. Nothing read an answer. This does, against a rubric per question
// written from the verified code: the claims a correct answer makes, the
// claims it must not make, and the files it has to cite. It is what a change
// upstream of the answer call is measured by, and it is the model-swap test:
// BACKEND_EVAL_PRO_MODEL and BACKEND_EVAL_GATE_MODEL point the two lanes at
// another deployment for the harness alone. The judge runs on its own client
// without those overrides, so a swapped gate model is graded by the same
// judge as the baseline.
//
//	hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'
//	BACKEND_EVAL_ANSWER_RUNS=1 hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'
//
// The judge is a model, and a model at temperature zero still re-rolls one or
// two questions in sixty (docs/measurements/2026-09-06-routing-rerun.md), so
// the arm runs twice by default and reports both runs side by side. Nothing
// here is concluded from a one-question gap.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/retrieve"
)

// Rubric is what a correct answer to one question has to say, written from
// the verified code beside the question it belongs to.
type Rubric struct {
	Question string `json:"question"`
	// Lang is the answer language the question is asked in: "de" or "en".
	Lang string `json:"lang"`
	// Must are claims a correct answer makes, one sentence each.
	Must []string `json:"must"`
	// MustNot are claims a wrong answer makes — the confident-but-incomplete
	// reading the flow corpus was built to expose.
	MustNot []string `json:"must_not,omitempty"`
	// Cite are the files the answer has to cite; empty means the question's
	// candidate paths.
	Cite []flowPart `json:"cite,omitempty"`
}

// rubricsFile is where the flow corpus's rubrics live. BACKEND_EVAL_RUBRICS
// points at another corpus's file.
func rubricsFile() string { return envOr("BACKEND_EVAL_RUBRICS", "flow-rubrics.json") }

func loadRubrics(t *testing.T) map[string]Rubric {
	t.Helper()
	body, err := os.ReadFile(rubricsFile())
	if err != nil {
		t.Skipf("no rubrics at %s: %v", rubricsFile(), err)
	}
	var list []Rubric
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse %s: %v", rubricsFile(), err)
	}
	out := map[string]Rubric{}
	for _, r := range list {
		out[r.Question] = r
	}
	return out
}

// answerLLM is the client every pipeline call goes through, with the
// harness-only lane overrides applied.
func answerLLM(t *testing.T) *llm.Client {
	t.Helper()
	cfg := evalLLMConfig(t, 15*time.Minute)
	cfg.Pro = os.Getenv("BACKEND_EVAL_PRO_MODEL")
	cfg.ShortGate = os.Getenv("BACKEND_EVAL_GATE_MODEL")
	return llm.NewClient(cfg, nil)
}

// judgeLLM is the judge's client: the deployments the product reads, never
// the overrides, so the grader does not change with the candidate.
func judgeLLM(t *testing.T) *llm.Client {
	t.Helper()
	return llm.NewClient(evalLLMConfig(t, 15*time.Minute), nil)
}

// evalLLMConfig is the one place the harness reads the model endpoint from
// the environment, the way config.Load does for the product: the base URL
// skips the test when unset, the opencode flag is mandatory and must be a
// boolean. A harness that silently sent llmwire's own client string to the
// token-plan host would measure a bot's welcome, not the model.
func evalLLMConfig(t *testing.T, timeout time.Duration) llm.Config {
	t.Helper()
	base := os.Getenv("BACKEND_LLM_BASE_URL")
	if base == "" {
		t.Skip("BACKEND_LLM_BASE_URL is unset")
	}
	raw := strings.TrimSpace(os.Getenv("BACKEND_LLM_EMULATE_OPENCODE"))
	if raw == "" {
		t.Fatal("BACKEND_LLM_EMULATE_OPENCODE is required: true or false, the same as for the product")
	}
	emulate, err := strconv.ParseBool(raw)
	if err != nil {
		t.Fatalf("BACKEND_LLM_EMULATE_OPENCODE=%q is not a boolean; want true or false", raw)
	}
	return llm.Config{
		BaseURL:         base,
		APIKey:          os.Getenv("BACKEND_LLM_API_KEY"),
		Timeout:         timeout,
		IdleTimeout:     90 * time.Second,
		EmulateOpenCode: emulate,
	}
}

// verdict is the judge's reply: one word per rubric line.
type verdict struct {
	Must    []string `json:"must"`
	MustNot []string `json:"must_not"`
}

const judgeAnswerSystem = `You grade an answer about a codebase against a rubric. Answer with JSON ONLY:
{"must":["present"|"absent"|"contradicted", ...], "must_not":["asserted"|"absent", ...]}

must: one entry per rubric claim, in order.
  present       the answer states this claim, in its own words.
  absent        the answer does not say it.
  contradicted  the answer says the opposite.
must_not: one entry per forbidden claim, in order.
  asserted      the answer makes this claim.
  absent        it does not.

Judge only what the answer says. Do not reward hedging, do not penalise
wording, and do not use your own knowledge of any codebase.`

// judgeAnswer grades one answer. A reply that fails to parse counts every
// line as absent and is reported: a judge that cannot be read must never
// look like a passing grade.
func judgeAnswer(ctx context.Context, c *llm.Client, r Rubric, answer string) (verdict, error) {
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
	out, _, err := c.Complete(ctx, []llm.Message{
		{Role: "system", Content: judgeAnswerSystem},
		{Role: "user", Content: b.String()},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(0), llm.WithMaxTokens(512), llm.WithStep("judge-answer"))
	if err != nil {
		return verdict{}, err
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "```") {
		out = strings.TrimSuffix(strings.TrimSpace(out[strings.IndexByte(out, '\n')+1:]), "```")
	}
	var v verdict
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return verdict{}, fmt.Errorf("judge reply was not JSON: %w: %s", err, excerpt(out, 200))
	}
	return v, nil
}

func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// answerRecord is one answered question, kept for reading after the run:
// the numbers say how much was right, the text says what was wrong.
type answerRecord struct {
	Run       int      `json:"run"`
	Question  string   `json:"question"`
	Asked     bool     `json:"asked"`
	Answer    string   `json:"answer"`
	Cited     []string `json:"cited"`
	Must      []string `json:"must"`
	MustNot   []string `json:"must_not"`
	Present   int      `json:"present"`
	Contra    int      `json:"contradicted"`
	Asserted  int      `json:"asserted"`
	CiteHit   int      `json:"cite_hit"`
	CiteTotal int      `json:"cite_total"`
	Tokens    int      `json:"tokens"`
	Sources   int      `json:"sources"`
	Err       string   `json:"err,omitempty"`
}

// TestEvalMeasureAnswers runs the product pipeline over the corpus's
// questions, grades every answer against its rubric, and reports per run:
// rubric claims present, contradicted, forbidden claims asserted, cited
// parts, tokens. Answers are written to a JSON file named in the log.
func TestEvalMeasureAnswers(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	c := answerLLM(t)
	judge := judgeLLM(t)
	retriever := retrieve.New(db, embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil))
	// The product's retriever, reranker included (main.go). BACKEND_EVAL_RERANK=0
	// measures the fused order the product ran before the reranker shipped.
	if envOr("BACKEND_EVAL_RERANK", "1") != "0" {
		retriever.Reranker = evalReranker(t, c)
	}
	opts := gatherOpts(t)
	mo := modules.Opts{MinChunks: envIntOr(t, "BACKEND_MODULE_MIN_CHUNKS", 8), MaxChunks: envIntOr(t, "BACKEND_MODULE_MAX_CHUNKS", 150)}
	pipeline := ask.NewPipeline(c, retriever, ask.NewGatherer(db, opts), ask.NewRouter(c, db, routeMargin(t), mo))

	questions := loadFlowQuestions(t)
	rubrics := loadRubrics(t)
	runs := envIntOr(t, "BACKEND_EVAL_ANSWER_RUNS", 2)
	audience := ask.Audience(envOr("BACKEND_EVAL_AUDIENCE", string(ask.AudienceBA)))

	out := filepath.Join(os.TempDir(), fmt.Sprintf("rongo-answers-%s.json", time.Now().Format("20060102-150405")))
	var records []answerRecord
	for run := 1; run <= runs; run++ {
		var present, must, contra, asserted, citeHit, citeTotal, tokens, asked, failed int
		t.Logf("\n=== run %d of %d, audience %s ===", run, runs, audience)
		for _, q := range questions {
			r, ok := rubrics[q.Text]
			if !ok {
				t.Fatalf("no rubric for %q in %s", q.Text, rubricsFile())
			}
			rec := answerRecord{Run: run, Question: q.Text}
			lang := ask.ParseLanguage(r.Lang)
			a, clar, err := pipeline.Run(ctx, q.Text, audience, lang, ask.Thread{}, ask.Events{})
			if err != nil {
				rec.Err = err.Error()
				failed++
				t.Logf("  %-70s FAILED: %v", short(q.Text), err)
				records = append(records, rec)
				continue
			}
			if clar != nil {
				rec.Asked = true
				asked++
				t.Logf("  %-70s asked back (%d candidates)", short(q.Text), len(clar.Candidates))
				records = append(records, rec)
				continue
			}
			rec.Answer = a.Text
			rec.Tokens = a.Usage.Total
			rec.Sources = len(a.Sources)
			tokens += a.Usage.Total
			for _, cit := range a.Citations {
				rec.Cited = append(rec.Cited, cit.Repo+"/"+cit.Path)
			}
			want := r.Cite
			if len(want) == 0 {
				want = q.parts()
			}
			rec.CiteTotal = len(want)
			for _, p := range want {
				for _, cit := range a.Citations {
					if cit.Repo == p.Repo && cit.Path == p.Path {
						rec.CiteHit++
						break
					}
				}
			}
			v, err := judgeAnswer(ctx, judge, r, a.Text)
			if err != nil {
				rec.Err = "judge: " + err.Error()
				t.Logf("  %-70s judge failed: %v", short(q.Text), err)
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
			citeHit += rec.CiteHit
			citeTotal += rec.CiteTotal
			t.Logf("  %-70s rubric %d/%d present, %d contradicted, %d forbidden; cited %d/%d; %d sources; %d tokens",
				short(q.Text), rec.Present, len(r.Must), rec.Contra, rec.Asserted, rec.CiteHit, rec.CiteTotal, rec.Sources, rec.Tokens)
			records = append(records, rec)
		}
		t.Logf("  run %d: rubric %d/%d present, %d contradicted, %d forbidden asserted; cited parts %d/%d; asked %d; failed %d; %d tokens",
			run, present, must, contra, asserted, citeHit, citeTotal, asked, failed, tokens)
	}
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("answers written to %s", out)
}
