package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// writeDeltas streams the tokens and stops there, leaving the stream open. A
// fake that means to break mid-answer writes these and nothing else.
func writeDeltas(w http.ResponseWriter, tokens []string) {
	fl := http.NewResponseController(w)
	for _, tok := range tokens {
		frame, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", frame)
		_ = fl.Flush()
	}
}

// writeSSE streams the tokens as content deltas, ends on finishReason when
// there is one, and closes with [DONE]. Every fake answer endpoint in these
// tests writes the same frames; what they differ in is which request gets
// them.
func writeSSE(w http.ResponseWriter, tokens []string, finishReason string) {
	fl := http.NewResponseController(w)
	writeDeltas(w, tokens)
	if finishReason != "" {
		end, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finishReason}},
		})
		fmt.Fprintf(w, "data: %s\n\n", end)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = fl.Flush()
}

// streamUpstream streams the given tokens and records the prompt it was sent.
func streamUpstream(t *testing.T, tokens ...string) (*llm.Client, *string, *int) {
	t.Helper()
	return streamUpstreamEnding(t, "", tokens)
}

// streamUpstreamEnding is streamUpstream with the finish_reason the stream
// ends on; "" sends none.
func streamUpstreamEnding(t *testing.T, finishReason string, tokens []string) (*llm.Client, *string, *int) {
	t.Helper()
	var prompt string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, tokens, finishReason)
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &prompt, &calls
}

func twoSources() []Source {
	return []Source{
		{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "backend/internal/playbackgrant/store.go",
			StartLine: 1, EndLine: 30, Text: "func NewGrant() {}", Reason: "hit"},
		{ChunkID: 2, Repo: "peeq", Branch: "master", Path: "backend/internal/httpapi/grant.go",
			StartLine: 5, EndLine: 20, Text: "func issueGrant() {}", Reason: "reference:NewGrant"},
	}
}

func collect(tokens *[]string) func(string) {
	return func(tok string) { *tokens = append(*tokens, tok) }
}

func TestAnswer_streamsAndResolvesTheMarkersItUsed(t *testing.T) {
	// Given
	c, _, _ := streamUpstream(t, "The grant ", "is created in ", "store.go [1].")
	var seen []string

	// When
	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// Then
	if len(seen) < 3 {
		t.Errorf("callbacks = %d, want the answer to arrive in pieces", len(seen))
	}
	if !strings.Contains(got.Text, "store.go [1]") {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v, want only the one marker the answer used", got.Citations)
	}
	cit := got.Citations[0]
	if cit.Marker != 1 || cit.Path != "backend/internal/playbackgrant/store.go" || cit.Branch != "master" {
		t.Errorf("citation = %+v, want it to resolve to source 1 with its branch", cit)
	}
}

func TestAnswer_aMarkerWithNoSourceIsDroppedNotInvented(t *testing.T) {
	// A model that cites [7] with three sources in front of it has made the
	// number up. Emitting a citation for it would put a fabricated reference
	// under an answer — the failure this product can least afford.
	c, _, _ := streamUpstream(t, "This happens in delivery [7].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none for an invented marker", got.Citations)
	}
}

func TestAnswer_aGroupedMarkerCountsForEachNumberInIt(t *testing.T) {
	// A claim resting on several sources comes out as [1, 2]. Read as one
	// marker it matches nothing, and both sources vanish from the panel while
	// the text still shows the brackets - the reader sees a citation that
	// leads nowhere.
	c, _, _ := streamUpstream(t, "Compared on poll [1, 2], and again [2,1].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 2 || got.Citations[0].Marker != 1 || got.Citations[1].Marker != 2 {
		t.Fatalf("citations = %+v, want markers 1 and 2 once each", got.Citations)
	}
}

func TestAnswer_anInventedNumberInsideAGroupIsDroppedAlone(t *testing.T) {
	c, _, _ := streamUpstream(t, "Compared on poll [1, 9].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 {
		t.Fatalf("citations = %+v, want only the real marker of the group", got.Citations)
	}
}

