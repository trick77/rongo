package ask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// titleUpstream is a fake that answers each attempt from a script, so a test
// can say "fail, then succeed" and count how many calls that took.
type titleUpstream struct {
	mu      sync.Mutex
	calls   int
	replies []titleReply
	// prompts is every attempt's messages joined, so a test can see what the
	// retry actually asked for.
	prompts []string
}

type titleReply struct {
	status  int
	content string
}

// next hands out the reply scripted for this attempt. A script that runs out
// keeps failing: the cap, not the fake, is what must end the loop.
func (u *titleUpstream) next(prompt string) titleReply {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, prompt)
	r := titleReply{status: http.StatusInternalServerError}
	if u.calls < len(u.replies) {
		r = u.replies[u.calls]
	}
	u.calls++
	return r
}

func (u *titleUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *titleUpstream) prompt(attempt int) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if attempt >= len(u.prompts) {
		return ""
	}
	return u.prompts[attempt]
}

func titleLLM(t *testing.T, replies ...titleReply) (*llm.Client, *titleUpstream) {
	t.Helper()
	up := &titleUpstream{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var prompt strings.Builder
		for _, m := range req.Messages {
			prompt.WriteString(m.Content + "\n")
		}
		reply := up.next(prompt.String())
		if reply.status != http.StatusOK {
			w.WriteHeader(reply.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply.content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), up
}

func TestTitle_aFailedCallIsRetried(t *testing.T) {
	c, up := titleLLM(t,
		titleReply{status: http.StatusInternalServerError},
		titleReply{status: http.StatusOK, content: "Shipping, end to end"},
	)
	if got := Title(context.Background(), c, "How does shipping work?", LanguageEN); got != "Shipping, end to end" {
		t.Fatalf("title = %q, want the second attempt's title", got)
	}
	if up.count() != 2 {
		t.Fatalf("calls = %d, want 2", up.count())
	}
	// Nothing came back to correct, so the retry asks the same thing again.
	if strings.Contains(up.prompt(1), "That was not a title") {
		t.Errorf("a call that failed on the wire was retried with the nudge:\n%s", up.prompt(1))
	}
}

func TestTitle_aReplyThatIsNotATitleIsRetried(t *testing.T) {
	c, up := titleLLM(t,
		titleReply{status: http.StatusOK, content: "Here is a title for you:\n\nShipping, end to end"},
		titleReply{status: http.StatusOK, content: "Shipping, end to end"},
	)
	if got := Title(context.Background(), c, "How does shipping work?", LanguageEN); got != "Shipping, end to end" {
		t.Fatalf("title = %q, want the second attempt's title", got)
	}
	if up.count() != 2 {
		t.Fatalf("calls = %d, want 2", up.count())
	}
	// The call is pinned to temperature 0 and to one upstream node, so a retry
	// that repeats itself word for word gets the same paragraph back. The
	// second attempt has to say what was wrong with the first.
	if strings.Contains(up.prompt(0), "That was not a title") {
		t.Errorf("the first attempt already carries the nudge:\n%s", up.prompt(0))
	}
	if !strings.Contains(up.prompt(1), "That was not a title") {
		t.Errorf("the retry repeats the first attempt instead of correcting it:\n%s", up.prompt(1))
	}
}

func TestTitle_theRetriesAreCapped(t *testing.T) {
	c, up := titleLLM(t)
	if got := Title(context.Background(), c, "How does shipping work?", LanguageEN); got != "" {
		t.Fatalf("title = %q, want empty so the placeholder stands", got)
	}
	if up.count() != titleAttempts {
		t.Fatalf("calls = %d, want %d", up.count(), titleAttempts)
	}
}

func TestTitle_aTitleOnTheFirstTryCostsOneCall(t *testing.T) {
	c, up := titleLLM(t, titleReply{status: http.StatusOK, content: "Shipping, end to end"})
	if got := Title(context.Background(), c, "How does shipping work?", LanguageEN); got != "Shipping, end to end" {
		t.Fatalf("title = %q", got)
	}
	if up.count() != 1 {
		t.Fatalf("calls = %d, want 1", up.count())
	}
}

func TestTitle_aCancelledTurnStopsRetrying(t *testing.T) {
	c, up := titleLLM(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Title(ctx, c, "How does shipping work?", LanguageEN); got != "" {
		t.Fatalf("title = %q, want empty", got)
	}
	if up.count() > 1 {
		t.Fatalf("calls = %d, want no retry after the cancel", up.count())
	}
}
