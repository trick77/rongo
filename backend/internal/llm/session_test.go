package llm

import (
	"context"
	"regexp"
	"strconv"
	"testing"
)

var sessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9a-zA-Z]{14}$`)

func TestNewSessionIDShape(t *testing.T) {
	// Given / When
	id := newSessionID()

	// Then
	if !sessionIDPattern.MatchString(id) {
		t.Fatalf("session id %q does not match ses_<12 hex><14 base62>", id)
	}
	if other := newSessionID(); other == id {
		t.Fatalf("consecutive session ids collided: %q", id)
	}
}

func TestChatSessionIDIsStablePerThread(t *testing.T) {
	// Given / When
	first := chatSessionID("thread-a")

	// Then
	if again := chatSessionID("thread-a"); again != first {
		t.Fatalf("session id changed for the same thread: %q then %q", first, again)
	}
	if other := chatSessionID("thread-b"); other == first {
		t.Fatalf("different threads share a session id: %q", other)
	}
	if !sessionIDPattern.MatchString(first) {
		t.Fatalf("thread session id %q does not match expected shape", first)
	}
}

func TestChatSessionIDFallsBackToProcessID(t *testing.T) {
	// Given / When / Then
	if got := chatSessionID(""); got != processSessionID {
		t.Fatalf("threadless turn used %q, want the per-process id %q", got, processSessionID)
	}
}

// TestWithThreadIDIgnoresZero keeps a turn that resolved no thread out of the
// cache: id 0 means "no thread", not thread number zero, so it must land on the
// per-process id like any other utility call.
func TestWithThreadIDIgnoresZero(t *testing.T) {
	// Given
	ctx := WithThreadID(context.Background(), 0)

	// When
	got := threadIDFromContext(ctx)

	// Then
	if got != "" {
		t.Fatalf("threadIDFromContext = %q, want empty", got)
	}
	if id := chatSessionID(got); id != processSessionID {
		t.Fatalf("session id = %q, want the per-process id %q", id, processSessionID)
	}
}

func TestWithThreadIDCarriesTheID(t *testing.T) {
	// Given
	ctx := WithThreadID(context.Background(), 42)

	// When / Then
	if got := threadIDFromContext(ctx); got != "42" {
		t.Fatalf("threadIDFromContext = %q, want %q", got, "42")
	}
}

// TestChatSessionIDCacheIsBounded pins the eviction: a long-running backend must
// not accumulate one entry per thread it has ever answered.
func TestChatSessionIDCacheIsBounded(t *testing.T) {
	// Given
	sessionCache.Lock()
	sessionCache.byThread = map[string]string{}
	sessionCache.order = nil
	sessionCache.Unlock()

	// When
	for i := 0; i < sessionCacheLimit+10; i++ {
		chatSessionID("thread-" + strconv.Itoa(i))
	}

	// Then
	sessionCache.Lock()
	defer sessionCache.Unlock()
	if len(sessionCache.byThread) > sessionCacheLimit {
		t.Fatalf("cache grew to %d entries, limit is %d", len(sessionCache.byThread), sessionCacheLimit)
	}
	if len(sessionCache.order) > sessionCacheLimit {
		t.Fatalf("order slice grew to %d entries, limit is %d", len(sessionCache.order), sessionCacheLimit)
	}
}

// TestForgetThreadDropsTheID pins what a deleted thread takes with it: the
// affinity id minted for it, so nothing of the conversation outlives the row.
func TestForgetThreadDropsTheID(t *testing.T) {
	// Given
	sessionCache.Lock()
	sessionCache.byThread = map[string]string{}
	sessionCache.order = nil
	sessionCache.Unlock()
	first := chatSessionID("7")

	// When
	ForgetThread(7)

	// Then
	sessionCache.Lock()
	_, held := sessionCache.byThread["7"]
	order := len(sessionCache.order)
	sessionCache.Unlock()
	if held {
		t.Fatal("the id is still cached after ForgetThread")
	}
	if order != 0 {
		t.Fatalf("order still holds %d entries, want 0", order)
	}
	if again := chatSessionID("7"); again == first {
		t.Fatal("the forgotten id came back; the thread was not re-pinned")
	}
}

// TestForgetThreadIgnoresWhatItNeverHeld covers the ordinary delete: a thread
// that never made a model call has no id to drop, and no other thread's may go
// with it.
func TestForgetThreadIgnoresWhatItNeverHeld(t *testing.T) {
	// Given
	sessionCache.Lock()
	sessionCache.byThread = map[string]string{}
	sessionCache.order = nil
	sessionCache.Unlock()
	kept := chatSessionID("3")

	// When
	ForgetThread(9)
	ForgetThread(0)

	// Then
	if chatSessionID("3") != kept {
		t.Fatal("forgetting an unrelated thread re-pinned this one")
	}
}
