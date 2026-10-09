package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/sourceview"
)

type fakeCommit struct {
	commit       sourceview.Commit
	err          error
	gotRepo, sha string
	// recorded is what RecordedCommit serves: the commit read past the
	// lane's permission. Nil refuses it.
	recorded *sourceview.Commit
}

func (f *fakeCommit) Commit(_ context.Context, repo, sha string) (sourceview.Commit, error) {
	f.gotRepo, f.sha = repo, sha
	return f.commit, f.err
}

func (f *fakeCommit) RecordedCommit(_ context.Context, _, _ string) (sourceview.Commit, error) {
	if f.recorded == nil {
		return sourceview.Commit{}, sourceview.ErrNotFound
	}
	return *f.recorded, nil
}

// TestCommit_aCitedCommitTheLaneHasDroppedStillOpens: the lane slides with
// every push, so a commit a changes answer cited a few weeks ago is no longer
// in it. A turn citing it is the permission, owner and share link alike; an
// uncited commit stays refused.
func TestCommit_aCitedCommitTheLaneHasDroppedStillOpens(t *testing.T) {
	deps, st, _ := testDeps(t)
	old := sourceview.Commit{Repo: "rongo", SHA: "aaa1111", Subject: "Old"}
	deps.Commit = &fakeCommit{err: fmt.Errorf("x: %w", sourceview.ErrNotInLane), recorded: &old}
	srv := NewServer(deps)
	th, _ := answerTurn(t, st, testSubject, "What changed?", "This [1].", citing(
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", SHA: "aaa1111", Kind: ask.SourceCommit, Subject: "Old"},
	))
	owner := func(sha string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/commit?"+url.Values{"repo": {"rongo"}, "sha": {sha}}.Encode(), nil))
		return rec
	}

	if rec := owner("aaa1111"); rec.Code != http.StatusOK {
		t.Errorf("owner, cited: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if rec := owner("bbb2222"); rec.Code != http.StatusNotFound {
		t.Errorf("owner, uncited: status = %d, want 404", rec.Code)
	}
	sh := share(t, srv, th.PublicID)
	if rec := getPublic(srv, "/api/shares/"+sh.Token+"/commit?repo=rongo&sha=aaa1111"); rec.Code != http.StatusOK {
		t.Errorf("share, cited: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

func TestCommit_servesTheCitedCommit_andSaysWhyNot(t *testing.T) {
	c := &fakeCommit{commit: sourceview.Commit{
		Repo: "rongo", Branch: "master", SHA: "7f2a492", CommittedAt: "2026-09-17T10:00:00Z",
		Subject: "Test sources are labelled", Files: []sourceview.FileChange{{Path: "a.go", Added: 3, Indexed: true}},
	}}
	get := func(deps Deps) *httptest.ResponseRecorder {
		q := url.Values{"repo": {"rongo"}, "sha": {"7f2a492"}}
		rec := httptest.NewRecorder()
		NewServer(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/commit?"+q.Encode(), nil))
		return rec
	}

	rec := get(Deps{Auth: devAuth(t), Commit: c})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if c.gotRepo != "rongo" || c.sha != "7f2a492" {
		t.Errorf("asked for %s %s", c.gotRepo, c.sha)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["subject"] != "Test sources are labelled" || got["committed_at"] != "2026-09-17T10:00:00Z" {
		t.Errorf("body = %v", got)
	}
	if _, leaked := got["author"]; leaked {
		t.Error("the author reached the wire")
	}

	for _, tc := range []struct {
		err  error
		want int
	}{
		{sourceview.ErrInvalid, http.StatusBadRequest},
		{sourceview.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("disk"), http.StatusInternalServerError},
	} {
		rec := get(Deps{Auth: devAuth(t), Commit: &fakeCommit{err: fmt.Errorf("wrapped: %w", tc.err)}})
		if rec.Code != tc.want {
			t.Errorf("%v: status = %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
	if rec := get(Deps{Auth: devAuth(t)}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no reader: status = %d, want 503", rec.Code)
	}
}

func TestPublicShareCommit_opensACitedCommitAndNothingElse(t *testing.T) {
	deps, st, _ := testDeps(t)
	deps.Commit = &fakeCommit{commit: sourceview.Commit{Repo: "rongo", SHA: "aaa1111", Subject: "Newest"}}
	srv := NewServer(deps)
	th, _ := answerTurn(t, st, testSubject, "What changed?", "This [1].", citing(
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", SHA: "aaa1111", Kind: ask.SourceCommit, Subject: "Newest", CommittedAt: "2026-09-17T10:00:00Z"},
	))
	sh := share(t, srv, th.PublicID)
	q := func(repo, sha string) string {
		return "/api/shares/" + sh.Token + "/commit?" + url.Values{"repo": {repo}, "sha": {sha}}.Encode()
	}

	rec := getPublic(srv, q("rongo", "aaa1111"))
	if rec.Code != http.StatusOK {
		t.Fatalf("cited commit: status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Robots-Tag") == "" {
		t.Error("a public commit view is indexable")
	}
	if rec := getPublic(srv, q("rongo", "bbb2222")); rec.Code != http.StatusNotFound {
		t.Errorf("an uncited commit: status = %d, want 404", rec.Code)
	}
	if rec := getPublic(srv, "/api/shares/nope/commit?repo=rongo&sha=aaa1111"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: status = %d, want 404", rec.Code)
	}
	// The public payload carries the commit citation's fields.
	page := getPublic(srv, "/api/shares/"+sh.Token)
	var got struct {
		Messages []struct {
			Citations []ask.Citation `json:"citations"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || len(got.Messages[0].Citations) != 1 || got.Messages[0].Citations[0].Kind != ask.SourceCommit ||
		got.Messages[0].Citations[0].Subject != "Newest" {
		t.Errorf("shared citations = %+v", got.Messages)
	}
}
