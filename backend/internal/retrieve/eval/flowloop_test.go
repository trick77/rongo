// Flow-loop diagnostic: can the model find a cross-repository flow if it is
// ALLOWED TO LOOK, or does one-shot retrieval fail because the flow is not
// findable at all?
//
// This is not a feature and must not become one. It answers the question that
// decides whether an edge table is load-bearing or merely an optimisation:
//
//   - the loop finds the parts  -> the flow IS findable; a graph would make it
//     cheaper and faster, nothing more.
//   - the loop does not find them -> the model cannot follow a queue name from
//     producer to consumer even with ripgrep in its hands, and the graph is
//     carrying the answer rather than accelerating it.
//
// It runs OUT OF BAND on purpose. internal/llm speaks one completion and one
// stream and says so ("No tools, no image path"): adding tool plumbing to the
// shipping client for a diagnostic would put a code path into the product that
// nothing in the product uses. So this file carries its own minimal
// tool-calling client, and internal/llm is left alone.
//
// Run it after TestEvalIndex has built the flow corpus:
//
//	set -a; . .env; set +a
//	BACKEND_EVAL=1 \
//	BACKEND_EVAL_DB=/tmp/rongo-flow.db \
//	BACKEND_REPOS_FILE=<worktree>/repos.yaml \
//	BACKEND_REPO_ROOT=/tmp/rongo-flow-repos \
//	go test -v -timeout 90m -run 'TestFlowLoop' ./internal/retrieve/eval/
package eval

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// flowToolBudget is how many ROUNDS one question gets, not how many tool calls:
// a model may put several calls in one assistant turn, and the first run
// measured questions spending 22 calls inside six rounds. Six is the number the
// review proposed, read as rounds; BACKEND_FLOW_BUDGET overrides it, because
// telling "cannot follow the flow" apart from "ran out of turns" needs the same
// questions at two budgets.
func flowToolBudget() int {
	if n := atoiOr(os.Getenv("BACKEND_FLOW_BUDGET"), 0); n > 0 {
		return n
	}
	return 6
}

// flowDeployments are the two arms. Both are MiMo: a frontier arm was
// considered and dropped, which means a DOUBLE failure here cannot separate
// "agentic search does not work for this" from "this model family cannot hold
// a six-call trajectory". Any conclusion drawn from two failures must say so.
var flowDeployments = []string{"mimo-v2.5-pro", "mimo-v2.5"}

// flowQuestion mirrors Question, but the candidates carry the evidence string
// that says what was verified in the pinned corpus.
type flowQuestion struct {
	Text       string          `json:"question"`
	Kind       string          `json:"kind"`
	Resolution Resolution      `json:"resolution"`
	Candidates []flowCandidate `json:"candidates"`
	Note       string          `json:"note"`
}

type flowCandidate struct {
	Repo     string   `json:"repo"`
	Paths    []string `json:"paths"`
	Verified string   `json:"verified"`
}

// parts flattens a question into the repo/path pairs an answer has to touch.
func (q flowQuestion) parts() []flowPart {
	var out []flowPart
	for _, c := range q.Candidates {
		for _, p := range c.Paths {
			out = append(out, flowPart{Repo: c.Repo, Path: p})
		}
	}
	return out
}

type flowPart struct {
	Repo string
	Path string
}

func (p flowPart) String() string { return p.Repo + "/" + p.Path }

func loadFlowQuestions(t *testing.T) []flowQuestion {
	t.Helper()
	body, err := os.ReadFile("flow-questions.json")
	if err != nil {
		t.Fatalf("read flow-questions.json: %v", err)
	}
	var qs []flowQuestion
	if err := json.Unmarshal(body, &qs); err != nil {
		t.Fatalf("parse flow-questions.json: %v", err)
	}
	if len(qs) == 0 {
		t.Fatal("flow-questions.json is empty")
	}
	return qs
}

