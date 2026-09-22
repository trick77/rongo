package llm

import (
	"context"
	"time"

	"github.com/trick77/llmwire"
)

// The tool path. The package doc says "no tools, no image path", and that was
// true while nothing in the product called tools: adding the plumbing for a
// diagnostic would have put a code path into the product that only a test
// used, which is why internal/retrieve/eval/flowloop_test.go talks to llmwire
// directly instead.
//
// A shipping loop changes that premise. It lives here rather than in the
// caller for two reasons: llmwire.Chat is reachable only from this package
// (Client.wire is unexported), and everything this file adds — the per-turn
// ceiling, the usage meter, the attempt window — is what rongo means by a
// call. A loop that bypassed them would be the one caller in the product
// spending tokens nothing counted.
//
// Deliberately NOT here: the loop itself. This is one round trip that may come
// back asking for tools. Who calls what, how many rounds, and what a tool
// result means belong to the caller, which is the only place that knows.

// Tool is one function the model may call. Parameters is a JSON Schema object,
// passed to the wire as given.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one call the model asked for. Arguments is the raw JSON string
// the model produced: it is not validated here, because only the caller knows
// what shape each of its tools takes.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolTurn is one round trip of a tool conversation. Exactly one of Content
// and Calls is the interesting half: calls mean the model wants more before it
// answers, content with no calls means it is done.
type ToolTurn struct {
	Content string
	Calls   []ToolCall
	Usage   Usage
}

// Done reports whether the model stopped asking for tools. A caller loops
// while this is false and its own round budget is not spent.
func (t ToolTurn) Done() bool { return len(t.Calls) == 0 }

// ToolMessage is one message of a tool conversation. It is a superset of
// Message: an assistant turn carries the calls it asked for, and a tool result
// carries the id of the call it answers.
//
// Role is the same OpenAI-compatible vocabulary Message uses. A result message
// sets CallID and leaves Calls empty; an assistant turn asking for tools sets
// Calls and usually leaves Content empty.
type ToolMessage struct {
	Role    string
	Content string
	Calls   []ToolCall
	CallID  string
}

// AssistantCalls is the model's own turn, fed back so the next request carries
// what it asked for. A tool conversation the assistant turn is missing from is
// one the model cannot follow.
func AssistantCalls(content string, calls []ToolCall) ToolMessage {
	return ToolMessage{Role: "assistant", Content: content, Calls: calls}
}

// ToolResult is the answer to one call, addressed by its id.
func ToolResult(id, content string) ToolMessage {
	return ToolMessage{Role: "tool", Content: content, CallID: id}
}

// CallTools runs ONE request that may come back asking for tools, and reports
// what the model said or what it wants called. It does not execute anything
// and it does not loop: the caller owns the round budget, because only the
// caller knows when it has learned enough.
//
// The turn ceiling is checked before the request like every other call here,
// so a loop that runs away is refused at its next round with ErrTurnBudget
// rather than discovered on the invoice. The usage meter records each round
// under the step the caller named, so a loop's cost lands in the same per-turn
// total a reader sees for everything else.
//
// No retry. Complete retries an empty reply because a completion that
// delivered nothing is worth one more go; here an empty reply with no calls is
// the model declining to continue, which is an answer, and a round that failed
// leaves the caller holding results it can still write from.
func (c *Client) CallTools(ctx context.Context, msgs []ToolMessage, tools []Tool, opts ...Option) (ToolTurn, error) {
	o := resolve(opts)
	if err := c.underBudget(ctx); err != nil {
		return ToolTurn{}, err
	}
	ctx, cancel := c.attemptWindow(ctx, o)
	defer cancel()

	req := c.request(nil, o)
	req.Messages = wireMessages(msgs)
	req.Tools = wireTools(tools)
	req.ToolChoice = llmwire.ToolChoice{Mode: llmwire.ToolChoiceAuto}

	started := time.Now()
	resp, warnings, err := c.wire.Chat(ctx, req)
	c.warn(c.deployment(o.model), warnings)
	if err != nil {
		return ToolTurn{}, err
	}
	u := usageFrom(resp.Usage)
	record(ctx, o, c.deployment(o.model), u, time.Since(started))

	turn := ToolTurn{Content: resp.Content, Usage: u}
	for _, call := range resp.ToolCalls {
		turn.Calls = append(turn.Calls, ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	// A reply cut at the cap is not a reply, and it matters more here than in
	// complete: a truncated tool call carries half a JSON argument, which the
	// caller would unmarshal as a malformed request rather than as the budget
	// running out. Reported with whatever arrived, so a caller that can still
	// answer from what it has may do so.
	if fr := resp.FinishReason; fr != "" && fr != "stop" && fr != "tool_calls" {
		return turn, &FinishError{Reason: fr, Completion: u.Completion}
	}
	return turn, nil
}

func wireTools(tools []Tool) []llmwire.Tool {
	out := make([]llmwire.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, llmwire.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

func wireMessages(msgs []ToolMessage) []llmwire.Message {
	out := make([]llmwire.Message, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.CallID != "":
			out = append(out, llmwire.ToolResult(m.CallID, m.Content))
		case len(m.Calls) > 0:
			calls := make([]llmwire.ToolCall, 0, len(m.Calls))
			for _, c := range m.Calls {
				calls = append(calls, llmwire.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
			}
			out = append(out, llmwire.Message{Role: llmwire.RoleAssistant, Text: m.Content, ToolCalls: calls})
		default:
			out = append(out, llmwire.TextMessage(llmwire.Role(m.Role), m.Content))
		}
	}
	return out
}
