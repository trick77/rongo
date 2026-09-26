package ask

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/llm"
)

// The default call timeout must let the answer call write its whole cap at
// the slowest stream rate rongo plans for. The cap on the wire is the answer
// plus the reasoning allowance llmwire adds for the model, so this is checked
// against what each shipped model is actually sent, not against
// answerMaxTokens alone: a profile with a larger allowance must fail here, not
// cut an answer off in production.
func TestAnswerCap_fitsTheDefaultTimeoutOnEveryModel(t *testing.T) {
	reg := llmwire.Default()
	gates := reg.ChatModels(llmwire.Needs{Tools: true})
	if len(gates) == 0 {
		t.Fatal("no chat model with tools to stand in for the gate lane")
	}
	budget := int(config.DefaultLLMTimeout / time.Second * config.SlowestStreamTokensPerSecond)
	answers := reg.ChatModels(llmwire.Needs{Streaming: true})
	if len(answers) == 0 {
		t.Fatal("no streaming chat model")
	}
	for _, model := range answers {
		t.Run(model, func(t *testing.T) {
			srv := llmwiretest.NewServer(t)
			c, err := llm.NewClient(llm.Config{BaseURL: srv.URL, APIKey: "k", Answer: model, Gate: gates[0]}, srv.Server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Stream(context.Background(), []llm.Message{{Role: "user", Content: "x"}}, func(string) {},
				llm.WithMaxAnswerTokens(answerMaxTokens), llm.WithStep("answer")); err != nil {
				t.Fatal(err)
			}
			sent, ok := srv.Last().MaxTokens()
			if !ok {
				t.Fatal("the answer call carried no cap")
			}
			if sent > budget {
				t.Errorf("cap on the wire = %d tokens, but %v at %d tok/s writes only %d",
					sent, config.DefaultLLMTimeout, config.SlowestStreamTokensPerSecond, budget)
			}
		})
	}
}
