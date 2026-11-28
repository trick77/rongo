package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/toolkit/llmwire"
	"github.com/trick77/toolkit/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/llm/llmtest"
	"github.com/trick77/rongo/internal/usage"
)

func TestCallTools_replaysTheModelsOwnTurn(t *testing.T) {
	// Given: a model on the Messages protocol, whose every answer carries a
	// signed thinking block that has to go back with the next round, as
	// claude-opus-5-5's does. A turn rebuilt from its text and calls loses
	// it, and the model re-thinks each round without its earlier reasoning.
	srv := llmwiretest.NewServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Registry: llmtest.Registry(),
		Answer: llmwiretest.MessagesModel, Gate: llmwiretest.MessagesModel}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ToolMessage{{Role: "user", Content: "Wie wird die Anzahl Haustiere an Ledger uebermittelt?"}}
	turn, err := c.CallTools(context.Background(), msgs, locateTools())
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// When: the turn goes back as the model's own.
	msgs = append(msgs, turn.Assistant(), ToolMessage{Role: "user", Content: "und weiter?"})
	if _, err := c.CallTools(context.Background(), msgs, locateTools()); err != nil {
		t.Fatalf("CallTools, round 2: %v", err)
	}

	// Then: the signed block is on the wire.
	if !srv.Last().ReplayedThinking() {
		t.Error("round 2 did not carry the model's signed thinking back")
	}
}

// messagesClient is a client whose both lanes run the Messages protocol, the
// one where caching is a mark on the request.
func messagesClient(t *testing.T) (*Client, *llmwiretest.Server) {
	t.Helper()
	srv := llmwiretest.NewServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Registry: llmtest.Registry(),
		Answer: llmwiretest.MessagesModel, Gate: llmwiretest.MessagesModel}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

// systemMarked reports whether the request's system block carries a cache mark.
func systemMarked(body map[string]any) bool {
	blocks, _ := body["system"].([]any)
	for _, b := range blocks {
		if block, _ := b.(map[string]any); block["cache_control"] != nil {
			return true
		}
	}
	return false
}

func TestCallTools_aRoundWithToolsCachesTheConversation(t *testing.T) {
	// Given: a locate round, tools on offer. The next round resends this whole
	// request extended by its results, so it is the prefix worth a mark.
	c, srv := messagesClient(t)
	msgs := []ToolMessage{{Role: "system", Content: "Find the place that answers."},
		{Role: "user", Content: "Wie wird die Anzahl Haustiere an Ledger uebermittelt?"}}

	if _, err := c.CallTools(context.Background(), msgs, locateTools()); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then: the moving breakpoint and the system mark are on the wire.
	body := srv.Last().Body
	if body["cache_control"] == nil {
		t.Error("a tool round went out without the conversation's cache mark")
	}
	if !systemMarked(body) {
		t.Error("a tool round went out without the system block's cache mark")
	}
}

func TestCallTools_aCallWithoutToolsIsNotMarked(t *testing.T) {
	// Given: the locate conclusion, the one call without tools. It ends the
	// loop, and its prefix differs from the rounds' from the tools on: a mark
	// would write the whole conversation at the write premium for nothing to
	// read.
	c, srv := messagesClient(t)
	msgs := []ToolMessage{{Role: "system", Content: "Find the place that answers."},
		{Role: "user", Content: "Stop looking."}}

	if _, err := c.CallTools(context.Background(), msgs, nil); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	if body := srv.Last().Body; body["cache_control"] != nil || systemMarked(body) {
		t.Errorf("a call without tools carried a cache mark: %v", body)
	}
	// Nor a tool choice: llmwire drops one with no tools and warns, a warning
	// every conclusion would log.
	if body := srv.Last().Body; body["tool_choice"] != nil {
		t.Errorf("tool_choice = %v on a call without tools", body["tool_choice"])
	}
}

func TestCallTools_withoutCacheSendsNoMarkThoughToolsAreOffered(t *testing.T) {
	// The loop's last round: tools on offer, but what follows is the
	// conclusion, which sends no tools and so reads none of it.
	c, srv := messagesClient(t)
	msgs := []ToolMessage{{Role: "system", Content: "Find the place that answers."}, {Role: "user", Content: "q"}}

	if _, err := c.CallTools(context.Background(), msgs, locateTools(), WithoutCache()); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	body := srv.Last().Body
	if body["cache_control"] != nil || systemMarked(body) {
		t.Errorf("WithoutCache still carried a cache mark: %v", body)
	}
	if body["tools"] == nil || body["tool_choice"] == nil {
		t.Errorf("WithoutCache dropped the tools: %v", body)
	}
}

