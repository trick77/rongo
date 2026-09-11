package retrieve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/rongo/internal/llm"
)

// LLMReranker reorders a deep fused list with one short-gate call, so a chunk
// the lanes ranked thirtieth can still make the cut of twenty.
//
// Why it exists: TestEvalRankOfMisses found six of the seven unique questions
// the search misses at K=20 sitting between rank 21 and 49 of the fused list.
// Fusion ranks by lane agreement, and a file the vector lane places at 30 with
// no keyword support stays at 30 whatever it says. A model reading the
// question against the chunk headers is a different judgement — the one a
// developer makes scanning search results — and it stores nothing: per turn,
// never indexed, never embedded, which keeps it outside the rule against
// model-written text about code.
//
// Measured and shipped (docs/measurements/2026-09-11-arms-after-the-crossing.md):
// the product sets Reranker in main.go, and internal/retrieve/eval can turn it
// off to read the fused-order baseline. The fused list is always in hand, so
// the reranker never fails a search: a call that errors or a reply that
// cannot be read keeps the fused order and says so in the log.
type LLMReranker struct {
	llm *llm.Client
	// Pool is how deep the fused list goes before the model sees it. Sixty is
	// where the rank diagnostic put every reachable miss.
	Pool int
	// Log receives the warning when the fused order is kept; nil means the
	// default logger.
	Log *slog.Logger
}

// logger is Log, or the default logger when none was set.
func (r *LLMReranker) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// NewLLMReranker builds a reranker over the short-gate lane.
func NewLLMReranker(c *llm.Client, pool int) *LLMReranker {
	if pool <= 0 {
		pool = 60
	}
	return &LLMReranker{llm: c, Pool: pool}
}

const rerankSystem = `You rank code search results for a question. Answer with JSON ONLY:
{"relevant":[<numbers>]}

The numbers are the results that help answer the question, most relevant
first, at most %d of them. Leave out results that do not help. A result helps
when its code, comment or path is about what the question asks, not when it
merely shares a word with it. Do not explain.`

// rerankExcerpt is how much of each chunk the model sees: the header and the
// opening lines, which is where a doc comment and a signature sit.
const rerankExcerpt = 240

// rerankMaxTokens caps the reply. A list of at most sixty numbers.
const rerankMaxTokens = 256

// Rerank returns hits reordered: the ones the model called relevant first, in
// the model's order, then everything else in the fused order, cut to k. A
// reply that cannot be read leaves the list as it was — the fused order is
// the measured baseline, and a reranker that fails must not do worse than
// nothing.
func (r *LLMReranker) Rerank(ctx context.Context, question string, hits []Hit, k int) ([]Hit, error) {
	if len(hits) <= 1 {
		return cut(hits, k), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nResults:\n", question)
	for i, h := range hits {
		fmt.Fprintf(&b, "\n[%d] %s %s", i+1, h.Repo, h.Path)
		if h.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", h.Symbol)
		}
		b.WriteString("\n")
		b.WriteString(excerpt(h.RawText, rerankExcerpt))
		b.WriteString("\n")
	}
	out, _, err := r.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(rerankSystem, k)},
		{Role: "user", Content: b.String()},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(0), llm.WithMaxTokens(rerankMaxTokens), llm.WithStep("rerank"))
	if err != nil {
		if ctx.Err() != nil {
			// The reader left; there is no turn to keep an order for.
			return nil, ctx.Err()
		}
		// The fused order answered every question before the reranker
		// existed; a gate-lane outage must not turn a search into an error.
		r.logger().Warn("rerank call failed; fused order kept", "err", err)
		return cut(hits, k), nil
	}
	var reply struct {
		Relevant []int `json:"relevant"`
	}
	if err := json.Unmarshal([]byte(stripFence(out)), &reply); err != nil {
		r.logger().Warn("rerank reply was not JSON; fused order kept", "reply", excerpt(out, 120))
		return cut(hits, k), nil
	}
	taken := make([]bool, len(hits))
	ranked := make([]Hit, 0, len(hits))
	for _, n := range reply.Relevant {
		if n < 1 || n > len(hits) || taken[n-1] {
			continue
		}
		taken[n-1] = true
		ranked = append(ranked, hits[n-1])
	}
	for i, h := range hits {
		if !taken[i] {
			ranked = append(ranked, h)
		}
	}
	return cut(ranked, k), nil
}

func cut(hits []Hit, k int) []Hit {
	if k > 0 && len(hits) > k {
		return append(make([]Hit, 0, k), hits[:k]...)
	}
	return hits
}

func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// stripFence unwraps a ```json fence, the way internal/ask does for its own
// JSON replies.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
