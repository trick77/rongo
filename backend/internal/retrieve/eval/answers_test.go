// The answer-quality arm: the product pipeline end to end, then a judge over
// what it wrote.
//
// Every other arm in this package scores hits, gathered files or routing
// decisions. Nothing read an answer. This does, against a rubric per question
// written from the verified code: the claims a correct answer makes, the
// claims it must not make, and the files it has to cite. It is what a change
// upstream of the answer call is measured by, and it is the model-swap test:
// BACKEND_EVAL_PRO_MODEL and BACKEND_EVAL_GATE_MODEL point the two lanes at
// another deployment for the harness alone, BACKEND_EVAL_GATE_REASONING and
// BACKEND_EVAL_REASONING set that model's policy per lane. The judge runs on
// its own client
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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/sourceview"
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
	// Follows is a question asked FIRST, whose turn this one then continues.
	// Empty is the ordinary case: a first turn, which is what every question
	// in this arm was until follow-ups were measured at all.
	//
	// It exists because a follow-up is the one shape the arm could not see. A
	// question like "and in which step does that happen?" names its subject
	// nowhere, and the pipeline is supposed to carry it from the turn before;
	// running every question against a zero Thread measured the half where
	// there is nothing to carry. The preceding turn is run through the same
	// pipeline, unjudged - it is setup, not a measurement - and what it
	// answers becomes the Thread the measured turn inherits.
	Follows string `json:"follows,omitempty"`
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
	// The policy per lane for the swapped model, the values
	// BACKEND_LLM_GATE_REASONING and BACKEND_LLM_REASONING take in the
	// product; unset keeps the default policy.
	cfg.Policy.GateReasoning = os.Getenv("BACKEND_EVAL_GATE_REASONING")
	cfg.Policy.ProReasoning = os.Getenv("BACKEND_EVAL_REASONING")
	return mustLLM(t, cfg)
}

// judgeLLM is the judge's client: the deployments the product reads, never
// the overrides, so the grader does not change with the candidate.
func judgeLLM(t *testing.T) *llm.Client {
	t.Helper()
	return evalLLM(t, 15*time.Minute)
}

// evalLLMConfig builds the model config the same way the product does: the
// host, the key variable and the opencode identity are all llmwire's, from
// the deployment's provider entry. evalLLM skips when the key is unset.
func evalLLMConfig(t *testing.T, timeout time.Duration) llm.Config {
	t.Helper()
	if os.Getenv("LLMWIRE_MIMO_API_KEY") == "" {
		t.Skip("LLMWIRE_MIMO_API_KEY is unset")
	}
	return llm.Config{Timeout: timeout}
}

// evalLLM is the model client on the product's deployments. A missing
// endpoint variable is llmwire's named error, and fatal: evalLLMConfig has
// already skipped the unset case.
func evalLLM(t *testing.T, timeout time.Duration) *llm.Client {
	t.Helper()
	return mustLLM(t, evalLLMConfig(t, timeout))
}

func mustLLM(t *testing.T, cfg llm.Config) *llm.Client {
	t.Helper()
	c, err := llm.NewClient(cfg, nil)
	if err != nil {
		t.Fatalf("llm.NewClient: %v", err)
	}
	return c
}