func TestAnswer_anIndexExpressionInCodeIsNotACitation(t *testing.T) {
	// The DEV prompt asks for short snippets, and `args[1]` inside one is an
	// index expression. Reading it as a marker would put a reference under the
	// answer that the model never made — checkable-looking and false.
	c, _, _ := streamUpstream(t,
		"The call is in store.go [2]:\n\n```go\nname := args[1]\nvalue := parts[1]\n```\n")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(got.Text, "store.go [1]:") || !strings.Contains(got.Text, "args[1]") {
		t.Errorf("text = %q, want the prose marker renumbered and the code untouched", got.Text)
	}
	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Fatalf("citations = %+v, want only the marker outside the code block", got.Citations)
	}
}

func TestAnswer_markersAreRenumberedInOrderOfFirstAppearance(t *testing.T) {
	// The prompt numbers a hundred sources and the model cites three of them,
	// so the reader sees [107]. The answer reads 1, 2, 3 in the order the
	// markers appear, and the stream carries the same text as the record:
	// a marker split across tokens is held back until it is complete.
	c, _, _ := streamUpstream(t, "Issued in grant.go [", "2", "], stored [1] and again [2].")
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Issued in grant.go [1], stored [2] and again [1]." {
		t.Errorf("text = %q", got.Text)
	}
	if strings.Join(seen, "") != got.Text {
		t.Errorf("streamed %q, stored %q; the reader must see the record", strings.Join(seen, ""), got.Text)
	}
	if len(got.Citations) != 2 || got.Citations[0].Marker != 1 || got.Citations[1].Marker != 2 {
		t.Fatalf("citations = %+v, want markers 1 and 2", got.Citations)
	}
	if got.Citations[0].Path != "backend/internal/httpapi/grant.go" || got.Citations[1].Path != "backend/internal/playbackgrant/store.go" {
		t.Errorf("citations = %+v, want 1 to be the source the model called [2]", got.Citations)
	}
}

