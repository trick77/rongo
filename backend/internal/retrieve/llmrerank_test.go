package retrieve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/llm"
)

// rerankSeen is what the fake read off the request: the prompt the model was
// shown and the reply cap it was sent. The gate profile spells the cap
// max_completion_tokens (llmwire profiles.yaml, mimo-v2.5).
type rerankSeen struct {
	prompt   string
	replyCap int
}

func rerankLLM(t *testing.T, reply string, saw *rerankSeen) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages            []struct{ Content string } `json:"messages"`
			MaxCompletionTokens int                        `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if saw != nil {
			saw.replyCap = req.MaxCompletionTokens
			if len(req.Messages) > 1 {
				saw.prompt = req.Messages[1].Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

// rerankLLMPicking replies with the number of the result whose header names
// path, read off the prompt it was sent — a model that recognises one file.
func rerankLLMPicking(t *testing.T, path string, saw *string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := req.Messages[len(req.Messages)-1].Content
		if saw != nil {
			*saw = prompt
		}
		reply := `{"relevant":[]}`
		for _, line := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(line, "[") && strings.Contains(line, path) {
				reply = `{"relevant":[` + line[1:strings.IndexByte(line, ']')] + `]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

func TestLLMRerank_putsWhatTheModelPickedFirstAndKeepsTheRest(t *testing.T) {
	var seen rerankSeen
	hits := []Hit{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", RawText: "func a()"},
		{ChunkID: 2, Repo: "peeq", Path: "b.go", RawText: "func b()"},
		{ChunkID: 3, Repo: "peeq", Path: "c.go", Symbol: "c", RawText: "// c is the answer\nfunc c()"},
		{ChunkID: 4, Repo: "peeq", Path: "d.go", RawText: "func d()"},
	}
	r := NewLLMReranker(rerankLLM(t, "```json\n{\"relevant\":[3,1,9,3]}\n```", &seen), 60)
	got, err := r.Rerank(context.Background(), "what is c?", hits, 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(got))
	for i, h := range got {
		ids[i] = h.ChunkID
	}
	// 3 then 1 as the model said (9 out of range and the repeat ignored), then
	// the fused order for the rest, cut to three.
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("order = %v, want [3 1 2]", ids)
	}
	if !strings.Contains(seen.prompt, "[3] peeq c.go (c)") || !strings.Contains(seen.prompt, "// c is the answer") {
		t.Errorf("the model was not shown the numbered headers and excerpts:\n%s", seen.prompt)
	}
}

func TestLLMRerank_keepsTheFusedOrderWhenTheReplyIsNotJSON(t *testing.T) {
	hits := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	r := NewLLMReranker(rerankLLM(t, "I think result 2 is best.", nil), 60)
	got, err := r.Rerank(context.Background(), "q", hits, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Errorf("a reply that could not be read changed the order: %+v", got)
	}
}

// TestLLMRerank_keepsTheFusedOrderWhenTheCallFails: the gate lane going down
// is not a search failure. The fused list was in hand before the call.
func TestLLMRerank_keepsTheFusedOrderWhenTheCallFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	hits := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	r := NewLLMReranker(fakeLLM(t, srv), 60)
	got, err := r.Rerank(context.Background(), "q", hits, 2)
	if err != nil {
		t.Fatalf("a failed rerank call became a search error: %v", err)
	}
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Errorf("a failed call changed the order: %+v", got)
	}
}

// TestLLMRerank_reportsACancelledContextAsSuch: a reader who left is not a
// gate-lane outage, and the search reports the cancellation like every
// other step.
func TestLLMRerank_reportsACancelledContextAsSuch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewLLMReranker(fakeLLM(t, srv), 60)
	if _, err := r.Rerank(ctx, "q", []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}, 1); err == nil {
		t.Fatal("a cancelled context was answered with the fused order")
	}
}