// TestFlowQuestionsAreWellFormed is the cheap gate, and it runs without
// BACKEND_EVAL: a candidate without a verified string is a claim nobody
// checked, and the whole point of this corpus is that every part was read.
func TestFlowQuestionsAreWellFormed(t *testing.T) {
	qs := loadFlowQuestions(t)
	for _, q := range qs {
		if strings.TrimSpace(q.Text) == "" {
			t.Error("a question has no text")
		}
		if len(q.Candidates) == 0 {
			t.Errorf("%q has no candidates", q.Text)
		}
		if q.Resolution == ResolutionComposition && len(q.Candidates) < 2 {
			t.Errorf("%q is composition with one candidate", q.Text)
		}
		for _, c := range q.Candidates {
			if len(c.Paths) == 0 {
				t.Errorf("%q: candidate %s has no paths", q.Text, c.Repo)
			}
			if strings.TrimSpace(c.Verified) == "" {
				t.Errorf("%q: candidate %s carries no verified evidence", q.Text, c.Repo)
			}
		}
	}
}

// --- the tools -------------------------------------------------------------

// flowTools is what the model may call. Deliberately the capabilities rongo
// ALREADY has: the hybrid search it ships, ripgrep it already shells out to,
// the ctags symbols it already stores, and reading a file. Giving the loop a
// tool the product does not have would measure a product that does not exist.
func flowToolSpecs() []map[string]any {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "search",
				"description": "Hybrid semantic and keyword search over the indexed corpus. Returns matching code chunks with repository, path and line range.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": strProp("What to look for, in natural language or code vocabulary."),
						"repo":  strProp("Optional repository name to restrict the search to."),
					},
					"required": []string{"query"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "grep",
				"description": "Literal or regular-expression search over the checked-out source, like ripgrep. Use it to follow an exact string such as a queue name or a route across repositories.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": strProp("The pattern to search for."),
						"repo":    strProp("Optional repository name to restrict the search to."),
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "symbol",
				"description": "Look up where a symbol (class, function, method) is defined.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": strProp("The symbol name."),
					},
					"required": []string{"name"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file from a repository, or a line range of it.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo":  strProp("Repository name."),
						"path":  strProp("Path within the repository."),
						"start": strProp("Optional first line, 1-based."),
						"end":   strProp("Optional last line."),
					},
					"required": []string{"repo", "path"},
				},
			},
		},
	}
}

// flowEnv is everything a tool call needs, plus the record of what the tools
// have shown the model so far.
type flowEnv struct {
	t         *testing.T
	retriever *retrieve.Retriever
	db        *sql.DB
	repoRoot  string
	rg        string

	// seen is every repo/path the model was actually SHOWN. The diagnostic
	// scores against this rather than against the final prose: whether the
	// model then wrote a good answer is a different question, and a wrong
	// answer built on the right files is not a retrieval failure.
	seen map[flowPart]bool
}

func (e *flowEnv) note(repo, path string) {
	e.seen[flowPart{Repo: repo, Path: path}] = true
}

func (e *flowEnv) call(ctx context.Context, name string, args map[string]string) string {
	switch name {
	case "search":
		return e.search(ctx, args["query"], args["repo"])
	case "grep":
		return e.grep(ctx, args["pattern"], args["repo"])
	case "symbol":
		return e.symbol(ctx, args["name"])
	case "read_file":
		return e.read(args["repo"], args["path"], args["start"], args["end"])
	default:
		return "no such tool: " + name
	}
}

func (e *flowEnv) search(ctx context.Context, query, repo string) string {
	q := retrieve.Query{Text: query, Question: query, K: 10}
	if repo != "" {
		q.Repos = []string{repo}
	}
	hits, err := e.retriever.Search(ctx, q)
	if err != nil {
		return "search failed: " + err.Error()
	}
	if len(hits) == 0 {
		return "no hits"
	}
	var b strings.Builder
	for _, h := range hits {
		e.note(h.Repo, h.Path)
		fmt.Fprintf(&b, "%s/%s:%d-%d", h.Repo, h.Path, h.StartLine, h.EndLine)
		if h.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", h.Symbol)
		}
		b.WriteString("\n")
		b.WriteString(clip(h.RawText, 600))
		b.WriteString("\n---\n")
	}
	return b.String()
}

