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
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
	"github.com/trick77/rongo/internal/threads"
)

type fakeCommit struct {
	commit       sourceview.Commit
	err          error
	gotRepo, sha string
}

func (f *fakeCommit) Commit(_ context.Context, repo, sha string) (sourceview.Commit, error) {
	f.gotRepo, f.sha = repo, sha
	return f.commit, f.err
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
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	if _, err := svc.UpsertUser(testSubject, testSubject+"@example.invalid", true); err != nil {
		t.Fatal(err)
	}
	st := threads.NewStore(db)
	c := &fakeCommit{commit: sourceview.Commit{Repo: "rongo", SHA: "aaa1111", Subject: "Newest"}}
	srv := NewServer(Deps{Auth: svc, Threads: st, Commit: c})
	ctx := context.Background()
	th, _ := st.Create(ctx, testSubject, "What changed?")
	m, _ := st.AddQuestion(ctx, th.ID, "ba", "en", "What changed?", 0)
	if err := st.Finish(ctx, m.ID, "This [1].", []ask.Citation{
		{Marker: 1, Repo: "rongo", Branch: "master", SHA: "aaa1111", Kind: ask.SourceCommit, Subject: "Newest", CommittedAt: "2026-09-17T10:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
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