// TestExcerpt_isRuneSafeAndCutsAtALineBreak: the excerpt is measured in runes,
// so a cut never splits an umlaut, and it prefers the last line break in the
// back half of the window so the model reads whole lines.
func TestExcerpt_isRuneSafeAndCutsAtALineBreak(t *testing.T) {
	t.Run("a cut never splits a rune", func(t *testing.T) {
		got := Excerpt(strings.Repeat("ä", 50), 10)
		if !utf8.ValidString(got) {
			t.Fatalf("excerpt cut inside a rune: %q", got)
		}
		if got != strings.Repeat("ä", 10)+"…" {
			t.Errorf("excerpt = %q, want ten runes and the ellipsis", got)
		}
	})
	t.Run("a line break in the back half wins", func(t *testing.T) {
		s := strings.Repeat("a", 6) + "\n" + strings.Repeat("b", 20)
		got := Excerpt(s, 10)
		if got != strings.Repeat("a", 6)+"…" {
			t.Errorf("excerpt = %q, want the cut at the newline", got)
		}
	})
	t.Run("a line break in the front half loses", func(t *testing.T) {
		s := "ab\n" + strings.Repeat("c", 30)
		got := Excerpt(s, 10)
		if got != "ab\n"+strings.Repeat("c", 7)+"…" {
			t.Errorf("excerpt = %q, want the full window", got)
		}
	})
	t.Run("a single overlong line cuts at n", func(t *testing.T) {
		got := Excerpt(strings.Repeat("x", 100), 10)
		if got != strings.Repeat("x", 10)+"…" {
			t.Errorf("excerpt = %q, want ten runes and the ellipsis", got)
		}
	})
	t.Run("text that fits comes back whole", func(t *testing.T) {
		if got := Excerpt("  füüf\nlines  ", 10); got != "füüf\nlines" {
			t.Errorf("excerpt = %q, want the whole trimmed text", got)
		}
	})
	t.Run("the byte walk agrees with the rune slice", func(t *testing.T) {
		for _, s := range excerptCorpus {
			for _, n := range []int{0, 1, 2, 3, 7, 10, 23, 240} {
				if got, want := Excerpt(s, n), excerptByRuneSlice(s, n); got != want {
					t.Errorf("Excerpt(%q, %d) = %q, want %q", s, n, got, want)
				}
			}
		}
	})
}

// excerptCorpus is mixed ASCII, umlauts and line breaks, including a cut that
// would land inside a rune and one where the only newline sits in the front
// half of the window.
var excerptCorpus = []string{
	"",
	"short",
	"ä",
	strings.Repeat("ä", 50),
	strings.Repeat("äb\n", 40),
	"ab\n" + strings.Repeat("c", 300),
	strings.Repeat("a", 6) + "\n" + strings.Repeat("ü", 300),
	"füüf\nlines\nof\ntext\nhere\n" + strings.Repeat("x", 500),
	strings.Repeat("Umlaut ö line\n", 40),
	"\n\n\n" + strings.Repeat("ß", 300),
}

// excerptByRuneSlice is the implementation excerpt replaced, kept as the
// reference the byte walk is checked against.
func excerptByRuneSlice(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cutAt := n
	for i := n - 1; i >= n/2; i-- {
		if r[i] == '\n' {
			cutAt = i
			break
		}
	}
	return string(r[:cutAt]) + "…"
}

func FuzzExcerpt(f *testing.F) {
	for _, s := range excerptCorpus {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, n int) {
		// Invalid UTF-8 is where the two deliberately differ: the rune slice
		// substitutes U+FFFD for a stray byte, the byte walk passes the
		// file's own bytes through. Chunk text is a file's bytes, so passing
		// them through is the better half of that pair; either way the cut
		// still lands on a rune boundary.
		if n < 0 || n > 1000 || !utf8.ValidString(s) {
			t.Skip()
		}
		if got, want := Excerpt(s, n), excerptByRuneSlice(s, n); got != want {
			t.Errorf("Excerpt(%q, %d) = %q, want %q", s, n, got, want)
		}
	})
}

// TestLLMRerank_headerCarriesTheStartLine: two chunks of one file differ by
// where they start, and the model cannot tell them apart without it.
func TestLLMRerank_headerCarriesTheStartLine(t *testing.T) {
	var seen rerankSeen
	hits := []Hit{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", Symbol: "a", StartLine: 42, RawText: "func a()"},
		{ChunkID: 2, Repo: "peeq", Path: "b.go", RawText: "func b()"},
	}
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &seen), 60)
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.prompt, "[1] peeq a.go:42 (a)") {
		t.Errorf("the start line is missing from the header:\n%s", seen.prompt)
	}
	if !strings.Contains(seen.prompt, "[2] peeq b.go\n") {
		t.Errorf("a chunk starting at line zero got a colon:\n%s", seen.prompt)
	}
}

// TestLLMRerank_labelsTestResults: the header says which results are tests,
// so the model can prefer the code a test exercises over the test itself.
func TestLLMRerank_labelsTestResults(t *testing.T) {
	var seen rerankSeen
	hits := []Hit{
		{ChunkID: 1, Repo: "peeq", Path: "client.go", Symbol: "Do", StartLine: 10, RawText: "func Do()"},
		{ChunkID: 2, Repo: "peeq", Path: "client_test.go", Symbol: "TestDo", StartLine: 5, RawText: "func TestDo()"},
	}
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &seen), 60)
	if _, err := r.Rerank(context.Background(), "what does Do do?", hits, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.prompt, "[2] peeq client_test.go:5 (TestDo) (test)") {
		t.Errorf("the test result is not labelled:\n%s", seen.prompt)
	}
	if !strings.Contains(seen.prompt, "[1] peeq client.go:10 (Do)\n") {
		t.Errorf("the code result got a label or lost its header:\n%s", seen.prompt)
	}
}

