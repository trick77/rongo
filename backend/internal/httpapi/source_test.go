package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trick77/rongo/internal/sourceview"
)

// fakeSource stands in for the checkout. It records what it was asked for, so
// a test can see that the handler passed the citation through untouched.
type fakeSource struct {
	file                  sourceview.File
	err                   error
	gotRepo, gotPath, sha string
}

func (f *fakeSource) Read(_ context.Context, repo, path, sha string) (sourceview.File, error) {
	f.gotRepo, f.gotPath, f.sha = repo, path, sha
	return f.file, f.err
}

func getSource(t *testing.T, deps Deps, repo, path, sha string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"repo": {repo}, "path": {path}, "sha": {sha}}
	req := httptest.NewRequest(http.MethodGet, "/api/source?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	NewServer(deps).ServeHTTP(rec, req)
	return rec
}

func TestSource_servesTheFileTheCitationPointsAt(t *testing.T) {
	// Given
	src := &fakeSource{file: sourceview.File{
		Repo: "peeq", Branch: "master", Path: "internal/a.go", SHA: "0123abc", Content: "package a\n",
	}}
	deps := Deps{Auth: devAuth(t), Source: src}

	// When
	rec := getSource(t, deps, "peeq", "internal/a.go", "0123abc")

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if src.gotRepo != "peeq" || src.gotPath != "internal/a.go" || src.sha != "0123abc" {
		t.Fatalf("asked for %s %s %s, want the citation as sent", src.gotRepo, src.gotPath, src.sha)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	for key, want := range map[string]any{
		"repo": "peeq", "branch": "master", "path": "internal/a.go", "sha": "0123abc", "content": "package a\n",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

func TestSource_eachRefusalHasItsOwnStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{sourceview.ErrInvalid, http.StatusBadRequest},
		{sourceview.ErrNotFound, http.StatusNotFound},
		{sourceview.ErrBinary, http.StatusUnsupportedMediaType},
		{sourceview.ErrTooLarge, http.StatusRequestEntityTooLarge},
		{fmt.Errorf("disk on fire"), http.StatusInternalServerError},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			deps := Deps{Auth: devAuth(t), Source: &fakeSource{err: fmt.Errorf("wrapped: %w", tc.err)}}
			rec := getSource(t, deps, "peeq", "a.go", "")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestSource_saysUnavailableRatherThanNotFoundWhenUnwired(t *testing.T) {
	// Given: a deployment without a checkout to read from.
	deps := Deps{Auth: devAuth(t)}

	// When
	rec := getSource(t, deps, "peeq", "a.go", "")

	// Then: "cannot tell you" and "no such file" are different facts.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
