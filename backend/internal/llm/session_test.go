package llm

import (
	"context"
	"testing"
)

// TestWithThreadIDIgnoresZero: id 0 means "no thread", not thread number zero,
// so a turn that resolved no thread leaves the context unmarked.
func TestWithThreadIDIgnoresZero(t *testing.T) {
	// Given
	ctx := WithThreadID(context.Background(), 0)

	// When / Then
	if got := ThreadID(ctx); got != "" {
		t.Fatalf("ThreadID = %q, want empty", got)
	}
}

func TestWithThreadIDCarriesTheID(t *testing.T) {
	// Given
	ctx := WithThreadID(context.Background(), 42)

	// When / Then
	if got := ThreadID(ctx); got != "42" {
		t.Fatalf("ThreadID = %q, want %q", got, "42")
	}
}