func (e *flowEnv) grep(ctx context.Context, pattern, repo string) string {
	root := e.repoRoot
	if repo != "" {
		root = filepath.Join(e.repoRoot, repo)
	}
	cmd := exec.CommandContext(ctx, e.rg, "--line-number", "--max-count", "5",
		"--max-columns", "300", "--no-heading", "--color", "never", pattern, root)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return "no matches"
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[:40]
	}
	var b strings.Builder
	for _, ln := range lines {
		rel := strings.TrimPrefix(ln, e.repoRoot+string(filepath.Separator))
		if r, p, ok := splitRepoPath(rel); ok {
			e.note(r, p)
		}
		b.WriteString(rel)
		b.WriteString("\n")
	}
	return b.String()
}

func (e *flowEnv) symbol(ctx context.Context, name string) string {
	rows, err := e.db.QueryContext(ctx, `
		SELECT f.repo, f.path, s.name, s.kind, s.line
		FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.name = ? LIMIT 20`, name)
	if err != nil {
		return "symbol lookup failed: " + err.Error()
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var repo, path, sym, kind string
		var line int
		if err := rows.Scan(&repo, &path, &sym, &kind, &line); err != nil {
			return "symbol lookup failed: " + err.Error()
		}
		e.note(repo, path)
		fmt.Fprintf(&b, "%s/%s:%d %s %s\n", repo, path, line, kind, sym)
	}
	if b.Len() == 0 {
		return "no such symbol"
	}
	return b.String()
}

func (e *flowEnv) read(repo, path, start, end string) string {
	if repo == "" || path == "" {
		return "read_file needs repo and path"
	}
	body, err := os.ReadFile(filepath.Join(e.repoRoot, repo, path))
	if err != nil {
		return "cannot read: " + err.Error()
	}
	e.note(repo, path)
	lines := strings.Split(string(body), "\n")
	from, to := 1, len(lines)
	if n := atoiOr(start, 0); n > 0 {
		from = n
	}
	if n := atoiOr(end, 0); n > 0 && n <= len(lines) {
		to = n
	}
	if to-from > 400 {
		to = from + 400
	}
	if from > len(lines) {
		return "past end of file"
	}
	var b strings.Builder
	for i := from; i <= to && i <= len(lines); i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return clip(b.String(), 8000)
}

// splitRepoPath turns "orders/src/main/..." into its repository and path.
func splitRepoPath(rel string) (string, string, bool) {
	i := strings.IndexByte(rel, filepath.Separator)
	if i <= 0 {
		return "", "", false
	}
	rest := rel[i+1:]
	if j := strings.IndexByte(rest, ':'); j >= 0 {
		rest = rest[:j]
	}
	return rel[:i], rest, rest != ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… truncated"
}

func atoiOr(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

// --- the minimal tool-calling client ---------------------------------------

type toolChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []toolChatCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type toolChatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolChatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []toolChatCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Total      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// flowChat posts one chat-completions request with tools. The deployment names
// come from internal/llm and are not configurable there; they are repeated here
// rather than exported, so the product's rule that a deployment is never an
// environment variable stays intact.
func flowChat(ctx context.Context, deployment string, msgs []toolChatMessage) (*toolChatResponse, error) {
	base := strings.TrimRight(os.Getenv("BACKEND_LLM_BASE_URL"), "/")
	if base == "" {
		return nil, fmt.Errorf("BACKEND_LLM_BASE_URL is empty")
	}
	payload := map[string]any{
		"model":       deployment,
		"messages":    msgs,
		"tools":       flowToolSpecs(),
		"tool_choice": "auto",
		"max_tokens":  2048,
		"temperature": 0,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("BACKEND_LLM_API_KEY"))
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out toolChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode (status %d): %w", resp.StatusCode, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("upstream: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("no choices (status %d)", resp.StatusCode)
	}
	return &out, nil
}

const flowSystemPrompt = `You answer questions about a codebase spread over several repositories.
Use the tools to find the code before answering. A question about what happens in a flow
usually touches more than one repository: follow queue names, routes and identifiers across
repository boundaries. When you have enough, answer in prose and name every file you relied on.`

