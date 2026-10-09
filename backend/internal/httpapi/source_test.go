package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
	"github.com/trick77/rongo/internal/store/storetest"
	"github.com/trick77/rongo/internal/threads"
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

func (f *fakeSource) ReadRecorded(_ context.Context, repo, path, sha string) (sourceview.File, error) {
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

// recordedSHA is the commit the record-backed tests cite: sourceview refuses
// anything that is not hex.
const recordedSHA = "abc1234"

// recordServer is shareServer over the real source viewer: a repository, a
// checkout holding a.go and secret.go at recordedSHA, and a files row for
// a.go only — secret.go is a path the index never served. The thread answered
// testSubject from a.go and otherSubject from secret.go.
func recordServer(t *testing.T) (*Server, *threads.Store, *sql.DB, threads.Thread) {
	t.Helper()
	db := storetest.Open(t, 4)
	ctx := context.Background()
	svc := auth.NewService(db, "dev", "")
	for _, subject := range []string{testSubject, otherSubject} {
		if _, err := svc.UpsertUser(ctx, subject, subject+"@example.invalid", true); err != nil {
			t.Fatalf("seed user %q: %v", subject, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, enabled, last_sha) VALUES ('rongo', 'x', 'master', 1, ?)`, recordedSHA); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES ('rongo', 'a.go', ?, '')`, recordedSHA); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	git := checkout{recordedSHA + ":a.go": "package a\n", recordedSHA + ":secret.go": "package secret\n"}
	st := threads.NewStore(db)
	srv := NewServer(Deps{Auth: svc, Threads: st, Source: sourceview.New(db, git, 1<<20)})

	answer := func(subject, path string) threads.Thread {
		th, err := st.Create(ctx, subject, "How does it start?")
		if err != nil {
			t.Fatalf("create thread: %v", err)
		}
		m, err := st.AddQuestion(ctx, th.ID, "dev", "en", "How does it start?", 0)
		if err != nil {
			t.Fatalf("add question: %v", err)
		}
		if err := st.Finish(ctx, m.ID, "Here [1].", []ask.Citation{
			{Marker: 1, Repo: "rongo", Branch: "master", Path: path, StartLine: 1, EndLine: 1, SHA: recordedSHA},
		}); err != nil {
			t.Fatalf("finish: %v", err)
		}
		return th
	}
	answer(otherSubject, "secret.go")
	return srv, st, db, answer(testSubject, "a.go")
}

func dropFromIndex(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM files WHERE repo = 'rongo' AND path = ?`, path); err != nil {
		t.Fatalf("drop %s: %v", path, err)
	}
}

func wantContent(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var f sourceview.File
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if f.Content != want || f.SHA != recordedSHA {
		t.Errorf("served %q at %q, want %q at the cited commit", f.Content, f.SHA, want)
	}
}

// A file renamed or deleted since the answer is still in the commit the
// answer was read at, and the link has authorised exactly that triple.
func TestPublicShareSourceOpensFileGoneFromIndex(t *testing.T) {
	srv, _, db, th := recordServer(t)
	sh := share(t, srv, th.PublicID)
	dropFromIndex(t, db, "a.go")

	rec := getPublic(srv, "/api/shares/"+sh.Token+"/source?"+
		url.Values{"repo": {"rongo"}, "path": {"a.go"}, "sha": {recordedSHA}}.Encode())

	wantContent(t, rec, "package a\n")
}

func TestSourceOwnerCitationOpensFileGoneFromIndex(t *testing.T) {
	srv, _, db, _ := recordServer(t)
	dropFromIndex(t, db, "a.go")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/source?"+
		url.Values{"repo": {"rongo"}, "path": {"a.go"}, "sha": {recordedSHA}}.Encode(), nil))

	wantContent(t, rec, "package a\n")
}

// /api/source takes any triple. Without a files row only the reader's own
// citation opens a path, or a signed-in reader could open what the index
// never served at a commit before it ran.
func TestSourceUncitedPathWithoutRowIs404(t *testing.T) {
	srv, _, _, _ := recordServer(t)

	for name, sha := range map[string]string{
		// secret.go is cited, but in otherSubject's thread.
		"another reader's citation": recordedSHA,
		"no commit at all":          "",
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/source?"+
			url.Values{"repo": {"rongo"}, "path": {"secret.go"}, "sha": {sha}}.Encode(), nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d (%s), want 404", name, rec.Code, rec.Body.String())
		}
	}
}

// The record is a join over every citation, so it is asked only when the index
// no longer lists the file: an indexed file opens without it, even when the
// record could not answer.
func TestSource_anIndexedFileNeverAsksTheRecord(t *testing.T) {
	srv, _, db, _ := recordServer(t)
	if _, err := db.Exec(`ALTER TABLE citations RENAME TO citations_gone`); err != nil {
		t.Fatalf("break citations: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/source?"+
		url.Values{"repo": {"rongo"}, "path": {"a.go"}, "sha": {recordedSHA}}.Encode(), nil))

	wantContent(t, rec, "package a\n")
}

// A record that cannot say whether the reader cited a file is a 500, never a
// guess either way.
func TestSource_aBrokenRecordIsAnError(t *testing.T) {
	srv, _, db, _ := recordServer(t)
	dropFromIndex(t, db, "a.go")
	if _, err := db.Exec(`ALTER TABLE citations RENAME TO citations_gone`); err != nil {
		t.Fatalf("break citations: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/source?"+
		url.Values{"repo": {"rongo"}, "path": {"a.go"}, "sha": {recordedSHA}}.Encode(), nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", rec.Code, rec.Body.String())
	}
}
