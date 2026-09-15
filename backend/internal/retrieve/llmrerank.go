package retrieve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/llmwire"

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
	// Excerpt is how many runes of each chunk the model reads; zero means
	// rerankExcerpt. The harness sweeps it, the product keeps the default.
	Excerpt int
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

// rerankExcerpt is how much of each chunk the model sees by default, in
// runes. Eight hundred, not the 240 this shipped with: a signature and its
// doc comment are regularly longer than 240, so the model was ranking on a
// truncated first line. Measured twice on the pinned Go corpus, unique
// gathered 39/42 at 240 against 41/42 at 800, MRR 0.72 against 0.81 and 0.83
// (docs/measurements/2026-09-16-rerank-excerpt.md).
const rerankExcerpt = 800

// rerankMaxTokens is the floor under the reply cap, and rerankTokensPerHit
// what each result in the pool adds on top. The floor covers a short list; the
// shipped pool of sixty is already past it and sends 304. Without the growth a
// deeper pool would end with finish_reason=length, which is an error, a
// warning and the fused order — the reranker doing nothing while looking like
// it ran.
const (
	rerankMaxTokens    = 256
	rerankTokensPerHit = 4
	rerankTokensBase   = 64
)

// replyCap is how many tokens the reply may take for a pool of n results.
func replyCap(n int) int {
	return max(rerankMaxTokens, rerankTokensBase+rerankTokensPerHit*n)
}

// excerptWidth is Excerpt, or the default when none was set.
func (r *LLMReranker) excerptWidth() int {
	if r.Excerpt > 0 {
		return r.Excerpt
	}
	return rerankExcerpt
}

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
	width := r.excerptWidth()
	fmt.Fprintf(&b, "Question: %s\n\nResults:\n", question)
	for i, h := range hits {
		fmt.Fprintf(&b, "\n[%d] %s %s", i+1, h.Repo, h.Path)
		// Two chunks of one file differ by where they start, and a header
		// without the line cannot tell them apart.
		if h.StartLine > 0 {
			fmt.Fprintf(&b, ":%d", h.StartLine)
		}
		if h.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", h.Symbol)
		}
		b.WriteString("\n")
		b.WriteString(excerpt(h.RawText, width))
		b.WriteString("\n")
	}
	out, _, err := r.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(rerankSystem, k)},
		{Role: "user", Content: b.String()},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(0), llm.WithMaxTokens(replyCap(len(hits))), llm.WithStep("rerank"))
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
	body, _ := llmwire.JSONObject(out)
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		r.logger().Warn("rerank reply was not JSON; fused order kept", "reply", head(out, 120))
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

// head is the first n RUNES of s, cut where the window ends and nowhere else.
// Runes, not bytes: a byte cut splits an umlaut and hands the reader a broken
// rune.
func head(s string, n int) string {
	s = strings.TrimSpace(s)
	_, end := runeOffsets(s, n)
	if end < 0 {
		return s
	}
	return s[:end] + "…"
}

// excerpt is head backed off to the last line break in the back half of the
// window, so the model reads whole lines. The back-half floor is what keeps a
// long first line from yielding nothing at all. A warning logs with head
// instead: a malformed reply's broken JSON sits after the prose line, and
// backing off would log the prose alone.
//
// It walks bytes rather than building a []rune: a chunk runs to a few
// thousand runes and a pool to a hundred of them, so the slice was one
// allocation per hit for a prefix of a few hundred.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	half, end := runeOffsets(s, n)
	if end < 0 {
		return s
	}
	cutAt := end
	// A newline is one byte and cannot hide inside a multi-byte rune, so the
	// byte search over the window is the rune search over it.
	if nl := strings.LastIndexByte(s[half:end], '\n'); nl >= 0 {
		cutAt = half + nl
	}
	return s[:cutAt] + "…"
}

// runeOffsets reports the byte offsets of rune n/2 and rune n in s. end is -1
// when s holds n runes or fewer, which is the whole-text case.
func runeOffsets(s string, n int) (half, end int) {
	half, end = 0, -1
	i := 0
	for off := range s {
		if i == n/2 {
			half = off
		}
		if i == n {
			end = off
			break
		}
		i++
	}
	return half, end
}
