package llm

import (
	"context"
	"strconv"
)

type threadIDKey struct{}

// WithThreadID marks ctx as belonging to one rongo thread, so every log line
// a turn writes can carry the same id. It does not reach the wire: session
// affinity is llmwire's, one id per process. Callers attach it once per turn,
// where the thread is resolved; id 0 means "no thread" and leaves ctx untouched.
func WithThreadID(ctx context.Context, id int64) context.Context {
	if id == 0 {
		return ctx
	}
	return context.WithValue(ctx, threadIDKey{}, strconv.FormatInt(id, 10))
}

// ThreadID returns the thread a turn belongs to, or "" for a call made outside
// one. It exists so a routing decision and the model calls it made line up in
// the log.
func ThreadID(ctx context.Context) string {
	id, _ := ctx.Value(threadIDKey{}).(string)
	return id
}
