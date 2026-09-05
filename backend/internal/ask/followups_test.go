package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// followupsUpstream is a fake upstream that records the whole request, not
// just the prompt: the lane, the temperature pin and the token cap are as much
// a part of this call's contract as the words are.
type followupsUpstream struct {
	mu          sync.Mutex
	prompt      string
	model       string
	temperature *float64
	maxTokens   int
	calls       int
}

func followupsLLM(t *testing.T, reply string, status int) (*llm.Client, *followupsUpstream) {
	t.Helper()
	up := &followupsUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model               string        `json:"model"`
			Temperature         *float64      `json:"temperature"`
			MaxCompletionTokens int           `json:"max_completion_tokens"`
			Messages            []llm.Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		up.mu.Lock()
		up.calls++
		up.model = req.Model
		up.temperature = req.Temperature
		up.maxTokens = req.MaxCompletionTokens
		up.prompt = ""
		for _, m := range req.Messages {
			up.prompt += m.Content + "\n"
		}
		up.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), up
}

func followupsSources() []Source {
	return []Source{
		{Repo: "rongo", Path: "backend/internal/ask/answer.go", Symbol: "Answer"},
		{Repo: "rongo", Path: "backend/internal/ask/answer.go", Symbol: "Answerer"},
		{Repo: "rongo", Path: "backend/internal/httpapi/ask.go", Symbol: "handleAsk"},
	}
}

func TestFollowups_readsOneQuestionPerLine(t *testing.T) {
	c, _ := followupsLLM(t, "What happens on a re-index?\nWhere does the viewer read the file from?\n", http.StatusOK)

	got := Followups(context.Background(), c, "How are citations stored?", "The answer.", AudienceBA, followupsSources(), Scope{}, LanguageEN)

	want := []string{"What happens on a re-index?", "Where does the viewer read the file from?"}
	if len(got) != len(want) {
		t.Fatalf("Followups() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Followups()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFollowups_stripsNumberingBulletsAndQuotes(t *testing.T) {
	c, _ := followupsLLM(t, "1. What happens on a re-index?\n- Where is the SHA recorded?\n* \"Which commit does the viewer use?\"\n", http.StatusOK)

	got := Followups(context.Background(), c, "q", "a", AudienceDev, followupsSources(), Scope{}, LanguageEN)

	want := []string{"What happens on a re-index?", "Where is the SHA recorded?", "Which commit does the viewer use?"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Followups() = %q, want %q", got, want)
	}
}

func TestFollowups_dropsProseAndOverlongLinesAndCapsAtThree(t *testing.T) {
	reply := strings.Join([]string{
		"Here are some questions you might ask next.",
		"What happens on a re-index?",
		strings.Repeat("a very long question ", 12) + "?",
		"Where is the SHA recorded?",
		"Which commit does the viewer use?",
		"How is the index built?",
	}, "\n")
	c, _ := followupsLLM(t, reply, http.StatusOK)

	got := Followups(context.Background(), c, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN)

	want := []string{"What happens on a re-index?", "Where is the SHA recorded?", "Which commit does the viewer use?"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Followups() = %q, want %q", got, want)
	}
}

func TestFollowups_emptyIsTheNormalFailure(t *testing.T) {
	// An upstream error, a reply with nothing question-shaped in it, and no
	// client at all: three ways to get no pills, none of them a turn failure.
	c, _ := followupsLLM(t, "", http.StatusInternalServerError)
	if got := Followups(context.Background(), c, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN); got != nil {
		t.Errorf("upstream error: Followups() = %q, want nil", got)
	}

	c, _ = followupsLLM(t, "I could not think of anything.", http.StatusOK)
	if got := Followups(context.Background(), c, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN); got != nil {
		t.Errorf("no question in the reply: Followups() = %q, want nil", got)
	}

	if got := Followups(context.Background(), nil, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN); got != nil {
		t.Errorf("no client: Followups() = %q, want nil", got)
	}
}

func TestFollowups_runsOnTheShortGateLaneWithThePinnedTemperature(t *testing.T) {
	c, up := followupsLLM(t, "What happens on a re-index?\n", http.StatusOK)

	Followups(context.Background(), c, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN)

	if up.model != llm.ShortGateDeployment {
		t.Errorf("model = %q, want the short-gate deployment", up.model)
	}
	if up.temperature == nil || *up.temperature != gateTemperature {
		t.Errorf("temperature = %v, want the pinned %v", up.temperature, float64(gateTemperature))
	}
	if up.maxTokens != followupsMaxTokens {
		t.Errorf("max tokens = %d, want %d", up.maxTokens, followupsMaxTokens)
	}
}

func TestFollowups_isGroundedInTheFilesTheAnswerWasWrittenFrom(t *testing.T) {
	c, up := followupsLLM(t, "What happens on a re-index?\n", http.StatusOK)

	Followups(context.Background(), c, "How are citations stored?", "The answer text.", AudienceDev,
		followupsSources(), Scope{Known: []string{"rongo"}, Unknown: []string{"shop-backend"}}, LanguageEN)

	// The paths the answer was written from, each named once.
	if !strings.Contains(up.prompt, "rongo/backend/internal/ask/answer.go") {
		t.Errorf("the prompt does not carry the source paths:\n%s", up.prompt)
	}
	if strings.Count(up.prompt, "rongo/backend/internal/ask/answer.go") != 1 {
		t.Errorf("a path the answer used twice is listed twice:\n%s", up.prompt)
	}
	// A repository the index does not carry must not turn up as a suggestion:
	// "no hit means no hit" applies to what rongo offers to ask, too.
	if !strings.Contains(up.prompt, "shop-backend") {
		t.Errorf("the prompt does not name the repositories the index lacks:\n%s", up.prompt)
	}
	// Never the gathered code itself: this call reads paths, not chunks.
	if strings.Contains(up.prompt, "func Answer") {
		t.Errorf("the prompt carries source text:\n%s", up.prompt)
	}
	if !strings.Contains(up.prompt, "How are citations stored?") || !strings.Contains(up.prompt, "The answer text.") {
		t.Errorf("the prompt carries neither the question nor the answer:\n%s", up.prompt)
	}
}

func TestFollowups_speaksTheReadersLanguageTheSwissWay(t *testing.T) {
	c, up := followupsLLM(t, "Was passiert beim Neuindexieren?\n", http.StatusOK)

	got := Followups(context.Background(), c, "Wie werden Zitate gespeichert?", "Die Antwort.", AudienceBA,
		followupsSources(), Scope{}, LanguageDE)

	if len(got) != 1 || got[0] != "Was passiert beim Neuindexieren?" {
		t.Errorf("Followups() = %q", got)
	}
	if strings.Count(up.prompt, "German") < 2 {
		t.Errorf("the language is named %d times, want it first and last:\n%s", strings.Count(up.prompt, "German"), up.prompt)
	}
	if !strings.Contains(up.prompt, "never the letter ß") {
		t.Errorf("the German prompt does not ask for Swiss orthography:\n%s", up.prompt)
	}
}

func TestFollowups_neverOffersTheSameQuestionTwice(t *testing.T) {
	// A model writing a list restates its own entries. Two identical pills
	// waste a third of the row and say nothing new.
	c, _ := followupsLLM(t, "What happens on a re-index?\nWhat happens on a re-index?\nWhere is the SHA recorded?\n", http.StatusOK)

	got := Followups(context.Background(), c, "q", "a", AudienceBA, followupsSources(), Scope{}, LanguageEN)

	want := []string{"What happens on a re-index?", "Where is the SHA recorded?"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Followups() = %q, want %q", got, want)
	}
}