// TestLLMRerank_excerptWidthIsAField: the field is what the model reads by; a
// line past rune 600 is invisible at 240 and visible at the shipped 800.
func TestLLMRerank_excerptWidthIsAField(t *testing.T) {
	body := strings.Repeat("filler line\n", 50) + "THE MARKER\n" + strings.Repeat("tail line\n", 50)
	hits := []Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go", RawText: body}, {ChunkID: 2}}

	var narrow rerankSeen
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &narrow), 60)
	r.Excerpt = 240
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(narrow.prompt, "THE MARKER") {
		t.Errorf("Excerpt = 240 already reaches the marker; the test proves nothing")
	}

	// The shipped width, left to the default.
	var wide rerankSeen
	r = NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &wide), 60)
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wide.prompt, "THE MARKER") {
		t.Errorf("the default width did not reach the marker:\n%s", wide.prompt)
	}
}

// TestRerankQuestion_aFollowUpBringsWhatItFollows: the reranker is asked
// whether sixty chunks answer the question, and "in welchem Formularschritt
// passiert das?" cannot be judged against anything on its own. Without the
// turn it continues, the chunks the thread is actually about read as
// irrelevant - so the lane that pulled them up would be undone by the cut.
func TestRerankQuestion_aFollowUpBringsWhatItFollows(t *testing.T) {
	got := rerankQuestion("In welchem Formularschritt passiert das?", "Wann genau und was genau macht der ZAS Check?")

	if !strings.Contains(got, "ZAS Check") {
		t.Errorf("rerank question = %q, want the subject the follow-up points at", got)
	}
	if !strings.Contains(got, "In welchem Formularschritt passiert das?") {
		t.Errorf("rerank question = %q, want the question the reader asked", got)
	}
	// The reader's question is the ask; the previous turn is context above it.
	if strings.Index(got, "ZAS Check") > strings.Index(got, "Formularschritt") {
		t.Errorf("rerank question = %q, want the previous turn above the current question", got)
	}
}

// TestRerankQuestion_aFirstTurnIsUnchanged: the measured 27/30 to 28/30 was
// taken on the bare question, and a first turn still sends exactly that.
func TestRerankQuestion_aFirstTurnIsUnchanged(t *testing.T) {
	q := "How does an Apple TV get at the media file?"

	if got := rerankQuestion(q, ""); got != q {
		t.Errorf("rerank question = %q, want the bare question", got)
	}
	// A reader asking the same thing twice adds nothing to judge against.
	if got := rerankQuestion(q, " "+q+" "); got != q {
		t.Errorf("rerank question = %q, want no duplicate of the same question", got)
	}
}

// TestLLMRerank_replyCapGrowsWithTheListAskedFor: the prompt bounds the reply
// at k numbers, so the cap is keyed to k. A long list past the floor must get
// the room, or the reply ends with finish_reason=length, which is an error, a
// warning and the fused order.
func TestLLMRerank_replyCapGrowsWithTheListAskedFor(t *testing.T) {
	for _, c := range []struct {
		k, want int
	}{
		{20, 256},  // the shipped call, inside the floor
		{60, 304},  // past the floor
		{100, 464}, // a caller asking for a hundred
	} {
		if got := replyCap(c.k); got != c.want {
			t.Errorf("replyCap(%d) = %d, want %d", c.k, got, c.want)
		}
	}

	// And the cap really reaches the wire, under the gate profile's own
	// spelling of the parameter.
	var seen rerankSeen
	hits := make([]Hit, 100)
	for i := range hits {
		hits[i] = Hit{ChunkID: int64(i + 1), Repo: "peeq", Path: "a.go"}
	}
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &seen), 100)
	if _, err := r.Rerank(context.Background(), "q", hits, 100); err != nil {
		t.Fatal(err)
	}
	if seen.replyCap != 464 {
		t.Errorf("reply cap on the wire = %d for k = 100, want 464", seen.replyCap)
	}
}

// fakeLLM is the model client pointed at a test server. With BaseURL set,
// llm.NewClient consults no environment variable, so the only way this can
// fail is a bug in the constructor.
func fakeLLM(t testing.TB, srv *httptest.Server) *llm.Client {
	t.Helper()
	c, err := llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatalf("llm.NewClient: %v", err)
	}
	return c
}
