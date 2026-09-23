package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
	return fakeLLM(t, srv), &gotModel, &gotPrompt
}

const appleTVReply = `{
  "intent": "how",
  "terms": ["playback without sign-in", "access for external playback devices"],
  "code_terms": ["AirPlay", "playbackgrant", "token"],
  "repos": ["peeq"]
}`

// TestUnderstand_theIntentIsNormalisedOnce: everything downstream compares the
// intent to a word — the answer prompt looks it up in a table — so the
// spelling the model happened to use is settled here and nowhere else.
func TestUnderstand_theIntentIsNormalisedOnce(t *testing.T) {
	for _, spelled := range []string{"Where ", "Where.", `\"Where\"`, "WHERE!"} {
		c, _, _ := modelUpstream(t, `{"intent":"`+spelled+`","terms":["t"],"code_terms":["c"],"repos":[]}`)

		got, err := NewUnderstander(c).Understand(context.Background(), "Wo?", Thread{}, nil)
		if err != nil {
			t.Fatalf("Understand %q: %v", spelled, err)
		}

		if got.Intent != "where" {
			t.Errorf("Intent = %q for %q, want it lower-cased and stripped", got.Intent, spelled)
		}
	}
}

// TestUnderstand_anUnknownIntentIsLeftAlone: normalising is spelling, not
// judgement. A word the prompt never offered still reaches the trace, where a
// reader sees what the model actually said.
func TestUnderstand_anUnknownIntentIsLeftAlone(t *testing.T) {
	c, _, _ := modelUpstream(t, `{"intent":"sideways","terms":["t"],"code_terms":["c"],"repos":[]}`)

	got, err := NewUnderstander(c).Understand(context.Background(), "Was?", Thread{}, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if got.Intent != "sideways" {
		t.Errorf("Intent = %q, want the model's own word", got.Intent)
	}
}

func TestUnderstand_returnsTermsAndGuessedCodeVocabulary(t *testing.T) {
	// The whole point of this step: the question says "Apple TV", the code says
	// "AirPlay". A fake that echoed the question's own words back would measure
	// an expansion that expands nothing.
	c, _, _ := modelUpstream(t, appleTVReply)
	q := "How does an Apple TV get at the media file without signing in?"

	got, err := NewUnderstander(c).Understand(context.Background(), q, Thread{}, nil)
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

	got, err := NewUnderstander(c).Understand(context.Background(), q, Thread{}, nil)
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

func TestUnderstand_runsOnTheGateLane(t *testing.T) {
	// Nobody reads this output; it is an id-and-label step. Running it on Pro
	// would pay the expensive queue for a JSON blob.
	c, model, prompt := modelUpstream(t, appleTVReply)

	if _, err := NewUnderstander(c).Understand(context.Background(), "How does shipping work?", Thread{}, nil); err != nil {
		t.Fatalf("Understand: %v", err)
	}

	if *model != testGateModel {
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

	got, err := NewUnderstander(c).Understand(context.Background(), "x", Thread{}, nil)

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

	_, err := NewUnderstander(c).Understand(context.Background(), "x", Thread{}, nil)

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

	got, err := NewUnderstander(c).Understand(context.Background(), "in all repos, how are token costs calculated in $?", Thread{}, nil)
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

	got, err := NewUnderstander(c).Understand(context.Background(), "how are token costs calculated in $?", Thread{}, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if got.AllRepos {
		t.Error("a reply that says nothing about all_repos must not mean every repository")
	}
}

// TestUnderstand_readsALinkCensus: "what links lead out of this UI" is a
// listing, not a mechanism, and the field is the only thing that tells the
// pipeline to read the link tokens of the repository instead of searching.
func TestUnderstand_readsALinkCensus(t *testing.T) {
	c, _, prompt := modelUpstream(t, `{
  "intent": "where",
  "terms": ["outbound links"],
  "code_terms": ["href"],
  "repos": ["claims-ui"],
  "census": " Link "
}`)

	got, err := NewUnderstander(c).Understand(context.Background(), "in claims-ui, what links lead to apps outside it?", Thread{}, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if got.Census != "link" {
		t.Errorf("Census = %q, want link, normalised like the intent", got.Census)
	}
	if !strings.Contains(*prompt, "census") {
		t.Errorf("the prompt never asks for the field:\n%s", *prompt)
	}
}

// TestUnderstand_aMechanismQuestionAsksForNoCensus is the default: a reply
// that says nothing, or names a kind the pipeline has no census for, reads
// as none.
func TestUnderstand_aMechanismQuestionAsksForNoCensus(t *testing.T) {
	for _, reply := range []string{
		`{"intent":"where","terms":["t"],"code_terms":["c"],"repos":["x"]}`,
		`{"intent":"where","terms":["t"],"code_terms":["c"],"repos":["x"],"census":"route"}`,
	} {
		c, _, _ := modelUpstream(t, reply)
		got, err := NewUnderstander(c).Understand(context.Background(), "where is the claim total computed?", Thread{}, nil)
		if err != nil {
			t.Fatalf("Understand: %v", err)
		}
		if got.Census != "" {
			t.Errorf("Census = %q for %s, want none", got.Census, reply)
		}
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
		"Kannst du das in einem Diagramm aufzeigen?", prev, nil); err != nil {
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

	if _, err := NewUnderstander(c).Understand(context.Background(), "Und wo wird das gecacht?", prev, nil); err != nil {
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

	if _, err := NewUnderstander(c).Understand(context.Background(), "How is pricing resolved?", Thread{}, nil); err != nil {
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
	// Read off the assembled follow-up prompt, not off understandSystem: the
	// rule is only in the prompt of a turn that HAS a previous turn, and a
	// first turn's prompt must not carry it at all.
	flat := strings.Join(strings.Fields(understandPrompt(false, true)), " ")
	if !strings.Contains(flat, "resolve what the current question leaves out") {
		t.Error("the prompt never says what the previous turn is for")
	}
	// Whitespace-folded: the constant is hard-wrapped, and a rule split across
	// two lines is still the rule.
	if !strings.Contains(flat, "changes the subject gets the new one") {
		t.Error("the prompt never says a follow-up may change the subject")
	}
}

// TestUnderstand_thePriorQuestionIsStampedNotRead: the previous question
// reaches the search because Go puts it there, not because the model chose to
// keep it. A reply that volunteers "prior" cannot influence the field, and a
// first turn leaves it empty.
func TestUnderstand_thePriorQuestionIsStampedNotRead(t *testing.T) {
	c, _, _ := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"prior":"nonsense the model made up"}`)
	prev := Thread{Question: "Wann genau und was genau macht der ZAS Check?", Answer: "ServiceZas prüft die Versichertennummer."}

	got, err := NewUnderstander(c).Understand(context.Background(), "In welchem Formularschritt passiert das?", prev, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if got.Prior != prev.Question {
		t.Errorf("Prior = %q, want the previous question the record holds, never the model's own word", got.Prior)
	}

	c2, _, _ := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"prior":"nonsense"}`)
	first, err := NewUnderstander(c2).Understand(context.Background(), "How is pricing resolved?", Thread{}, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if first.Prior != "" {
		t.Errorf("Prior = %q on a first turn, want empty", first.Prior)
	}
}

// TestSearchTexts_aFirstTurnIsUnchanged: the lanes a question with no thread
// behind it searches are exactly what they were. This is the eval baseline.
func TestSearchTexts_aFirstTurnIsUnchanged(t *testing.T) {
	u := Understanding{Terms: []string{"pricing", "cost per token"}, CodeTerms: []string{"Price", "CostNanoUSD"}}
	q := "How is pricing resolved?"

	texts := u.SearchTexts(q)

	want := []string{q, "pricing cost per token", u.CodeText()}
	if !slices.Equal(texts, want) {
		t.Errorf("search texts = %v, want %v", texts, want)
	}
	if texts[len(texts)-1] != u.CodeText() {
		t.Error("the last lane must stay CodeText's: the keyword lane finds the code rung by comparing the two")
	}
}

// TestSearchTexts_aFollowUpSearchesThePreviousQuestion is the whole fix. A
// follow-up names its subject a turn ago, so the search has to carry it -
// whatever the expansion came back with.
func TestSearchTexts_aFollowUpSearchesThePreviousQuestion(t *testing.T) {
	// Terms and code terms that lost the subject entirely, which is the
	// failure this exists for.
	u := Understanding{
		Prior:     "Wann genau und was genau macht der ZAS Check?",
		Terms:     []string{"Wo im Formularablauf findet dieser Vorgang statt?"},
		CodeTerms: []string{"FormStep", "StepSequence"},
	}
	q := "In welchem Formularschritt passiert das?"

	texts := u.SearchTexts(q)

	if len(texts) != 4 {
		t.Fatalf("search texts = %v, want the question, the previous question, the terms and the code", texts)
	}
	if texts[0] != q {
		t.Errorf("first lane = %q, want the question as asked", texts[0])
	}
	if texts[1] != u.Prior {
		t.Errorf("second lane = %q, want the previous question", texts[1])
	}
	if !strings.Contains(strings.Join(texts, " "), "ZAS") {
		t.Error("the subject the follow-up points at never reaches the search")
	}
	if texts[len(texts)-1] != u.CodeText() {
		t.Error("the last lane must stay CodeText's")
	}
}

// TestUnderstandPrompt_aFirstTurnCarriesNoFollowUpRule: the follow-up rule is
// about material a first turn does not have, and a prompt that explains an
// absent previous turn is what the evaluation baseline was NOT measured on.
// Proven here rather than by an eval run, which cannot see a first turn's
// prompt at all.
func TestUnderstandPrompt_aFirstTurnCarriesNoFollowUpRule(t *testing.T) {
	for _, withMemory := range []bool{false, true} {
		first := understandPrompt(withMemory, false)
		if strings.Contains(strings.Join(strings.Fields(first), " "), "EVERY entry of terms") {
			t.Errorf("memory=%v: a first turn is told to carry a subject it has none of:\n%s", withMemory, first)
		}
		// The paragraph the baseline was measured with stays, on every turn:
		// dropping it from a first turn would be an unmeasured prompt change
		// on the one path the eval does cover.
		if !strings.Contains(first, "A question may arrive with the previous turn") {
			t.Errorf("memory=%v: the measured paragraph left the first-turn prompt", withMemory)
		}

		followUp := understandPrompt(withMemory, true)
		// Whitespace-folded: the constant is hard-wrapped, and a rule split
		// across two lines is still the rule.
		if !strings.Contains(strings.Join(strings.Fields(followUp), " "), "EVERY entry of terms and EVERY entry of code_terms carries it") {
			t.Errorf("memory=%v: a follow-up is never told to carry the subject:\n%s", withMemory, followUp)
		}
		// The follow-up prompt is the first-turn one plus the rule, nothing
		// removed and nothing reordered.
		if !strings.Contains(followUp, "A question may arrive with the previous turn") {
			t.Errorf("memory=%v: the follow-up prompt dropped the paragraph it extends", withMemory)
		}
		if len(followUp) <= len(first) {
			t.Errorf("memory=%v: the follow-up prompt is not longer than the first-turn one", withMemory)
		}
	}
}

// TestUnderstandPrompt_aFirstTurnIsByteIdenticalToTheMeasuredOne is the guard
// the eval cannot be: TestEvalMeasureAnswers has no follow-up questions, so a
// first turn's prompt is the only part of this change it could see, and the
// cheapest way not to move that number is not to touch those bytes.
//
// The golden is the rendering of the prompt as it stood when the baseline was
// taken. A deliberate first-turn prompt change updates it AND re-runs the
// eval twice; anything else failing here is an accident.
func TestUnderstandPrompt_aFirstTurnIsByteIdenticalToTheMeasuredOne(t *testing.T) {
	first := understandPrompt(false, false)
	if !strings.HasSuffix(first, goldenFirstTurnTail) {
		t.Errorf("the first-turn prompt no longer ends as the measured one did.\ngot tail:\n%q\nwant tail:\n%q",
			first[max(0, len(first)-len(goldenFirstTurnTail)):], goldenFirstTurnTail)
	}
	// Every %s is filled: a stray verb would reach the model as "%!s(MISSING)".
	if strings.Contains(first, "%!") {
		t.Errorf("unfilled format verb in the prompt:\n%s", first)
	}
	if strings.Contains(understandPrompt(true, true), "%!") {
		t.Error("unfilled format verb in the memory+follow-up prompt")
	}
}

// goldenFirstTurnTail is the last paragraphs of the first-turn prompt exactly
// as origin/master renders them, blank lines included.
const goldenFirstTurnTail = `A question may arrive with the previous turn of the conversation above it. That
material is there for ONE purpose: to resolve what the current question leaves
out - "that", "this", "it", "and how about the other one", a question with no
subject at all. Everything you answer with describes the CURRENT question. A
follow-up that stays on the subject inherits it; a follow-up that changes the
subject gets the new one, and the previous turn contributes nothing to it. A
follow-up that only moves the window of a changes question ("only the last
two days") keeps intent "changes" and the topic.

code_terms is the most important part. The question is phrased in the language
of the business domain, the code is not: someone asking about an "Apple TV"
means "AirPlay" in the code; someone asking about "disk almost full" means
"statfs" or "free bytes". Guess that bridge, even when you are not sure. Do not
simply repeat the words of the question.

No running text, no explanation, just the JSON object.`

// TestUnderstand_aFollowUpIsToldToCarryTheSubject: the gate is driven by the
// thread, not by a flag a caller might forget.
func TestUnderstand_aFollowUpIsToldToCarryTheSubject(t *testing.T) {
	c, _, sys := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)
	prev := Thread{Question: "Wann genau und was genau macht der ZAS Check?", Answer: "ServiceZas prüft."}

	if _, err := NewUnderstander(c).Understand(context.Background(), "In welchem Formularschritt passiert das?", prev, nil); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if !strings.Contains(strings.Join(strings.Fields(*sys), " "), "EVERY entry of terms") {
		t.Errorf("the follow-up rule never reached the call:\n%s", *sys)
	}

	c2, _, sys2 := modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`)
	if _, err := NewUnderstander(c2).Understand(context.Background(), "How is pricing resolved?", Thread{}, nil); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	// The same string the positive check above looks for: the paragraph about
	// a previous turn is unconditional, so asserting on IT would pass whatever
	// the gate did.
	if strings.Contains(strings.Join(strings.Fields(*sys2), " "), "EVERY entry of terms") {
		t.Errorf("a first turn carried the follow-up rule:\n%s", *sys2)
	}
}

// TestSearchTexts_aRetryDoesNotSearchTheSameTextTwice: a retry re-asks the
// question it retries, so the previous question IS this one.
func TestSearchTexts_aRetryDoesNotSearchTheSameTextTwice(t *testing.T) {
	q := "How is pricing resolved?"
	u := Understanding{Prior: " " + q + " ", Terms: []string{"cost"}, CodeTerms: []string{"Price"}}

	texts := u.SearchTexts(q)

	if len(texts) != 3 {
		t.Errorf("search texts = %v, want no lane for a previous question identical to this one", texts)
	}
}