func TestAnswer_aGroupedMarkerIsRenumberedPerNumber(t *testing.T) {
	c, _, _ := streamUpstream(t, "Compared on poll [2, 1] and [9, 2].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// The real group is renumbered, sorted and split one number per bracket.
	// The group carrying an invented number keeps the shape it came in with,
	// so the UI can drop that number to plain text where it stands.
	if got.Text != "Compared on poll [1][2] and [9, 1]." {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 2 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_aMarkerInsideInlineCodeIsNotRenumbered(t *testing.T) {
	// The span is split across tokens: the line is held back until the
	// closing backtick says it is code.
	c, _, _ := streamUpstream(t, "Use `args[", "2]` as in grant.go [2].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Use `args[2]` as in grant.go [1]." {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_anUnclosedBacktickIsProseAtTheEnd(t *testing.T) {
	// withoutCode reads an inline span only when it closes on the same line;
	// a stray backtick with a marker after it is prose, and the marker counts.
	c, _, _ := streamUpstream(t, "A stray ` and then [2]")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "A stray ` and then [1]" {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_aCutAnswerIsStillRenumberedAndFlushed(t *testing.T) {
	c, _, _ := streamUpstreamEnding(t, "length", []string{"Stored [2] and then [", "1"})
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// The half-written marker at the cut is text, and it reaches the reader.
	if got.Text != "Stored [1] and then [1" || strings.Join(seen, "") != got.Text {
		t.Errorf("text = %q, streamed %q", got.Text, strings.Join(seen, ""))
	}
	if len(got.Citations) != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_withoutSourcesItSaysSoAndNeverCallsTheModel(t *testing.T) {
	// "No hit means no hit." Asking the model anyway would get a fluent answer
	// built from nothing but the question and the system prompt.
	c, _, calls := streamUpstream(t, "I suspect that ...")

	got, err := NewAnswerer(c).Answer(context.Background(), "How does shipping work?", AudienceBA, LanguageEN, nil, Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if *calls != 0 {
		t.Errorf("the model was called %d times with nothing gathered", *calls)
	}
	if !strings.Contains(strings.ToLower(got.Text), "found nothing") {
		t.Errorf("text = %q, want it to say nothing was found", got.Text)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none", got.Citations)
	}
}

func TestAnswer_theAudienceReachesThePrompt(t *testing.T) {
	// The role changes only this step: language level, depth, whether code is
	// embedded. A prompt that ignored it would make the BA/DEV switch decorative.
	cBA, promptBA, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cBA).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cDev, promptDev, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cDev).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if *promptBA == *promptDev {
		t.Fatal("the BA and DEV prompts are identical; the role switch does nothing")
	}
	if !strings.Contains(*promptBA, "[1]") || !strings.Contains(*promptBA, "playbackgrant/store.go") {
		t.Error("the sources never reached the prompt with their markers")
	}
	// The UI colours a fence by its tag and guesses nothing; an untagged
	// fence stays plain. Only the DEV answer carries code; both may carry the
	// one fence that is not code, the diagram.
	if !strings.Contains(*promptDev, "```go") {
		t.Error("the DEV prompt does not ask for a language tag on fenced code")
	}
	if strings.Contains(*promptBA, "```go") {
		t.Error("the BA prompt talks about fenced code, but a BA answer carries none")
	}
	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if !strings.Contains(p, "```mermaid") || !strings.Contains(p, "flowchart LR with two subgraphs") {
			t.Errorf("the %s prompt does not name the diagram fence and its types", name)
		}
		if !strings.Contains(p, "No citation markers inside the fence") {
			t.Errorf("the %s prompt lets a marker into the fence, where a bracket is syntax", name)
		}
	}
	// The diagram rule follows the audience block, so "the audience rules
	// above" are the ones the model just read.
	if strings.Index(*promptBA, "Audience: business analyst") > strings.Index(*promptBA, "```mermaid") {
		t.Error("the diagram rule comes before the audience block it refers to")
	}
	// The BA block names the picture in the reader's words, inside the
	// block: "control flow" in the diagram rule never fired on a process
	// question, and "then stop" a page earlier had already ended the answer.
	// It names the picture, never the fence, so the shape rules still
	// precede the first fence (asserted below).
	ba := *promptBA
	if end := strings.Index(ba, "Open with ONE sentence"); end < 0 ||
		!strings.Contains(ba[:end], "a flowchart of it") || !strings.Contains(ba[:end], "a sequence diagram") {
		t.Error("the BA audience block does not say when a picture belongs in the answer")
	}
	// The diagram rule's trigger is audience-neutral: a branch or a second
	// party earns the picture, whoever the parties are.
	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if !strings.Contains(p, "roles, systems and people") || !strings.Contains(p, "Steps with no branch and one party are a list") {
			t.Errorf("the %s diagram rule is not the audience-neutral trigger", name)
		}
		if !strings.Contains(p, "a second party are a diagram, not a list") {
			t.Errorf("the %s shape rule leaves the list permission to claim the flowchart's material", name)
		}
	}
	// The shape rules are about the whole answer, so BOTH audiences get them,
	// and they sit between the audience block and everything conditional: put
	// last, they would read as rules about whichever special case happened to
	// be appended.
	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if !strings.Contains(p, "Open with ONE sentence that answers the question") {
			t.Errorf("the %s prompt does not ask for the answer first", name)
		}
		if !strings.Contains(p, "never use a list where two sentences would do") {
			t.Errorf("the %s prompt permits a list without fencing it", name)
		}
		audience := "Audience: business analyst"
		if name == "DEV" {
			audience = "Audience: developer"
		}
		if strings.Index(p, "Open with ONE sentence") < strings.Index(p, audience) {
			t.Errorf("the %s shape rules come before the audience block", name)
		}
		if strings.Index(p, "Open with ONE sentence") > strings.Index(p, "```mermaid") {
			t.Errorf("the %s shape rules come after the conditional blocks", name)
		}
	}
	// The block describes the reader by what they decide and what they know,
	// and every clause names a move the model makes. A job title alone left
	// what a business analyst is to the model's training; a sentence about
	// what the reader lacks left the behaviour to be inferred from a ban.
	// "The person who will read this answer" binds the pronoun: "the reader"
	// recurs unbound in the memory block and the values paragraph, and
	// "Audience: business analyst" names a category, not a person.
	// The constant is wrapped, so a phrase is matched against the prompt with
	// its newlines folded to spaces: the assertion is about the wording, not
	// about where the source happens to break a line.
	flatBA := strings.Join(strings.Fields(*promptBA), " ")
	flatDev := strings.Join(strings.Fields(*promptDev), " ")
	for _, want := range []string{
		"The person who will read this answer decides what the system is supposed to do and checks whether it does it",
		"Write in that vocabulary and spend the words on the mechanism",
		"say what each system does for the business",
		"state the effect the business sees",
	} {
		if !strings.Contains(flatBA, want) {
			t.Errorf("the BA audience block does not say %q", want)
		}
		if strings.Contains(flatDev, want) {
			t.Errorf("the DEV prompt carries the Analyst persona sentence %q", want)
		}
	}

	// Deliberately no headings: a short answer wearing three of them looks
	// over-built, and that is a judgement the model gets wrong more often than
	// it gets the list wrong. answerLanguage names headings for a different
	// reason — whatever the answer uses is written in the reader's language —
	// so what is asserted here is that nothing asks for one to be emitted.
	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if strings.Contains(p, "###") {
			t.Errorf("the %s prompt asks for headings; they were left out on purpose", name)
		}
	}
}

// TestAnswer_theIntentSharpensTheOpeningSentence: the shape rule says to open
// with one sentence that answers the question, and what that means is
// different for a WHERE, a WHY and a yes/no question. Each intent adds its own
// line, and nothing else changes.
func TestAnswer_theIntentSharpensTheOpeningSentence(t *testing.T) {
	for intent, want := range map[string]string{
		"where":       "The question asks WHERE: open by saying every place the mechanism lives",
		"why":         "The question asks WHY: state the condition or rule that decides it",
		"conformance": "answer yes or no in the first sentence",
	} {
		c, prompt, _ := streamUpstream(t, "x")
		if _, err := NewAnswerer(c).Answer(context.Background(), "Wo?", AudienceBA, LanguageEN, twoSources(),
			Scope{Intent: intent}, "", nil); err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if !strings.Contains(*prompt, want) {
			t.Errorf("the %q prompt does not carry its own rule:\n%s", intent, *prompt)
		}
		// It refines the shape rule, so it follows it, and it is not a
		// special case appended after the conditional blocks.
		if strings.Index(*prompt, want) < strings.Index(*prompt, "Open with ONE sentence") {
			t.Errorf("the %q rule comes before the shape rules it refines", intent)
		}
		if strings.Index(*prompt, want) > strings.Index(*prompt, "```mermaid") {
			t.Errorf("the %q rule comes after the conditional blocks", intent)
		}
	}
}

// TestAnswer_anIntentWithNoRuleAddsNothing: "how" is what the shape rule was
// written for, and the intent comes from a model — a word this prompt has no
// rule for must leave it exactly as it was, never a line about the word.
func TestAnswer_anIntentWithNoRuleAddsNothing(t *testing.T) {
	base, basePrompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(base).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(),
		Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	for _, intent := range []string{"how", "sideways"} {
		c, prompt, _ := streamUpstream(t, "x")
		if _, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(),
			Scope{Intent: intent}, "", nil); err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if *prompt != *basePrompt {
			t.Errorf("intent %q changed the prompt; only where, why and conformance have a rule", intent)
		}
	}
}

func TestAnswer_anEmptyCompletionIsAnErrorNotAnAnswer(t *testing.T) {
	// An upstream that ends cleanly without one content delta must not become
	// a finished turn with nothing in it: the reader would see a Done mark over
	// an empty answer and nothing would be logged.
	c, _, _ := streamUpstream(t)
	a := NewAnswerer(c)

	_, err := a.Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil)

	if err == nil {
		t.Fatal("Answer: nil error on an empty completion")
	}
	if !strings.Contains(err.Error(), "no answer text") {
		t.Errorf("err = %v, want it to say the model wrote nothing", err)
	}
}

func TestAnswer_aCutAnswerKeepsWhatTheReaderAlreadySaw(t *testing.T) {
	// finish_reason=length after content: the text streamed to the browser
	// must not vanish from the record. Empty is a failure; truncated is an
	// answer with a log line.
	c, _, _ := streamUpstreamEnding(t, "length", []string{"The grant ", "is created in store.go [1]."})
	a := NewAnswerer(c)

	got, err := a.Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil)

	if err != nil {
		t.Fatalf("Answer: %v, want the partial text kept", err)
	}
	if !strings.Contains(got.Text, "store.go [1]") || len(got.Citations) != 1 {
		t.Errorf("answer = %+v, want the text that arrived, with its citation", got)
	}
}

func TestAnswer_aCutAnswerWithNoTextIsStillAnError(t *testing.T) {
	c, _, _ := streamUpstreamEnding(t, "length", nil)

	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil)

	if err == nil || !strings.Contains(err.Error(), "length") {
		t.Fatalf("err = %v, want the length failure surfaced", err)
	}
}