func TestComplete_aOneOffCallIsNotMarked(t *testing.T) {
	// Answer, rerank, route, gap, title, followups: one-off prefixes. A mark
	// on any of them is a surcharge with no read behind it.
	c, srv := messagesClient(t)
	msgs := []Message{{Role: "system", Content: "Answer from the sources."}, {Role: "user", Content: "q"}}

	if _, _, err := c.Complete(context.Background(), msgs); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if body := srv.Last().Body; body["cache_control"] != nil || systemMarked(body) {
		t.Errorf("a one-off call carried a cache mark: %v", body)
	}
}

func TestCallTools_cachedTokensCountTowardThePrompt(t *testing.T) {
	// Given: a Messages reply whose input lanes are disjoint: input_tokens is
	// only the uncached part. The turn ceiling reads the meter's total, so a
	// cached round counted as its 4 uncached tokens would let a loop run past
	// it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "message", "role": "assistant",
			"content":     []any{map[string]any{"type": "text", "text": "FOUND: x"}},
			"stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 4, "output_tokens": 5,
				"cache_read_input_tokens": 11215, "cache_creation_input_tokens": 300}})
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Registry: llmtest.Registry(),
		Answer: llmwiretest.MessagesModel, Gate: llmwiretest.MessagesModel}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	meter := usage.New()
	ctx := usage.WithMeter(context.Background(), meter)

	if _, err := c.CallTools(ctx, []ToolMessage{{Role: "user", Content: "q"}}, locateTools()); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	if got, want := meter.Total(), 4+11215+300+5; got != want {
		t.Errorf("meter total = %d, want %d: every input lane counted", got, want)
	}
}

func TestToolTurn_assistantIsRebuiltOnceTheCallsChange(t *testing.T) {
	// Given: a turn with two calls, and the replay items that carry both.
	calls := []ToolCall{{ID: "a", Name: "grep", Arguments: "{}"}, {ID: "b", Name: "grep", Arguments: "{}"}}
	turn := ToolTurn{Calls: calls, own: llmwire.Message{Role: llmwire.RoleAssistant,
		ToolCalls:     []llmwire.ToolCall{{ID: "a", Name: "grep", Arguments: "{}"}, {ID: "b", Name: "grep", Arguments: "{}"}},
		ResponseItems: []json.RawMessage{json.RawMessage(`{"type":"thinking"}`)}}}

	// Then: the turn as the model sent it carries its replay items.
	if got := turn.Assistant(); got.replay == nil {
		t.Error("an unchanged turn lost its replay items")
	}
	// The signed blocks name every call they carry: a turn whose calls the
	// caller trimmed, reordered or replaced goes back rebuilt.
	for name, changed := range map[string][]ToolCall{
		"trimmed":   calls[:1],
		"reordered": {calls[1], calls[0]},
		"replaced":  {calls[0], {ID: "c", Name: "grep", Arguments: "{}"}},
		"repaired":  {calls[0], {ID: "b", Name: "grep", Arguments: `{"pattern":"x"}`}},
	} {
		turn.Calls = changed
		if got := turn.Assistant(); got.replay != nil || len(got.Calls) != len(changed) {
			t.Errorf("%s: turn = %+v, want rebuilt from the calls kept", name, got)
		}
	}
}