// evalEmbedder is the embedding client on llmwire's OpenAI host with the key
// LLMWIRE_OPENAI_API_KEY, read by llmwire; a missing key is its named error. requireEval has already gated the test on a real endpoint.
func evalEmbedder(t *testing.T) *embed.Client {
	t.Helper()
	c, err := embed.NewClient(embed.Config{}, nil)
	if err != nil {
		t.Fatalf("embed.NewClient: %v", err)
	}
	return c
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
	body, _ := llmwire.JSONObject(out)
	var v verdict
	if err := json.Unmarshal([]byte(body), &v); err != nil {
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
	Run      int    `json:"run"`
	Question string `json:"question"`
	// Follows is the question this turn continued, when the rubric named one.
	// Empty on a first turn, which is every question the arm carried before
	// follow-ups were measured.
	Follows   string   `json:"follows,omitempty"`
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
	// Digraphs are the prose words of a German answer the model wrote with
	// ß or an umlaut as ae/oe/ue, as it wrote them (ask.Answer.Respelled).
	// The reader never sees them - swiss.go corrects the stream - so this
	// column is the only place the deployment's German spelling shows.
	Digraphs []string `json:"digraphs,omitempty"`
	// Diagram is the type the answer's diagram fence names, "flowchart",
	// "sequenceDiagram", "stateDiagram-v2", "erDiagram" (ask.DiagramKind),
	// and "" when the reader gets prose alone.
	Diagram string `json:"diagram,omitempty"`
	Err     string `json:"err,omitempty"`
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
	// The product runs every turn with a memory holder on the context
	// (BACKEND_MEMORY defaults on), which is what makes the understanding
	// prompt ask for a standing instruction. Measured as the product ships
	// it: BACKEND_EVAL_MEMORY=0 is the arm without, the prompt as it was.
	// Rules can be seeded for a "memory wins" arm: BACKEND_EVAL_MEMORY_RULES
	// is a "|"-separated list of English sentences written into every turn.
	memoryArm := "off"
	if envOr("BACKEND_EVAL_MEMORY", "1") != "0" {
		var rows []memory.Row
		for i, text := range strings.Split(os.Getenv("BACKEND_EVAL_MEMORY_RULES"), "|") {
			if text = strings.TrimSpace(text); text != "" {
				rows = append(rows, memory.Row{ID: int64(i + 1), Text: text, ScopeLive: true})
			}
		}
		ctx = memory.With(ctx, memory.NewHolder(rows))
		memoryArm = fmt.Sprintf("on, %d rules", len(rows))
	}
	c := answerLLM(t)
	judge := judgeLLM(t)
	retriever := evalRetriever(t, db)
	// The product's retriever, reranker included (main.go). BACKEND_EVAL_RERANK=0
	// measures the fused order the product ran before the reranker shipped.
	rerank := "off (fused order)"
	if envOr("BACKEND_EVAL_RERANK", "1") != "0" {
		rr := evalReranker(t, c)
		retriever.Reranker = rr
		rerank = fmt.Sprintf("pool %d, %d-rune excerpts", rr.Pool, rr.Excerpt)
	}
	opts := gatherOpts(t)
	// The gap pass is harness-only until it is measured, so it is off unless
	// asked for: BACKEND_EVAL_GAP=1 is the arm.
	gatherer := ask.NewGatherer(db, opts)
	if envOr("BACKEND_EVAL_GAP", "0") == "1" {
		gatherer = evalGatherer(t, db, opts, c)
	}
	mo := modules.Opts{MinChunks: envIntOr(t, "BACKEND_MODULE_MIN_CHUNKS", 8), MaxChunks: envIntOr(t, "BACKEND_MODULE_MAX_CHUNKS", 150)}
	pipeline := ask.NewPipeline(c, retriever, gatherer, ask.NewRouter(c, db, routeMargin(t), mo))
	// The product's process listing (main.go): the model files are read from
	// the checkouts the index arm cloned. BACKEND_EVAL_PROCESSES=0 measures
	// the answer without it.
	if envOr("BACKEND_EVAL_PROCESSES", "1") != "0" {
		gitBin, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("git: %v", err)
		}
		gitc := gitrepo.New(gitBin, envOr("BACKEND_REPO_ROOT", "/tmp/rongo-eval-repos"))
		pipeline.WithModels(sourceview.New(db, gitc, 1<<20))
	}

	questions := loadFlowQuestions(t)
	rubrics := loadRubrics(t)
	runs := envIntOr(t, "BACKEND_EVAL_ANSWER_RUNS", 2)
	audience := ask.Audience(envOr("BACKEND_EVAL_AUDIENCE", string(ask.AudienceBA)))

	out := filepath.Join(os.TempDir(), fmt.Sprintf("rongo-answers-%s.json", time.Now().Format("20060102-150405")))
	var records []answerRecord
	for run := 1; run <= runs; run++ {
		var present, must, contra, asserted, citeHit, citeTotal, tokens, asked, failed, digraphs, diagrams int
		t.Logf("\n=== run %d of %d, audience %s, rerank %s%s, memory %s ===", run, runs, audience, rerank, codeLaneLabel(), memoryArm)
		for _, q := range questions {
			r, ok := rubrics[q.Text]
			if !ok {
				t.Fatalf("no rubric for %q in %s", q.Text, rubricsFile())
			}
			rec := answerRecord{Run: run, Question: q.Text}
			lang := ask.ParseLanguage(r.Lang)
			// A rubric naming a preceding question is measured as the SECOND
			// turn of a thread: the first is run to have something to follow,
			// and only what it leaves behind - the question, the answer and
			// the sources it was written from - reaches the turn under test.
			// A setup turn that fails or asks back is reported and the
			// question skipped, never measured against a thread that is not
			// the one the rubric describes.
			thread := ask.Thread{}
			if r.Follows != "" {
				// In ITS own language, which need not be the follow-up's: the
				// preceding turn is a real turn of the corpus and has a
				// rubric of its own saying what it was asked in. A thread
				// keeps one language in the product, and these two-turn pairs
				// deliberately cross it to check the subject survives the
				// crossing rather than riding on shared wording.
				prevLang := lang
				if pr, ok := rubrics[r.Follows]; ok && pr.Lang != "" {
					prevLang = ask.ParseLanguage(pr.Lang)
				}
				prev, prevClar, perr := pipeline.Run(ctx, r.Follows, audience, prevLang, ask.Thread{}, ask.Events{})
				switch {
				case perr != nil:
					rec.Err = "preceding turn: " + perr.Error()
					failed++
					t.Logf("  %-70s SETUP FAILED: %v", short(q.Text), perr)
					records = append(records, rec)
					continue
				case prevClar != nil:
					rec.Err = "preceding turn asked back"
					failed++
					t.Logf("  %-70s SETUP ASKED BACK", short(q.Text))
					records = append(records, rec)
					continue
				}
				thread = ask.Thread{
					Question:     r.Follows,
					Answer:       prev.Text,
					Sources:      prev.Sources,
					SourcesTotal: len(prev.Sources),
				}
				rec.Follows = r.Follows
				tokens += prev.Usage.Total
			}
			a, clar, err := pipeline.Run(ctx, q.Text, audience, lang, thread, ask.Events{})
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
			if lang == ask.LanguageDE {
				rec.Digraphs = a.Respelled
				digraphs += len(rec.Digraphs)
			}
			rec.Diagram = ask.DiagramKind(a.Text)
			if rec.Diagram != "" {
				diagrams++
			}
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
			t.Logf("  %-70s rubric %d/%d present, %d contradicted, %d forbidden; cited %d/%d; %d sources; %d tokens; digraphs %d %v; diagram %q",
				short(q.Text), rec.Present, len(r.Must), rec.Contra, rec.Asserted, rec.CiteHit, rec.CiteTotal, rec.Sources, rec.Tokens, len(rec.Digraphs), rec.Digraphs, rec.Diagram)
			records = append(records, rec)
		}
		t.Logf("  run %d: rubric %d/%d present, %d contradicted, %d forbidden asserted; cited parts %d/%d; asked %d; failed %d; %d tokens; digraphs %d; diagrams %d",
			run, present, must, contra, asserted, citeHit, citeTotal, asked, failed, tokens, digraphs, diagrams)
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
