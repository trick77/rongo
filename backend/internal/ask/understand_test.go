package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// modelUpstream answers one completion with the given content and records the
// request it saw.
func modelUpstream(t *testing.T, content string) (*llm.Client, *string, *string) {
	t.Helper()
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel = req.Model
		for _, m := range req.Messages {
			gotPrompt += m.Content + "\n"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), &gotModel, &gotPrompt
}

const appleTVReply = `{
  "intent": "how",
  "terms": ["Wiedergabe ohne Anmeldung", "Freigabe für externe Abspielgeräte"],
  "code_terms": ["AirPlay", "playbackgrant", "token"],
  "repos": ["peeq"]
}`

func TestUnderstand_returnsTermsAndGuessedCodeVocabulary(t *testing.T) {
	// The whole point of this step: the question says "Apple TV", the code says
	// "AirPlay". A fake that echoed the question's own words back would measure
	// an expansion that expands nothing.
	c, _, _ := modelUpstream(t, appleTVReply)
	q := "Wie kommt ein Apple TV ohne Anmeldung an die Mediendatei?"

	got, err := NewUnderstander(c).Understand(context.Background(), q)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if got.Intent != "how" {
		t.Errorf("Intent = %q", got.Intent)
	}
	if len(got.CodeTerms) == 0 {
		t.Fatal("no code vocabulary guessed")
	}
	var bridged bool
	for _, term := range got.CodeTerms {
		if !strings.Contains(strings.ToLower(q), strings.ToLower(term)) {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("code terms %v all appear in the question already — that is not an expansion", got.CodeTerms)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "peeq" {
		t.Errorf("Repos = %v", got.Repos)
	}
}

func TestUnderstand_searchTextsCarryBothLanguages(t *testing.T) {
	// The retriever fuses one semantic lane per text. Handing it only the raw
	// question would leave the guessed code vocabulary unused, which is exactly
	// the gap the phase-3 measurement found.
	c, _, _ := modelUpstream(t, appleTVReply)
	q := "Wie kommt ein Apple TV ohne Anmeldung an die Mediendatei?"

	got, err := NewUnderstander(c).Understand(context.Background(), q)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	texts := got.SearchTexts(q)

	if len(texts) < 2 {
		t.Fatalf("search texts = %v, want the question plus at least one expansion", texts)
	}
	if texts[0] != q {
		t.Errorf("first text = %q, want the raw question kept", texts[0])
	}
	joined := strings.ToLower(strings.Join(texts, " "))
	if !strings.Contains(joined, "airplay") {
		t.Errorf("search texts %v never mention the guessed code vocabulary", texts)
	}
}

func TestUnderstand_runsOnTheShortGateDeployment(t *testing.T) {
	// Nobody reads this output; it is an id-and-label step. Running it on Pro
	// would pay the expensive queue for a JSON blob.
	c, model, prompt := modelUpstream(t, appleTVReply)

	if _, err := NewUnderstander(c).Understand(context.Background(), "Wie laeuft der Versand?"); err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if *model != llm.ShortGateDeployment {
		t.Errorf("model = %q, want the short-gate deployment", *model)
	}
	if !strings.Contains(*prompt, "Wie laeuft der Versand?") {
		t.Error("the question never reached the prompt")
	}
}

func TestUnderstand_toleratesAFencedJsonBlock(t *testing.T) {
	// Models wrap JSON in ```json fences often enough that treating it as a
	// parse failure would make the step flaky for no reason.
	c, _, _ := modelUpstream(t, "```json\n"+appleTVReply+"\n```")

	got, err := NewUnderstander(c).Understand(context.Background(), "x")

	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if len(got.CodeTerms) == 0 {
		t.Error("fenced JSON parsed to nothing")
	}
}

func TestUnderstand_malformedJsonIsAnErrorNotAnEmptyExpansion(t *testing.T) {
	// An empty Understanding would silently fall back to searching the raw
	// question — the exact behaviour this step exists to replace, and it would
	// look like the expansion simply did not help.
	c, _, _ := modelUpstream(t, "I think you mean the playback code?")

	_, err := NewUnderstander(c).Understand(context.Background(), "x")

	if err == nil {
		t.Fatal("prose answer accepted as an understanding")
	}
}
