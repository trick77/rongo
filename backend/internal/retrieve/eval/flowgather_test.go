// What reaches the answer on the flow corpus.
//
// TestFlowEdgeReach measures the edge table on its own and TestFlowLoopDiagnostic
// measures a tool loop that is not the product. This is the product: the
// expanded question through retrieve.Search, then ask.Gatherer with the deployed
// bounds, and the question is which of a flow's parts are among the SOURCES the
// answer is written from. Three arms, so the crossing's contribution is read
// against the symbol walk it sits on rather than against nothing.
//
// Run it after TestEvalIndex has built the flow corpus and
// TestExpandFlowQuestions has frozen the expansions:
//
//	hack/run-flow-eval.sh TestExpandFlowQuestions
//	hack/run-flow-eval.sh TestFlowGathered
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// flowExpansionsFile freezes the understanding step's output per flow
// question, for the reason expansions.json exists: the model is not
// deterministic, and a measurement of retrieval must not move with its mood.
const flowExpansionsFile = "flow-expansions.json"

// TestExpandFlowQuestions freezes one expansion per flow question. One
// short-gate call each; BACKEND_EVAL_EXPAND=missing keeps what is frozen and
// calls the model only for questions without an entry.
func TestExpandFlowQuestions(t *testing.T) {
	requireEval(t)
	base := os.Getenv("BACKEND_LLM_BASE_URL")
	if base == "" {
		t.Skip("BACKEND_LLM_BASE_URL is unset")
	}
	u := ask.NewUnderstander(llm.NewClient(llm.Config{
		BaseURL: base,
		APIKey:  os.Getenv("BACKEND_LLM_API_KEY"),
		Timeout: 2 * time.Minute,
	}, nil))

	previous := map[string]expansion{}
	if body, err := os.ReadFile(flowExpansionsFile); err == nil {
		var list []expansion
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("parse %s: %v", flowExpansionsFile, err)
		}
		for _, e := range list {
			previous[e.Question] = e
		}
	}

	var out []expansion
	for _, q := range loadFlowQuestions(t) {
		if old, ok := previous[q.Text]; ok && expandOnlyMissing() {
			out = append(out, old)
			continue
		}
		var got ask.Understanding
		var last error
		for attempt := 1; attempt <= expandAttempts; attempt++ {
			got, last = u.Understand(context.Background(), q.Text, ask.Thread{})
			if last == nil {
				break
			}
			t.Logf("RETRY %d/%d %s: %v", attempt, expandAttempts, short(q.Text), last)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if last != nil {
			if old, ok := previous[q.Text]; ok {
				out = append(out, old)
			}
			t.Errorf("understand %q: %v", q.Text, last)
			continue
		}
		texts := got.SearchTexts(q.Text)
		t.Logf("EXPANDED %-60s -> %v repos=%v", short(q.Text), texts[1:], got.Repos)
		out = append(out, expansion{Question: q.Text, Texts: texts, Repos: got.Repos})
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(flowExpansionsFile, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", flowExpansionsFile, err)
	}
}

func loadFlowExpansions(t *testing.T) map[string]expansion {
	t.Helper()
	body, err := os.ReadFile(flowExpansionsFile)
	if err != nil {
		t.Skipf("no %s yet; run TestExpandFlowQuestions first", flowExpansionsFile)
	}
	var list []expansion
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse %s: %v", flowExpansionsFile, err)
	}
	out := map[string]expansion{}
	for _, e := range list {
		out[e.Question] = e
	}
	return out
}

// flowGatherArm is one configuration of search-then-gather.
type flowGatherArm struct {
	name        string
	hops        int
	noCrossings bool
}

// TestFlowGathered reports, per arm and per question, which parts of the flow
// are among the sources, at which hop, and by what reason — hit, symbol
// reference, or a crossing on a named queue or route. Scored on files, the
// way TestFlowEdgeReach and the loop diagnostic are, so the three tables read
// against each other.
func TestFlowGathered(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	retriever := retrieve.New(db, embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil))
	expansions := loadFlowExpansions(t)
	questions := loadFlowQuestions(t)
	deployed := gatherOpts(t)

	arms := []flowGatherArm{
		{name: "search only", hops: 0, noCrossings: true},
		{name: "symbol walk", hops: deployed.MaxHops, noCrossings: true},
		{name: "symbol walk + crossings (the product)", hops: deployed.MaxHops},
	}

	// Searched once per question, shared across the arms: the arms differ
	// in gathering only, and a second embedding call per arm would add the
	// embedding endpoint's own variance to a comparison of the walk.
	hitsFor := map[string][]retrieve.Hit{}
	for _, q := range questions {
		e, ok := expansions[q.Text]
		if !ok {
			t.Fatalf("no frozen expansion for %q; run TestExpandFlowQuestions", q.Text)
		}
		hits, err := retriever.Search(ctx, retrieve.Query{Texts: e.Texts, Repos: e.Repos, Question: q.Text, K: gatherSearchK})
		if err != nil {
			t.Fatalf("search %q: %v", q.Text, err)
		}
		hitsFor[q.Text] = hits
	}

	for _, arm := range arms {
		g := ask.NewGatherer(db, ask.GatherOptions{MaxHops: arm.hops, TokenBudget: deployed.TokenBudget, NoCrossings: arm.noCrossings})
		var totalParts, totalReached, totalSources, whole int
		t.Logf("\n=== arm: %s", arm.name)
		for _, q := range questions {
			sources, err := g.Gather(ctx, hitsFor[q.Text])
			if err != nil {
				t.Fatalf("gather %q: %v", q.Text, err)
			}
			parts := q.parts()
			reached := 0
			var lines, missing []string
			for _, p := range parts {
				hop, reason := hopAndReason(sources, p)
				if hop < 0 {
					missing = append(missing, p.String())
					continue
				}
				reached++
				lines = append(lines, fmt.Sprintf("    hop %d %-14s %s", hop, reasonKind(reason), p.String()))
			}
			totalParts += len(parts)
			totalReached += reached
			totalSources += len(sources)
			if reached == len(parts) {
				whole++
			}
			sort.Strings(missing)
			t.Logf("  %-70s %d/%d parts, %d sources", short(q.Text), reached, len(parts), len(sources))
			for _, l := range lines {
				t.Log(l)
			}
			if len(missing) > 0 {
				t.Logf("    never gathered: %s", strings.Join(missing, ", "))
			}
		}
		t.Logf("  %s: %d/%d parts, %d/%d questions whole, mean sources %.1f",
			arm.name, totalReached, totalParts, whole, len(questions), float64(totalSources)/float64(len(questions)))
	}
}

// hopAndReason is hopOfCandidate for one flow part: the lowest hop at which
// the file entered the sources, and the reason of that entry.
func hopAndReason(sources []ask.Source, p flowPart) (int, string) {
	best, reason := -1, ""
	for _, s := range sources {
		if s.Repo == p.Repo && s.Path == p.Path && (best < 0 || s.Hop < best) {
			best, reason = s.Hop, s.Reason
		}
	}
	return best, reason
}

// reasonKind folds a source's reason to its class, for a column that lines up.
func reasonKind(reason string) string {
	switch {
	case reason == "hit":
		return "hit"
	case strings.HasPrefix(reason, "edge:"):
		return "crossing"
	default:
		return "reference"
	}
}
