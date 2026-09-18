package ask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/stages"
)

// fakeReleaser is a checkout with one linear branch per repository, oldest
// first, tags on it, and a few commits off it.
type fakeReleaser struct {
	branch    map[string][]string // repo -> first-parent shas, oldest first
	tags      map[string]string   // repo/tag -> sha
	offBranch map[string]bool     // repo/sha -> lies on a side branch
	head      map[string]RepoHead
	depth     int
}

func (f *fakeReleaser) ResolveTag(_ context.Context, repo, tag string) (string, error) {
	if sha, ok := f.tags[repo+"/"+tag]; ok {
		return sha, nil
	}
	if sha, ok := f.tags[repo+"/v"+tag]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("%s: %q: %w", repo, tag, ErrVersionUnknown)
}

func (f *fakeReleaser) index(repo, sha string) int {
	for i, s := range f.branch[repo] {
		if s == sha {
			return i
		}
	}
	return -1
}

func (f *fakeReleaser) IsAncestor(_ context.Context, repo, ancestor, descendant string) (bool, error) {
	if f.offBranch[repo+"/"+ancestor] || f.offBranch[repo+"/"+descendant] {
		return ancestor == descendant, nil
	}
	a, d := f.index(repo, ancestor), f.index(repo, descendant)
	return a >= 0 && d >= 0 && a <= d, nil
}

func (f *fakeReleaser) Range(_ context.Context, repo, from, to string, limit int) ([]string, error) {
	a, d := f.index(repo, from), f.index(repo, to)
	var out []string
	for i := d; i > a && len(out) < limit; i-- {
		out = append(out, f.branch[repo][i])
	}
	return out, nil
}

func (f *fakeReleaser) Head(_ context.Context, repo string) (RepoHead, error) {
	if h, ok := f.head[repo]; ok {
		return h, nil
	}
	b := f.branch[repo]
	return RepoHead{SHA: b[len(b)-1], Remote: b[len(b)-1], Branch: "main"}, nil
}

func (f *fakeReleaser) Depth() int {
	if f.depth == 0 {
		return 500
	}
	return f.depth
}

// releaseHistory holds rows for every sha it is told about, so BySHAs finds
// them; a sha left out is one the lane never recorded.
type releaseHistory struct {
	fakeHistory
	rows map[string]history.Commit
}

