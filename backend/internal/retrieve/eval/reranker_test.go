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
	r := retrieve.NewLLMReranker(c, 60)
	r.Log = slog.New(failOnWarn{t: t})
	return r
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
