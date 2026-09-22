package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/usage"
)

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
	if calls[0].Step != "locate" || calls[0].Model != ShortGateDeployment {
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