func (h *releaseHistory) BySHAs(_ context.Context, repo string, shas []string) ([]history.Commit, error) {
	var out []history.Commit
	for _, s := range shas {
		if c, ok := h.rows[repo+"/"+s]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// releaseCorpus is the shop: an infrastructure repository with prod and
// test overlays, a backend and a UI it deploys, and a worker nobody declared.
//
//	shop-backend: c1 (1.0.0) - c2 - c3 (1.1.0) - c4 (head)
//	shop-ui:      u1 (5.0.0) - u2 (5.0.0 on test too)
//	prod: backend 1.0.0, ui 5.0.0, worker 0.9
//	test: backend 1.1.0, ui 5.0.0, worker 0.9
func releaseCorpus(t *testing.T, db *sql.DB) (*fakeReleaser, *releaseHistory, stages.Set, projects.Map) {
	t.Helper()
	for _, r := range [][3]string{
		{"shop-infra", "shop", ""},
		{"shop-backend", "shop", "registry.example.invalid/acme/shop-backend"},
		{"shop-ui", "shop", "registry.example.invalid/acme/shop-ui"},
	} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, project, image) VALUES (?, 'file:///x', 'main', ?, ?)`,
			r[0], r[1], r[2]); err != nil {
			t.Fatalf("seed %s: %v", r[0], err)
		}
	}
	seedChunkIn(t, db, "shop-infra", "prod/kustomization.yaml", 0, 1, 10, "", "images: ...")
	seedTokenIn(t, db, "shop-infra", "prod/kustomization.yaml", "image", "registry.example.invalid/acme/shop-backend:1.0.0", 3)
	seedTokenIn(t, db, "shop-infra", "prod/kustomization.yaml", "image", "registry.example.invalid/acme/shop-ui:5.0.0", 5)
	seedTokenIn(t, db, "shop-infra", "prod/kustomization.yaml", "image", "registry.example.invalid/acme/worker:0.9", 7)
	seedChunkIn(t, db, "shop-infra", "test/kustomization.yaml", 0, 1, 10, "", "images: ...")
	seedTokenIn(t, db, "shop-infra", "test/kustomization.yaml", "image", "registry.example.invalid/acme/shop-backend:1.1.0", 3)
	seedTokenIn(t, db, "shop-infra", "test/kustomization.yaml", "image", "registry.example.invalid/acme/shop-ui:5.0.0", 5)
	seedTokenIn(t, db, "shop-infra", "test/kustomization.yaml", "image", "registry.example.invalid/acme/worker:0.9", 7)
	// The base is never read: a placeholder there must not become a version.
	seedChunkIn(t, db, "shop-infra", "base/deployment.yaml", 0, 1, 10, "", "image: ...")
	seedTokenIn(t, db, "shop-infra", "base/deployment.yaml", "image", "registry.example.invalid/acme/shop-backend:0.0.1", 9)

	pm, err := projects.Load(context.Background(), db)
	if err != nil {
		t.Fatalf("projects.Load: %v", err)
	}
	declared := stages.Set{
		{Repo: "shop-infra", Name: "prod", Prefix: "prod/", Aliases: []string{"production"}},
		{Repo: "shop-infra", Name: "test", Prefix: "test/", Aliases: []string{"testing"}},
	}
	rel := &fakeReleaser{
		branch: map[string][]string{
			"shop-backend": {"c1", "c2", "c3", "c4"},
			"shop-ui":      {"u1", "u2"},
		},
		tags: map[string]string{
			"shop-backend/v1.0.0": "c1", "shop-backend/v1.1.0": "c3",
			"shop-ui/5.0.0": "u2",
		},
		offBranch: map[string]bool{},
		head:      map[string]RepoHead{},
	}
	h := &releaseHistory{rows: map[string]history.Commit{}}
	for i, s := range []string{"c2", "c3"} {
		h.rows["shop-backend/"+s] = history.Commit{ID: int64(10 + i), Repo: "shop-backend", Branch: "main", SHA: s,
			CommittedAt: fixedNow.Add(-time.Duration(48-i) * time.Hour), Subject: "backend change " + s, Paths: []string{"a.go"}}
	}
	return rel, h, declared, pm
}

func releaseUpstream(t *testing.T, understanding string, answerTokens ...string) (*Pipeline, *releaseHistory, *fakeReleaser, *[]string) {
	t.Helper()
	var prompts []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": understanding}}},
			})
			return
		}
		calls++
		for _, m := range req.Messages {
			prompts = append(prompts, m.Content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, answerTokens, "")
	}))
	t.Cleanup(srv.Close)
	db := gatherDB(t)
	rel, h, declared, pm := releaseCorpus(t, db)
	router := &fakeRouter{projects: pm, stages: declared}
	p := NewPipeline(fakeLLM(t, srv), &fakeSearch{indexed: []string{"shop-infra", "shop-backend", "shop-ui"}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), router).
		WithHistory(h, func() time.Time { return fixedNow }).WithReleases(rel)
	return p, h, rel, &prompts
}

const releaseUnderstanding = `{"intent":"release","between":["prod","test"],"terms":[],"code_terms":[],"repos":["shop"]}`

func TestRun_releaseAnswersFromTheCommitsBetweenTwoDeployedVersions(t *testing.T) {
	p, _, _, prompts := releaseUpstream(t, releaseUnderstanding, "The backend moved [1][2].")
	var steps []string

	got, clar, err := p.Run(context.Background(), "release notes for shop between production and testing", AudienceBA, LanguageEN, Thread{},
		Events{OnStatus: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if clar != nil {
		t.Fatal("a release turn asked instead of answering")
	}
	if strings.Join(steps, ",") != "understanding,searching,answering,writing" {
		t.Errorf("steps = %v", steps)
	}
	// Two commits between 1.0.0 and 1.1.0 on the backend, newest first.
	if len(got.Sources) != 2 || got.Sources[0].SHA != "c3" || got.Sources[1].SHA != "c2" {
		t.Fatalf("sources = %+v", got.Sources)
	}
	if got.Sources[0].Reason != "release:registry.example.invalid/acme/shop-backend 1.0.0..1.1.0" {
		t.Errorf("reason = %q", got.Sources[0].Reason)
	}
	// The record carries the pair and one line per image.
	if got.Scope.Intent != IntentRelease || strings.Join(got.Scope.Between, ",") != "prod,test" {
		t.Errorf("scope = %+v", got.Scope)
	}
	byImage := map[string]ReleaseLine{}
	for _, l := range got.Scope.Release {
		byImage[l.Image] = l
	}
	if l := byImage["registry.example.invalid/acme/shop-backend"]; l.Repo != "shop-backend" || l.Commits != 2 ||
		l.Versions["prod"] != "1.0.0" || l.Versions["test"] != "1.1.0" || l.Ahead != "test" || l.Note != "" {
		t.Errorf("backend line = %+v", l)
	}
	if l := byImage["registry.example.invalid/acme/shop-ui"]; l.Repo != "shop-ui" || l.Note != NoteUnchanged {
		t.Errorf("ui line = %+v", l)
	}
	if l := byImage["registry.example.invalid/acme/worker"]; l.Repo != "" || l.Note != NoteUndeclared {
		t.Errorf("worker line = %+v", l)
	}
	// The prompt: the commits, the versions, the rule, and never the base.
	all := strings.Join(*prompts, "\n")
	for _, want := range []string{
		"[1] shop-backend commit c3",
		"[2] shop-backend commit c2",
		"shop-backend: prod 1.0.0, test 1.1.0, test is ahead by 2 commits",
		"shop-ui: prod 5.0.0, test 5.0.0, unchanged",
		"registry.example.invalid/acme/worker: prod 0.9, test 0.9, no repository declares this image",
		"between two deployed versions",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(all, "0.0.1") {
		t.Error("the base overlay's version reached the prompt")
	}
}

func TestRun_releaseWithNothingBetweenIsTemplated(t *testing.T) {
	p, _, rel, prompts := releaseUpstream(t, releaseUnderstanding, "never")
	// prod catches up with test.
	rel.tags["shop-backend/v1.0.0"] = "c3"

	got, _, err := p.Run(context.Background(), "release notes between production and testing", AudienceBA, LanguageDE, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*prompts) != 0 {
		t.Errorf("a model was called for an empty release: %v", *prompts)
	}
	if !strings.Contains(got.Text, "Keine Commits") || !strings.Contains(got.Text, "prod") || !strings.Contains(got.Text, "test") {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Sources) != 0 {
		t.Errorf("sources = %+v", got.Sources)
	}
}

func TestReleaseLines_theRefusalMatrix(t *testing.T) {
	cases := []struct {
		name   string
		tweak  func(rel *fakeReleaser, h *releaseHistory)
		note   string
		ahead  string
		commit int
	}{
		// The pair is a set: with prod's tag the newer one, prod is ahead and
		// the same two commits are the range. Never a "rollback": nothing
		// declares which stage is meant to lead.
		{"prod ahead of test", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.tags["shop-backend/v1.0.0"], rel.tags["shop-backend/v1.1.0"] = "c3", "c1"
		}, "", "prod", 2},
		{"never indexed", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.head["shop-backend"] = RepoHead{SHA: "", Remote: "c4", Branch: "main"}
		}, NoteNoIndex, "", 0},
		{"diverged", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.tags["shop-backend/v1.1.0"] = "h1"
			rel.branch["shop-backend"] = append(rel.branch["shop-backend"], "h1")
			rel.offBranch["shop-backend/h1"] = true
		}, NoteOffBranch, "", 0},
		{"tag unknown", func(rel *fakeReleaser, _ *releaseHistory) {
			delete(rel.tags, "shop-backend/v1.1.0")
		}, NoteTagUnknown, "", 0},
		{"not indexed yet", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.head["shop-backend"] = RepoHead{SHA: "c2", Remote: "c4", Branch: "main"}
		}, NoteNotIndexed, "", 0},
		// Refused, but the direction and the true size still stand.
		{"a commit the lane never recorded", func(rel *fakeReleaser, h *releaseHistory) {
			delete(h.rows, "shop-backend/c2")
		}, NoteUnrecorded, "test", 2},
		{"one tag spelled two ways", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.tags["shop-backend/1.1.0"] = "c1"
			rel.tags["shop-backend/v1.1.0"] = "c1"
		}, NoteUnchanged, "", 0},
		{"range past the depth", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.depth = 1
		}, NoteBeyondDepth, "test", 2},
		{"snapshot has no history", func(rel *fakeReleaser, _ *releaseHistory) {
			rel.head["shop-backend"] = RepoHead{SHA: "c4", Remote: "c4", Branch: "snapshot", Snapshot: true}
		}, NoteSnapshot, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := gatherDB(t)
			rel, h, declared, pm := releaseCorpus(t, db)
			tc.tweak(rel, h)
			g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000})
			lines, _, err := releaseLines(context.Background(), g, rel, h, pm, "shop-infra", declared, []string{"prod", "test"})
			if err != nil {
				t.Fatalf("releaseLines: %v", err)
			}
			var got ReleaseLine
			for _, l := range lines {
				if l.Repo == "shop-backend" {
					got = l
				}
			}
			if got.Note != tc.note || got.Ahead != tc.ahead || got.Commits != tc.commit {
				t.Errorf("line = %+v, want note %q ahead %q commits %d", got, tc.note, tc.ahead, tc.commit)
			}
		})
	}
}

func TestReleaseLines_digestMissingAndAmbiguousVersions(t *testing.T) {
	db := gatherDB(t)
	rel, h, declared, pm := releaseCorpus(t, db)
	// The UI is pinned by digest on both stages, and the backend is named
	// twice on test with two versions.
	if _, err := db.Exec(`DELETE FROM integration_tokens WHERE value LIKE '%shop-ui%'`); err != nil {
		t.Fatal(err)
	}
	seedTokenIn(t, db, "shop-infra", "prod/kustomization.yaml", "image", "registry.example.invalid/acme/shop-ui@sha256:abc", 5)
	seedTokenIn(t, db, "shop-infra", "test/kustomization.yaml", "image", "registry.example.invalid/acme/shop-ui@sha256:def", 5)
	seedTokenIn(t, db, "shop-infra", "test/kustomization.yaml", "image", "registry.example.invalid/acme/shop-backend:1.2.0", 4)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000})

	lines, _, err := releaseLines(context.Background(), g, rel, h, pm, "shop-infra", declared, []string{"prod", "test"})
	if err != nil {
		t.Fatalf("releaseLines: %v", err)
	}

	by := map[string]ReleaseLine{}
	for _, l := range lines {
		by[l.Repo] = l
	}
	if l := by["shop-ui"]; l.Note != NoteDigest {
		t.Errorf("ui = %+v, want the digest note", l)
	}
	if l := by["shop-backend"]; l.Note != NoteAmbiguous {
		t.Errorf("backend = %+v, want the ambiguous note", l)
	}
}

func TestReleaseLines_anInlineImageYieldsToTheKustomizeEntry(t *testing.T) {
	db := gatherDB(t)
	rel, h, declared, pm := releaseCorpus(t, db)
	// A manifest under test/ still carries the old inline tag; the overlay's
	// images: entry is what runs.
	seedChunkIn(t, db, "shop-infra", "test/deployment.yaml", 0, 1, 10, "", "image: ...")
	seedTokenIn(t, db, "shop-infra", "test/deployment.yaml", "image", "registry.example.invalid/acme/shop-backend:0.5.0", 2)
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000})

	lines, _, err := releaseLines(context.Background(), g, rel, h, pm, "shop-infra", declared, []string{"prod", "test"})
	if err != nil {
		t.Fatalf("releaseLines: %v", err)
	}
	for _, l := range lines {
		if l.Repo == "shop-backend" && (l.Versions["test"] != "1.1.0" || l.Note != "") {
			t.Errorf("backend = %+v, want test 1.1.0 from the kustomize entry", l)
		}
	}
}

func TestRun_releaseRefusesWithoutTwoStagesOrAnInfrastructureRepository(t *testing.T) {
	cases := map[string]struct {
		understanding string
		question      string
		want          string
	}{
		"one stage": {
			`{"intent":"release","between":["prod"],"terms":[],"code_terms":[],"repos":[]}`,
			"release notes for production", "two"},
		"unknown stage": {
			`{"intent":"release","between":["prod","uat"],"terms":[],"code_terms":[],"repos":[]}`,
			"release notes between prod and uat", "two"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, _, prompts := releaseUpstream(t, tc.understanding, "never")
			got, _, err := p.Run(context.Background(), tc.question, AudienceBA, LanguageEN, Thread{}, Events{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(*prompts) != 0 {
				t.Errorf("a model was called: %v", *prompts)
			}
			if !strings.Contains(got.Text, tc.want) {
				t.Errorf("text = %q, want it to mention %q", got.Text, tc.want)
			}
		})
	}
}

func TestRun_releaseTakesTheStagesFromTheReadersOwnWords(t *testing.T) {
	// The model's field is a guess; "production" and "testing" in the
	// question are aliases and win.
	p, _, _, _ := releaseUpstream(t,
		`{"intent":"release","between":[],"terms":[],"code_terms":[],"repos":[]}`, "ok [1]")
	got, _, err := p.Run(context.Background(), "what is between production and testing?", AudienceDev, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(got.Scope.Between, ",") != "prod,test" {
		t.Errorf("between = %v", got.Scope.Between)
	}
}

func TestReleasePair_composesTheReadersWordAndTheModelsMapping(t *testing.T) {
	declared := stages.Set{
		{Repo: "infra", Name: "prod", Prefix: "prod/", Aliases: []string{"production"}},
		{Repo: "infra", Name: "intg", Prefix: "intg/"},
	}
	// "integration" is a refused alias, so only the model can map it; the
	// reader's "production" still counts.
	if got := releasePair("production vs. the integration environment", []string{"intg"}, declared); strings.Join(got, ",") != "prod,intg" {
		t.Errorf("composed = %v", got)
	}
	// The model repeating the reader's word is not a second stage.
	if got := releasePair("what is on production", []string{"production", "prod"}, declared); got != nil {
		t.Errorf("one stage twice = %v", got)
	}
	// A comma-separated string decodes like a list.
	var u Understanding
	if err := json.Unmarshal([]byte(`{"between":"prod, intg"}`), &u); err != nil || strings.Join(u.Between, ",") != "prod,intg" {
		t.Errorf("string between = %v, %v", u.Between, err)
	}
	if err := json.Unmarshal([]byte(`{"between":7}`), &u); err != nil || len(u.Between) != 0 {
		t.Errorf("number between = %v, %v", u.Between, err)
	}
}