func TestAnswer_theCorpusWideMarkerRuleReachesBothPrompts(t *testing.T) {
	// An off-corpus question ("should I buy shares?") is refused correctly, and
	// the refusal's one claim is about the source set as a whole. Read against
	// "every statement carries its marker" alone, that claim wants every marker
	// there is: one turn came back with all 58 gathered sources enumerated in a
	// single bracket run. The rule that a claim about the sources is not a
	// claim any passage makes is what stops it, and it belongs to both roles.
	// Its absence half has to be the strict one: three chips under a refusal
	// open three files that say nothing about what was asked.
	cBA, promptBA, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cBA).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cDev, promptDev, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cDev).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if !strings.Contains(p, "about the sources as a whole") {
			t.Errorf("the %s prompt never says a claim about the source set is not a passage's claim", name)
		}
		if !strings.Contains(p, "never enumerate the sources") {
			t.Errorf("the %s prompt does not forbid enumerating the sources", name)
		}
		if !strings.Contains(p, "carries no marker at all") {
			t.Errorf("the %s prompt lets a claim about absence keep a few markers as examples", name)
		}
		if !strings.Contains(p, "never cite a few of them as examples of the silence") {
			t.Errorf("the %s prompt does not close the examples-of-silence reading", name)
		}
	}
}

