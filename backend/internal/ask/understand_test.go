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
  "terms": ["playback without sign-in", "access for external playback devices"],
  "code_terms": ["AirPlay", "playbackgrant", "token"],
  "repos": ["peeq"]
}`

func TestUnderstand_returnsTermsAndGuessedCodeVocabulary(t *testing.T) {
	// The whole point of this step: the question says "Apple TV", the code says
	// "AirPlay". A fake that echoed the question's own words back would measure
	// an expansion that expands nothing.
	c, _, _ := modelUpstream(t, appleTVReply)
	q := "How does an Apple TV get at the media file without signing in?"

	got, err := NewUnderstander(c).Understand(context.Background(), q, Thread{})
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
	q := "How does an Apple TV get at the media file without signing in?"

	got, err := NewUnderstander(c).Understand(context.Background(), q, Thread{})
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

	if _, err := NewUnderstander(c).Understand(context.Background(), "How does shipping work?", Thread{}); err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if *model != llm.ShortGateDeployment {
		t.Errorf("model = %q, want the short-gate deployment", *model)
	}
	if !strings.Contains(*prompt, "How does shipping work?") {
		t.Error("the question never reached the prompt")
	}
}

func TestUnderstand_toleratesAFencedJsonBlock(t *testing.T) {
	// Models wrap JSON in ```json fences often enough that treating it as a
	// parse failure would make the step flaky for no reason.
	c, _, _ := modelUpstream(t, "```json\n"+appleTVReply+"\n```")

	got, err := NewUnderstander(c).Understand(context.Background(), "x", Thread{})

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

	_, err := NewUnderstander(c).Understand(context.Background(), "x", Thread{})

	if err == nil {
		t.Fatal("prose answer accepted as an understanding")
	}
}

// TestUnderstand_readsTheAskForEveryRepository pins the one thing that lets a
// reader opt out of the repository card in advance: saying so in the question.
// Without it "in all repos, how are token costs calculated?" is
// indistinguishable from a question that named nothing, and the router would
// ask which repository was meant after the reader already said.
func TestUnderstand_readsTheAskForEveryRepository(t *testing.T) {
	c, _, prompt := modelUpstream(t, `{
  "intent": "how",
  "terms": ["token cost"],
  "code_terms": ["pricing"],
  "repos": [],
  "all_repos": true
}`)

	got, err := NewUnderstander(c).Understand(context.Background(), "in all repos, how are token costs calculated in $?", Thread{})
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if !got.AllRepos {
		t.Error("all_repos must reach the understanding, or the reader's own words never leave this step")
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none named", got.Repos)
	}
	if !strings.Contains(*prompt, "all_repos") {
		t.Errorf("the prompt never asks for the field:\n%s", *prompt)
	}
}

// TestUnderstand_ordinaryQuestionAsksForNoRepositoryAtAll is the default the
// rung rests on: a question that says nothing about scope must not arrive
// with permission to answer across the corpus.
func TestUnderstand_ordinaryQuestionAsksForNoRepositoryAtAll(t *testing.T) {
	c, _, _ := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)

	got, err := NewUnderstander(c).Understand(context.Background(), "how are token costs calculated in $?", Thread{})
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if got.AllRepos {
		t.Error("a reply that says nothing about all_repos must not mean every repository")
	}
}

// TestUnderstand_carriesThePreviousTurnSoAFollowUpCanBeResolved: "Kannst du
// das in einem Diagramm aufzeigen?" names no mechanism, no module and no
// repository, because the reader named all three a turn ago. Alone it is
// searched for as the word "Diagramm".
func TestUnderstand_carriesThePreviousTurnSoAFollowUpCanBeResolved(t *testing.T) {
	c, _, prompt := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)
	prev := Thread{
		Question: "Wie unterscheidet sich rongo von reinem RAG auf dem Quellcode?",
		Answer:   "rongo indexiert Symbole mit universal-ctags [1] und zitiert jede Aussage [2].",
	}

	if _, err := NewUnderstander(c).Understand(context.Background(),
		"Kannst du das in einem Diagramm aufzeigen?", prev); err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if !strings.Contains(*prompt, prev.Question) {
		t.Errorf("the previous question never reached the call:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "universal-ctags") {
		t.Errorf("the previous answer never reached the call:\n%s", *prompt)
	}
	if strings.Contains(*prompt, "[1]") {
		t.Errorf("citation markers number sources this call cannot see:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "Question: Kannst du das in einem Diagramm aufzeigen?") {
		t.Errorf("the question being asked now must be the last thing in the message:\n%s", *prompt)
	}
}

// TestUnderstand_thePreviousAnswersDiagramIsNotWhatTheFollowUpIsAbout: an
// answer that drew one carries a fence of JSON, and 1200 characters of node
// specs is what the excerpt would otherwise consist of.
func TestUnderstand_thePreviousAnswersDiagramIsNotWhatTheFollowUpIsAbout(t *testing.T) {
	c, _, prompt := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)
	prev := Thread{
		Question: "Wie läuft das Indexieren ab?",
		Answer: "Der Indexer holt den Checkout und schneidet ihn in Chunks.\n\n" +
			"```diagram\n{\"nodes\":[{\"id\":\"fetch\",\"label\":\"Fetch\",\"src\":[1]}]}\n```\n\n" +
			"Danach werden die Embeddings gecacht.",
	}

	if _, err := NewUnderstander(c).Understand(context.Background(), "Und wo wird das gecacht?", prev); err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if strings.Contains(*prompt, "\"nodes\"") {
		t.Errorf("the diagram spec reached the call, where it is pure noise:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "Embeddings gecacht") {
		t.Errorf("the prose after the fence was thrown away with it:\n%s", *prompt)
	}
}

// TestUnderstand_afirstTurnCarriesNothing: without a previous turn the call is
// exactly what it was before any of this existed — the bare question, with no
// labels around it to explain away.
func TestUnderstand_afirstTurnCarriesNothing(t *testing.T) {
	c, _, prompt := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)

	if _, err := NewUnderstander(c).Understand(context.Background(), "How is pricing resolved?", Thread{}); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if strings.Contains(*prompt, "Previous question") {
		t.Errorf("a first turn must not be told about a turn that does not exist:\n%s", *prompt)
	}
}

// TestUnderstand_theSystemPromptSaysThePreviousTurnOnlyResolvesTheQuestion is
// the risk the previous turn brings with it: a follow-up that CHANGES the
// subject must be expanded as the new subject, not as the old one. That is a
// prompt rule, so this is the test that it is written down at all.
func TestUnderstand_theSystemPromptSaysThePreviousTurnOnlyResolvesTheQuestion(t *testing.T) {
	if !strings.Contains(strings.Join(strings.Fields(understandSystem), " "),
		"resolve what the current question leaves out") {
		t.Error("the prompt never says what the previous turn is for")
	}
	// Whitespace-folded: the constant is hard-wrapped, and a rule split across
	// two lines is still the rule.
	flat := strings.Join(strings.Fields(understandSystem), " ")
	if !strings.Contains(flat, "changes the subject gets the new one") {
		t.Error("the prompt never says a follow-up may change the subject")
	}
}
