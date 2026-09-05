package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// captureLog points the default logger at a buffer for the duration of one
// test and hands back the records it wrote. Route logs through slog.Default,
// the way every other package in the backend does, so this is the only way to
// see what an operator would see.
func captureLog(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("log line is not JSON: %v (%q)", err, line)
			}
			out = append(out, rec)
		}
		return out
	}
}

// only returns the one record with the given message, failing when there is
// not exactly one: the route line is one per turn, and two of them would mean
// a rung logged its own decision behind Route's back.
func only(t *testing.T, records []map[string]any, msg string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, rec := range records {
		if rec["msg"] == msg {
			found = append(found, rec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %q line, got %d: %v", msg, len(found), records)
	}
	return found[0]
}

func hasMsg(records []map[string]any, msg string) bool {
	for _, rec := range records {
		if rec["msg"] == msg {
			return true
		}
	}
	return false
}

// TestRouteLogsTheRepositoryRung is the whole point of the log line: a card
// nobody expected, explained after the fact. The question names no
// repository, the hits span two, and the reader needs to be able to read that
// out of the log without re-running the turn.
func TestRouteLogsTheRepositoryRung(t *testing.T) {
	read := captureLog(t)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			t.Fatalf("the repository rung is deterministic; the judge must not be asked. prompt: %q", prompt)
		}
		return `{"title":"Token accounting","summary":"Counts what a turn cost."}`
	}), testDBWithDeps(t, nil))

	ctx := llm.WithThreadID(context.Background(), 42)
	got, err := r.Route(ctx, "how is usage counted?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/usage/usage.go", Score: 0.60},
		{Repo: "loom", Path: "backend/internal/usage/usage.go", Score: 0.40},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("candidates in two repositories with none named are a card")
	}

	records := read()
	rec := only(t, records, "route")
	want := map[string]any{
		"thread":          "42",
		"decision":        "ask",
		"rung":            rungRepository,
		"repos":           float64(2),
		"named":           float64(0),
		"spans":           true,
		"all_repos":       false,
		"dominates":       true,
		"related_checked": true,
		"related":         false,
		"judge_ran":       false,
		"role_gate":       false,
		"role_can_choose": true,
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
	// The margin dominated and the turn asked anyway. Without both numbers in
	// the line that reads as a contradiction rather than as the rung doing its
	// job.
	if rec["lead"] != 0.60 || rec["runner_up"] != 0.40 {
		t.Errorf("scores = %v/%v, want 0.6/0.4", rec["lead"], rec["runner_up"])
	}
	if hasMsg(records, "route judge") || hasMsg(records, "route role gate") {
		t.Error("no gate ran, so no gate line may be written")
	}
}

// TestRouteLogsANamingFailureOnARepositoryCard pins the one thing the
// repository branch can get wrong and the reader will see: a naming call that
// failed leaves the bare repository name on the card. The role gate does not
// apply to a repository card, so allNamed is not read there for the decision -
// but it is the explanation, and the line must not report it as fine.
func TestRouteLogsANamingFailureOnARepositoryCard(t *testing.T) {
	read := captureLog(t)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, "repository loom") {
			return "sorry, I could not name this one"
		}
		return `{"title":"Token accounting","summary":"Counts what a turn cost."}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is usage counted?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/usage/usage.go", Score: 0.60},
		{Repo: "loom", Path: "backend/internal/usage/usage.go", Score: 0.40},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("candidates in two repositories with none named are a card")
	}

	rec := only(t, read(), "route")
	if rec["all_named"] != false {
		t.Errorf("all_named = %v, want false — one repository was never named", rec["all_named"])
	}
}

// TestRouteLogsTheFastPathOnce pins the cheap turn: one line, the margin as
// the reason, and nothing that suggests a rung ran which did not.
func TestRouteLogsTheFastPathOnce(t *testing.T) {
	read := captureLog(t)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("no model call may happen when the margin is clear; got %q", prompt)
		return ""
	}), testDBWithDeps(t, nil))

	if _, err := r.Route(context.Background(), "wie prueft peeq den Plattenplatz?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.9},
		{Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.1},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}

	rec := only(t, read(), "route")
	if rec["decision"] != "answer" || rec["rung"] != rungMargin {
		t.Errorf("decision/rung = %v/%v, want answer/%s", rec["decision"], rec["rung"], rungMargin)
	}
	if rec["related_checked"] != false {
		t.Error("the fast path makes no manifest query; the line must not claim one")
	}
	if rec["thread"] != "" {
		t.Errorf("thread = %v, want empty outside a turn", rec["thread"])
	}
}

// TestJudgeLogsAnUnreadableReply pins the case the line exists for: a judge
// whose answer did not parse asks, and to everything downstream that is
// indistinguishable from a judge that meant to ask.
func TestJudgeLogsAnUnreadableReply(t *testing.T) {
	read := captureLog(t)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return "I think they are alternatives."
		}
		return `{"title":"peeq HTTP layer","summary":"Takes requests and answers them."}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("an unreadable judgement falls to the safe side, which is asking")
	}

	records := read()
	judge := only(t, records, "route judge")
	if judge["decoded"] != false {
		t.Error("decoded must be false when the reply did not parse")
	}
	if judge["ask"] != true {
		t.Error("the fallback is ask")
	}
	if s, _ := judge["reply"].(string); !strings.Contains(s, "alternatives") {
		t.Errorf("reply = %q, want the model's own text", s)
	}
	rec := only(t, records, "route")
	if rec["rung"] != rungJudge || rec["judge_ran"] != true || rec["judged"] != true {
		t.Errorf("route line = %v, want the judge as the rung that settled it", rec)
	}
}

// TestRoleGateLogsItsRefusal covers the Analyst's rung: the judge found the
// code ambiguous, the reader cannot resolve it, and the turn answers. Which
// of the two rungs turned the card off is the whole question the line
// answers.
func TestRoleGateLogsItsRefusal(t *testing.T) {
	read := captureLog(t)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			return `{"decision":"cannot"}`
		default:
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "wie wird angemeldet?", AudienceBA, LanguageDE, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "peeq", Path: "backend/internal/login/session.go", Score: 0.49},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Fatal("a card the reader cannot answer is not asked")
	}

	records := read()
	gate := only(t, records, "route role gate")
	if gate["decoded"] != true || gate["choose"] != false {
		t.Errorf("gate line = %v, want a decoded refusal", gate)
	}
	rec := only(t, records, "route")
	want := map[string]any{
		"decision":        "answer",
		"rung":            rungRole,
		"judged":          true,
		"judge_ran":       true,
		"role_gate":       true,
		"all_named":       true,
		"role_can_choose": false,
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
}