// flowTrajectory is what one question produced.
type flowTrajectory struct {
	Question string
	Steps    []string
	Reached  map[flowPart]bool
	Answer   string
	Tokens   int
	Err      error
}

func runFlowQuestion(ctx context.Context, t *testing.T, env *flowEnv, deployment string, q flowQuestion) flowTrajectory {
	env.seen = map[flowPart]bool{}
	traj := flowTrajectory{Question: q.Text}
	msgs := []toolChatMessage{
		{Role: "system", Content: flowSystemPrompt},
		{Role: "user", Content: q.Text},
	}
	budget := flowToolBudget()
	for i := 0; i < budget; i++ {
		resp, err := flowChat(ctx, deployment, msgs)
		if err != nil {
			traj.Err = err
			break
		}
		traj.Tokens += resp.Usage.Total
		choice := resp.Choices[0]
		if len(choice.Message.ToolCalls) == 0 {
			traj.Answer = choice.Message.Content
			break
		}
		msgs = append(msgs, toolChatMessage{
			Role:      "assistant",
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		})
		for _, call := range choice.Message.ToolCalls {
			args := map[string]string{}
			var raw map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &raw); err == nil {
				for k, v := range raw {
					args[k] = fmt.Sprint(v)
				}
			}
			result := env.call(ctx, call.Function.Name, args)
			traj.Steps = append(traj.Steps, fmt.Sprintf("%s(%s)", call.Function.Name, call.Function.Arguments))
			msgs = append(msgs, toolChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    clip(result, 6000),
			})
		}
	}
	traj.Reached = map[flowPart]bool{}
	for p := range env.seen {
		traj.Reached[p] = true
	}
	return traj
}

// TestFlowLoopDiagnostic runs the ten flow questions through a bounded tool
// loop, once per deployment, and reports the trajectory rather than a pass
// rate: WHERE a loop lost the thread is the finding, not how many it got.
func TestFlowLoopDiagnostic(t *testing.T) {
	requireEval(t)
	if os.Getenv("BACKEND_LLM_BASE_URL") == "" {
		t.Skip("BACKEND_LLM_BASE_URL is not set")
	}
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatalf("ripgrep: %v", err)
	}
	model := envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small")
	env := &flowEnv{
		t: t,
		retriever: retrieve.New(db, embed.NewClient(embed.Config{
			BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
			APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
			Model:   model,
			Dim:     dim,
		}, nil)),
		db:       db,
		repoRoot: envOr("BACKEND_REPO_ROOT", "/tmp/rongo-flow-repos"),
		rg:       rg,
		seen:     map[flowPart]bool{},
	}

	questions := loadFlowQuestions(t)
	for _, deployment := range flowDeployments {
		t.Run(deployment, func(t *testing.T) {
			var totalParts, totalReached int
			for _, q := range questions {
				traj := runFlowQuestion(ctx, t, env, deployment, q)
				parts := q.parts()
				reached := 0
				var missing []string
				for _, p := range parts {
					if traj.Reached[p] {
						reached++
					} else {
						missing = append(missing, p.String())
					}
				}
				sort.Strings(missing)
				totalParts += len(parts)
				totalReached += reached
				t.Logf("\n=== %s\n  parts %d/%d, %d tool calls, %d tokens",
					q.Text, reached, len(parts), len(traj.Steps), traj.Tokens)
				for i, s := range traj.Steps {
					t.Logf("  %d. %s", i+1, clip(s, 200))
				}
				if len(missing) > 0 {
					t.Logf("  never shown: %s", strings.Join(missing, ", "))
				}
				if traj.Err != nil {
					t.Logf("  ERROR: %v", traj.Err)
				}
				if traj.Answer == "" && traj.Err == nil {
					t.Logf("  budget exhausted without an answer")
				}
			}
			t.Logf("\n%s: %d/%d parts reached across %d questions",
				deployment, totalReached, totalParts, len(questions))
		})
	}
}