func TestAnswer_aRefusalThatEnumeratesEveryMarkerIsStillResolved(t *testing.T) {
	// The rule above is a prompt change, and a prompt is a request. A model
	// that ignores it and enumerates the corpus anyway must still produce a
	// correct record: every marker resolves, in first-use order, with nothing
	// invented and nothing dropped. This is the guard, not an assertion that
	// the model obeys.
	sources := make([]Source, 58)
	for i := range sources {
		sources[i] = Source{
			ChunkID: int64(i + 1), Repo: "netra", Branch: "master",
			Path:      fmt.Sprintf("internal/hub/read/file%d.go", i+1),
			StartLine: 1, EndLine: 10, Text: "package read", Reason: "hit",
		}
	}
	var run strings.Builder
	for i := 1; i <= len(sources); i++ {
		fmt.Fprintf(&run, "[%d]", i)
	}
	c, _, _ := streamUpstream(t, "There is no information about shares here", run.String(), ".")

	got, err := NewAnswerer(c).Answer(context.Background(), "Should I buy shares?", AudienceBA, LanguageEN, sources, Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != len(sources) {
		t.Fatalf("citations = %d, want all %d markers resolved", len(got.Citations), len(sources))
	}
	for i, cit := range got.Citations {
		if cit.Marker != i+1 || cit.Path != sources[i].Path {
			t.Fatalf("citation %d = %+v, want marker %d on %s", i, cit, i+1, sources[i].Path)
		}
	}
}

// TestReachedVia_namesHowEachSourceArrived: the prompt says how a source got
// there, and the three ways are told apart. A chunk the gap pass fetched was
// reached from no file at all, so it names the lookup instead of a near side
// the answer could otherwise claim called it.
func TestReachedVia_namesHowEachSourceArrived(t *testing.T) {
	for _, c := range []struct{ reason, want string }{
		{"reference:NewGrant", "reached via NewGrant"},
		{"edge:destination shipping-task from shipping/S.java",
			"reached in another repository, which shares the destination shipping-task from shipping/S.java"},
		{"gap:unitPrice", "looked up by name after reading the sources: unitPrice"},
		{"gap:/paymentAuth", "looked up by name after reading the sources: /paymentAuth"},
	} {
		if got := reachedVia(c.reason); got != c.want {
			t.Errorf("reachedVia(%q) = %q, want %q", c.reason, got, c.want)
		}
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