func TestWireMessages_aCallingTurnCarriesItsReplayAndItsResults(t *testing.T) {
	// The locate loop's own shape: the model's calling turn, replayed, then
	// its results. llmwire gets the calls beside the items they must agree
	// with, and the results addressed to them.
	items := []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"x","signature":"s"}`),
		json.RawMessage(`{"type":"tool_use","id":"a","name":"grep","input":{}}`)}
	turn := ToolTurn{Calls: []ToolCall{{ID: "a", Name: "grep", Arguments: "{}"}},
		own: llmwire.Message{Role: llmwire.RoleAssistant, ToolCalls: []llmwire.ToolCall{{ID: "a", Name: "grep", Arguments: "{}"}},
			ResponseItems: items}}

	got := wireMessages([]ToolMessage{{Role: "user", Content: "q"}, turn.Assistant(), ToolResult("a", "hit")})

	if len(got) != 3 || len(got[1].ResponseItems) != 2 || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "a" {
		t.Fatalf("assistant turn = %+v, want the replay items beside the call", got[1])
	}
	if got[2].ToolCallID != "a" {
		t.Errorf("result answers %q, want a", got[2].ToolCallID)
	}
}

// The tools here are the ones the locate loop actually gives the model, and
// the arguments are terms from the question that motivated it ("wie wird die
// Anzahl Haustiere an Ledger uebermittelt"). Placeholder names would test that a
// round trip round-trips; these test that the round trip carries what a caller
// will really send. A cap tested with stems called alpha, bravo and charlie
// shipped a feature that never worked on its own case.

// capturedTools is what the wire sent, decoded far enough to see the tool
// halves. captured's Messages are []Message, which carries no tool fields by
// design — the ordinary path has none — so the tool shape is decoded here
// rather than widening a type five other tests share.
type capturedTools struct {
	Messages []struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	ToolChoice any `json:"tool_choice"`
}

// toolUpstream answers with the tool calls given, then records what it was
// sent. A reply with no calls is the model answering.
func toolUpstream(t *testing.T, content string, finishReason string, calls ...map[string]any) (*Client, *capturedTools) {
	t.Helper()
	got := &capturedTools{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, got); err != nil {
			t.Errorf("upstream got unparseable body: %v", err)
		}
		msg := map[string]any{"content": content}
		if len(calls) > 0 {
			wire := make([]any, 0, len(calls))
			for i, c := range calls {
				wire = append(wire, map[string]any{
					"id":   c["id"],
					"type": "function",
					"function": map[string]any{
						"name":      c["name"],
						"arguments": c["arguments"],
					},
					"index": i,
				})
			}
			msg["tool_calls"] = wire
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": finishReason}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
	}))
	t.Cleanup(srv.Close)
	return mustClient(t, Config{BaseURL: srv.URL, APIKey: "s3cret"}, srv.Client()), got
}

func locateTools() []Tool {
	return []Tool{{
		Name:        "grep",
		Description: "Substring scan over the indexed corpus.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
		},
	}}
}

func TestCallTools_returnsWhatTheModelWantsCalledAndIsNotDone(t *testing.T) {
	// Given: the model asks for the scan that finds setAnzahlhaustiere inside a
	// larger token, which is the whole reason the loop exists.
	c, _ := toolUpstream(t, "", "tool_calls", map[string]any{
		"id": "call_1", "name": "grep", "arguments": `{"pattern":"setAnzahlhaustiere"}`,
	})

	// When
	turn, err := c.CallTools(context.Background(),
		[]ToolMessage{{Role: "user", Content: "Wie wird die Anzahl Haustiere an Ledger uebermittelt?"}},
		locateTools())
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then
	if turn.Done() {
		t.Error("a turn asking for a tool must not be Done")
	}
	if len(turn.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(turn.Calls))
	}
	got := turn.Calls[0]
	if got.ID != "call_1" || got.Name != "grep" {
		t.Errorf("call = %+v, want id call_1 name grep", got)
	}
	// The arguments reach the caller as the model wrote them: only the caller
	// knows what shape its own tool takes.
	if got.Arguments != `{"pattern":"setAnzahlhaustiere"}` {
		t.Errorf("arguments = %q, want the model's own JSON", got.Arguments)
	}
}

func TestCallTools_aReplyWithNoCallsIsDone(t *testing.T) {
	// Given
	c, _ := toolUpstream(t, "Der Wert wird im Converter gesetzt.", "stop")

	// When
	turn, err := c.CallTools(context.Background(),
		[]ToolMessage{{Role: "user", Content: "Wie wird die Anzahl Haustiere an Ledger uebermittelt?"}},
		locateTools())
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then
	if !turn.Done() {
		t.Error("a reply with no calls must be Done")
	}
	if turn.Content != "Der Wert wird im Converter gesetzt." {
		t.Errorf("content = %q", turn.Content)
	}
}

func TestCallTools_sendsTheAssistantTurnAndTheResultItAnswers(t *testing.T) {
	// Given: the conversation as it stands after one round — the model asked,
	// the caller ran the scan, and now both halves go back. A tool
	// conversation missing the assistant turn is one the model cannot follow.
	c, got := toolUpstream(t, "", "stop")
	calls := []ToolCall{{ID: "call_1", Name: "grep", Arguments: `{"pattern":"setAnzahlhaustiere"}`}}

	// When
	if _, err := c.CallTools(context.Background(), []ToolMessage{
		{Role: "user", Content: "Wie wird die Anzahl Haustiere an Ledger uebermittelt?"},
		AssistantCalls("", calls),
		ToolResult("call_1", "ConverterPetRegistry.java:162"),
	}, locateTools()); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then: three messages, and the tool result addressed to the call it
	// answers.
	if len(got.Messages) != 3 {
		t.Fatalf("upstream got %d messages, want 3", len(got.Messages))
	}
	if r := got.Messages[1].Role; r != "assistant" {
		t.Errorf("second message role = %q, want assistant", r)
	}
	if len(got.Messages[1].ToolCalls) != 1 || got.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("the assistant turn must carry the call it asked for, got %+v", got.Messages[1].ToolCalls)
	}
	if n := got.Messages[1].ToolCalls[0].Function.Name; n != "grep" {
		t.Errorf("the assistant turn's call = %q, want grep", n)
	}
	if r := got.Messages[2].Role; r != "tool" {
		t.Errorf("third message role = %q, want tool", r)
	}
	if id := got.Messages[2].ToolCallID; id != "call_1" {
		t.Errorf("tool result answers %q, want call_1", id)
	}
}

func TestCallTools_sendsTheToolsAndLetsTheModelChoose(t *testing.T) {
	// Given
	c, got := toolUpstream(t, "ok", "stop")

	// When
	if _, err := c.CallTools(context.Background(),
		[]ToolMessage{{Role: "user", Content: "x"}}, locateTools()); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "grep" {
		t.Fatalf("upstream got tools %+v, want one called grep", got.Tools)
	}
	if got.ToolChoice != "auto" {
		t.Errorf("tool_choice = %v, want auto", got.ToolChoice)
	}
}

func TestCallTools_recordsEveryRoundIntoTheMeterUnderItsStep(t *testing.T) {
	// Given: a loop's rounds are the one place in the product where a single
	// turn makes an unbounded number of calls, so each has to land in the
	// per-turn total a reader is shown.
	c, _ := toolUpstream(t, "ok", "stop")
	m := usage.New()
	ctx := usage.WithMeter(context.Background(), m)

	// When
	if _, err := c.CallTools(ctx, []ToolMessage{{Role: "user", Content: "x"}},
		locateTools(), ShortGate(), WithStep("locate")); err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	// Then
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	if calls[0].Step != "locate" || calls[0].Model != testGateModel {
		t.Errorf("call = %+v, want step locate on the short-gate deployment", calls[0])
	}
	if calls[0].Prompt != 11 || calls[0].Completion != 7 {
		t.Errorf("call = %+v, want the upstream's own numbers", calls[0])
	}
}

func TestCallTools_refusesTheNextRoundOnceTheTurnHasSpentItsCeiling(t *testing.T) {
	// Given: the tripwire BACKEND_TURN_MAX_TOKENS exists for exactly this —
	// "once a loop or a retry that nobody bounded appears". A runaway loop is
	// refused at its next round rather than discovered on the invoice.
	c, _ := toolUpstream(t, "ok", "stop")
	m := usage.New()
	c.turnMaxTokens = 10
	ctx := usage.WithMeter(context.Background(), m)

	// When: the first round fits, and its 18 tokens put the turn over.
	if _, err := c.CallTools(ctx, []ToolMessage{{Role: "user", Content: "x"}}, locateTools()); err != nil {
		t.Fatalf("first round: %v", err)
	}
	_, err := c.CallTools(ctx, []ToolMessage{{Role: "user", Content: "x"}}, locateTools())

	// Then
	if !errors.Is(err, ErrTurnBudget) {
		t.Errorf("second round err = %v, want ErrTurnBudget", err)
	}
}

func TestCallTools_aTruncatedCallIsReportedAsTheCapNotAsBadJSON(t *testing.T) {
	// Given: a call cut at the cap carries half an argument. Unmarshalled by
	// the caller that would read as a malformed request from the model rather
	// than as the budget running out, so the cause has to travel.
	c, _ := toolUpstream(t, "", "length", map[string]any{
		"id": "call_1", "name": "grep", "arguments": `{"pattern":"setAnzahl`,
	})

	// When
	turn, err := c.CallTools(context.Background(),
		[]ToolMessage{{Role: "user", Content: "x"}}, locateTools())

	// Then
	var fe *FinishError
	if !errors.As(err, &fe) || fe.Reason != "length" {
		t.Fatalf("err = %v, want a FinishError saying length", err)
	}
	// Reported WITH what arrived: a caller holding earlier results may still
	// answer from them.
	if len(turn.Calls) != 1 {
		t.Errorf("the truncated call must still reach the caller, got %+v", turn.Calls)
	}
}
