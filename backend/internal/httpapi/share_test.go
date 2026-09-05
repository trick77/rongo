package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
	"github.com/trick77/rongo/internal/threads"
)

// shareServer wires a dev-auth server over a real thread store and a fake
// checkout, so a test can share through HTTP and then read the link back the
// way an anonymous browser would.
func shareServer(t *testing.T) (*Server, *threads.Store, *fakeSource) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	for _, subject := range []string{testSubject, otherSubject} {
		if _, err := svc.UpsertUser(subject, subject+"@example.invalid", true); err != nil {
			t.Fatalf("seed user %q: %v", subject, err)
		}
	}
	src := &fakeSource{file: sourceview.File{
		Repo: "rongo", Branch: "master", Path: "a.go", SHA: "abc", Content: "package a\n",
	}}
	st := threads.NewStore(db)
	return NewServer(Deps{Auth: svc, Threads: st, Source: src}), st, src
}

// sharedTurn is a thread with one finished, cited turn — the smallest thing
// worth sharing.
func sharedTurn(t *testing.T, st *threads.Store, subject string) int64 {
	t.Helper()
	ctx := context.Background()
	th, err := st.Create(ctx, subject, "How does routing decide?")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	m, err := st.AddQuestion(ctx, th.ID, "ba", "en", "How does routing decide?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Finish(ctx, m.ID, "It is a ladder [1].", []ask.Citation{
		{Marker: 1, Repo: "rongo", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 4, SHA: "abc"},
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return th.ID
}

// share makes a link through the HTTP layer, the way the dialog does.
func share(t *testing.T, srv *Server, threadID int64) threads.Share {
	t.Helper()
	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share", threadID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("share status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var sh threads.Share
	if err := json.Unmarshal(rec.Body.Bytes(), &sh); err != nil {
		t.Fatalf("decode share: %v", err)
	}
	return sh
}

// getPublic reads a link the way a browser with no session does: a plain GET,
// no cookie, no bearer token.
func getPublic(srv *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPublicShare_readsWithoutASession(t *testing.T) {
	// Given a shared thread
	srv, st, _ := shareServer(t)
	sh := share(t, srv, sharedTurn(t, st, testSubject))

	// When an anonymous reader opens the link
	rec := getPublic(srv, "/api/shares/"+sh.Token)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Title    string `json:"title"`
		Messages []struct {
			Question  string          `json:"question"`
			Answer    string          `json:"answer"`
			Citations []ask.Citation  `json:"citations"`
			Followups []string        `json:"followups"`
			Usage     json.RawMessage `json:"usage"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Answer != "It is a ladder [1]." {
		t.Fatalf("messages = %+v, want the one answered turn", got.Messages)
	}
	if len(got.Messages[0].Citations) != 1 {
		t.Error("a shared answer arrived without the sources it was written from")
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q, want noindex", rec.Header().Get("X-Robots-Tag"))
	}
}

func TestPublicShare_carriesNoUsageCostOrFollowups(t *testing.T) {
	// Given a shared turn that paid for calls and offered follow-ups
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th := sharedTurn(t, st, testSubject)
	msgs, err := st.Messages(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if err := st.SaveFollowups(ctx, msgs[0].ID, []string{"And then?"}); err != nil {
		t.Fatalf("save followups: %v", err)
	}
	sh := share(t, srv, th)

	// When
	rec := getPublic(srv, "/api/shares/"+sh.Token)

	// Then nothing about what the turn cost, and nothing to ask next: there is
	// no composer on that page to ask it with.
	body := rec.Body.String()
	for _, leak := range []string{`"usage"`, `"followups":[`, `"calls"`} {
		if strings.Contains(body, leak) {
			t.Errorf("the public payload carries %s:\n%s", leak, body)
		}
	}
}

func TestPublicShare_stopsAtTheCeiling(t *testing.T) {
	// Given a link made before a second question was asked
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th := sharedTurn(t, st, testSubject)
	sh := share(t, srv, th)
	later, err := st.AddQuestion(ctx, th, "ba", "en", "And then?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Finish(ctx, later.ID, "Then this.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// When
	rec := getPublic(srv, "/api/shares/"+sh.Token)

	// Then
	if strings.Contains(rec.Body.String(), "Then this.") {
		t.Error("a turn asked after the link was made is on it")
	}
}

func TestPublicShare_unknownRevokedAndDeletedAllAnswerTheSame(t *testing.T) {
	srv, st, _ := shareServer(t)
	th := sharedTurn(t, st, testSubject)
	revoked := share(t, srv, th)
	if rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d/share", th), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", rec.Code)
	}
	gone := share(t, srv, sharedTurn(t, st, testSubject))
	if rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", gone.ThreadID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}

	for name, token := range map[string]string{
		"unknown": "AAAAAAAAAAAAAAAAAAAAAA",
		"revoked": revoked.Token,
		"deleted": gone.Token,
	} {
		rec := getPublic(srv, "/api/shares/"+token)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s token: status = %d, want 404", name, rec.Code)
		}
		if rec.Header().Get("X-Robots-Tag") == "" {
			t.Errorf("%s token: the 404 is indexable", name)
		}
	}
}

func TestPublicShareSource_opensACitedFileAndNothingElse(t *testing.T) {
	// Given
	srv, st, src := shareServer(t)
	sh := share(t, srv, sharedTurn(t, st, testSubject))
	q := func(repo, path, sha string) string {
		return "/api/shares/" + sh.Token + "/source?" +
			url.Values{"repo": {repo}, "path": {path}, "sha": {sha}}.Encode()
	}

	// When the reader follows the citation
	rec := getPublic(srv, q("rongo", "a.go", "abc"))

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("cited file: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if src.gotPath != "a.go" || src.sha != "abc" {
		t.Errorf("read %q at %q, want the cited file at the cited commit", src.gotPath, src.sha)
	}

	// And nothing else in the corpus is reachable through the link.
	if rec := getPublic(srv, q("rongo", "internal/config/config.go", "abc")); rec.Code != http.StatusNotFound {
		t.Errorf("an uncited file: status = %d, want 404", rec.Code)
	}
	if rec := getPublic(srv, q("rongo", "a.go", "deadbeef")); rec.Code != http.StatusNotFound {
		t.Errorf("the cited file at another commit: status = %d, want 404", rec.Code)
	}
}

func TestShare_isRefusedWhileTheTurnIsStillBeingWritten(t *testing.T) {
	// Given a question whose answer has not landed
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th, err := st.Create(ctx, testSubject, "How?")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0); err != nil {
		t.Fatalf("add question: %v", err)
	}

	// When
	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share", th.ID), "")

	// Then 409, not 400: the request is fine and will work in a moment.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

func TestShare_anotherReadersThreadIsNotFound(t *testing.T) {
	srv, st, _ := shareServer(t)
	th := sharedTurn(t, st, otherSubject)

	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share", th), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestShareUpdate_movesTheCeilingAndKeepsTheLink(t *testing.T) {
	// Given a link the thread has moved on from
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th := sharedTurn(t, st, testSubject)
	first := share(t, srv, th)
	later, err := st.AddQuestion(ctx, th, "ba", "en", "And then?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Finish(ctx, later.ID, "Then this.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// When
	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share/update", th), "")

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var raised threads.Share
	if err := json.Unmarshal(rec.Body.Bytes(), &raised); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raised.Token != first.Token {
		t.Errorf("the link changed on update: %q -> %q", first.Token, raised.Token)
	}
	if !strings.Contains(getPublic(srv, "/api/shares/"+raised.Token).Body.String(), "Then this.") {
		t.Error("the turn the update took in is still not on the link")
	}
}

func TestShares_listsThisReadersLiveLinks(t *testing.T) {
	srv, st, _ := shareServer(t)
	sh := share(t, srv, sharedTurn(t, st, testSubject))
	// Someone else's link, made directly through the store.
	other := sharedTurn(t, st, otherSubject)
	if _, err := st.Share(context.Background(), otherSubject, other); err != nil {
		t.Fatalf("share: %v", err)
	}

	rec := act(srv, http.MethodGet, "/api/shares", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var list []threads.Share
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].Token != sh.Token {
		t.Fatalf("shares = %+v, want only this reader's one link", list)
	}
}

// brokenShares wires the share routes over a store whose database is shut, so
// every one of them takes its failure branch. Those branches are the whole
// reason a reader gets a clear error instead of a blank page, and they are the
// half of each handler no happy-path test ever reaches.
func brokenShares(t *testing.T) (*Server, int64) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	if _, err := svc.UpsertUser(testSubject, "", true); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	st := threads.NewStore(db)
	th, err := st.Create(context.Background(), testSubject, "How?")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	srv := NewServer(Deps{Auth: svc, Threads: st, Source: &fakeSource{}})
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return srv, th.ID
}

func TestShareRoutes_aBrokenDatabaseIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	srv, th := brokenShares(t)

	for name, path := range map[string]struct{ method, url string }{
		"share":  {http.MethodPost, fmt.Sprintf("/api/threads/%d/share", th)},
		"update": {http.MethodPost, fmt.Sprintf("/api/threads/%d/share/update", th)},
		"revoke": {http.MethodDelete, fmt.Sprintf("/api/threads/%d/share", th)},
	} {
		if rec := act(srv, path.method, path.url, ""); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d (%s), want 500", name, rec.Code, rec.Body.String())
		}
	}
}

func TestPublicShareRoutes_aBrokenDatabaseTellsNobodyWhy(t *testing.T) {
	srv, _ := brokenShares(t)

	// The public read cannot tell a broken database from an unknown token
	// without becoming an oracle, so it answers 404 either way — and logs the
	// reason for whoever has to fix it.
	if rec := getPublic(srv, "/api/shares/AAAAAAAAAAAAAAAAAAAAAA"); rec.Code != http.StatusNotFound {
		t.Errorf("public read: status = %d, want 404", rec.Code)
	}
	// The source route has nothing to hide: a citation lookup that failed is
	// not a statement about whether the link exists.
	src := getPublic(srv, "/api/shares/AAAAAAAAAAAAAAAAAAAAAA/source?repo=r&path=p&sha=s")
	if src.Code != http.StatusInternalServerError {
		t.Errorf("public source: status = %d (%s), want 500", src.Code, src.Body.String())
	}
}

func TestShareRoutes_sayTheFeatureIsOffRatherThanCrash(t *testing.T) {
	// Threads unconfigured: every route answers 503, the shape the rest of
	// this package already uses.
	srv := NewServer(Deps{Auth: devAuth(t)})

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"share":  act(srv, http.MethodPost, "/api/threads/1/share", ""),
		"update": act(srv, http.MethodPost, "/api/threads/1/share/update", ""),
		"revoke": act(srv, http.MethodDelete, "/api/threads/1/share", ""),
		"list":   act(srv, http.MethodGet, "/api/shares", ""),
		"public": getPublic(srv, "/api/shares/tok"),
		"pubsrc": getPublic(srv, "/api/shares/tok/source?repo=r&path=p&sha=s"),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d (%s), want 503", name, rec.Code, rec.Body.String())
		}
	}
}

func TestShare_malformedThreadIDIsRefusedBeforeTheStore(t *testing.T) {
	srv, _, _ := shareServer(t)

	rec := act(srv, http.MethodPost, "/api/threads/not-a-number/share", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

func TestRevokeShare_aThreadWithNoLinkIsNotFound(t *testing.T) {
	srv, st, _ := shareServer(t)
	th := sharedTurn(t, st, testSubject)

	rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d/share", th), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestShareUpdate_aThreadWithNoLinkIsNotFound(t *testing.T) {
	srv, st, _ := shareServer(t)
	th := sharedTurn(t, st, testSubject)

	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share/update", th), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestShareUpdate_isRefusedWhileTheTurnIsStillBeingWritten(t *testing.T) {
	srv, st, _ := shareServer(t)
	ctx := context.Background()
	th := sharedTurn(t, st, testSubject)
	share(t, srv, th)
	if _, err := st.AddQuestion(ctx, th, "ba", "en", "And then?", 0); err != nil {
		t.Fatalf("add question: %v", err)
	}

	rec := act(srv, http.MethodPost, fmt.Sprintf("/api/threads/%d/share/update", th), "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}
