package eval

import (
	"context"
	"log/slog"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// evalReranker is the product's reranker with one difference: a call that
// fails or a reply that cannot be read fails the test instead of keeping the
// fused order. The product's fallback is right for a reader; in a
// measurement it would let a dead gate lane or a misspelled model name
// report the baseline as the reranked arm.
func evalReranker(t *testing.T, c *llm.Client) *retrieve.LLMReranker {
	t.Helper()
	pool := envIntOr(t, "BACKEND_EVAL_RERANK_POOL", retrieve.DefaultRerankPool)
	excerpt := envIntOr(t, "BACKEND_EVAL_RERANK_EXCERPT", retrieve.DefaultRerankExcerpt)
	// A zero would be silently replaced by the default while the arm's label
	// still read "0-rune excerpts" — a measurement reporting the wrong arm.
	if pool <= 0 || excerpt <= 0 {
		t.Fatalf("BACKEND_EVAL_RERANK_POOL = %d, BACKEND_EVAL_RERANK_EXCERPT = %d, want both above zero", pool, excerpt)
	}
	r := retrieve.NewLLMReranker(c, pool)
	r.Excerpt = excerpt
	r.Log = slog.New(failOnWarn{t: t})
	return r
}

// TestEvalReranker_readsPoolAndExcerptFromEnv: the pool and the excerpt width
// are the arm's knobs, so a sweep needs no recompile. No endpoint is touched;
// the client is only used once Rerank calls it.
func TestEvalReranker_readsPoolAndExcerptFromEnv(t *testing.T) {
	// A sweep exporting the knobs must not make the defaults half fail.
	t.Setenv("BACKEND_EVAL_RERANK_POOL", "")
	t.Setenv("BACKEND_EVAL_RERANK_EXCERPT", "")
	// The default arm is the product's, whatever the product currently ships.
	if got := evalReranker(t, nil); got.Pool != retrieve.DefaultRerankPool || got.Excerpt != retrieve.DefaultRerankExcerpt {
		t.Errorf("defaults = pool %d, excerpt %d, want the product's %d and %d",
			got.Pool, got.Excerpt, retrieve.DefaultRerankPool, retrieve.DefaultRerankExcerpt)
	}
	t.Setenv("BACKEND_EVAL_RERANK_POOL", "100")
	t.Setenv("BACKEND_EVAL_RERANK_EXCERPT", "800")
	got := evalReranker(t, nil)
	if got.Pool != 100 || got.Excerpt != 800 {
		t.Errorf("from the environment = pool %d, excerpt %d, want 100 and 800", got.Pool, got.Excerpt)
	}
}

// failOnWarn is a slog handler that turns a warning into a test failure.
type failOnWarn struct{ t *testing.T }

func (h failOnWarn) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (h failOnWarn) Handle(_ context.Context, r slog.Record) error {
	attrs := ""
	r.Attrs(func(a slog.Attr) bool { attrs += " " + a.String(); return true })
	h.t.Errorf("reranker fell back to the fused order: %s%s", r.Message, attrs)
	return nil
}
func (h failOnWarn) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h failOnWarn) WithGroup(string) slog.Handler      { return h }
