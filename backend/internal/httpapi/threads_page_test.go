package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// listPage is what GET /api/threads answers: one page and the cursor for the
// next, or null on the last.
type listPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

func TestThreads_answersOnePageAndTheCursorForTheNext(t *testing.T) {
	// Given three threads of this reader's
	ctx := context.Background()
	srv, st := threadActions(t)
	for i := 1; i <= 3; i++ {
		if _, err := st.Create(ctx, testSubject, fmt.Sprintf("q%d", i)); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// When the rail asks for two
	rec := act(srv, http.MethodGet, "/api/threads?limit=2", "")

	// Then it gets the newest two and a cursor that is the second's address
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var page listPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if len(page.Items) != 2 || page.Items[0]["title"] != "q3" {
		t.Fatalf("items = %+v, want q3 and q2", page.Items)
	}
	if page.NextCursor == nil || *page.NextCursor != page.Items[1]["id"] {
		t.Fatalf("next_cursor = %v, want the last row's id", page.NextCursor)
	}

	// And the page after it is the last, with no cursor
	rec = act(srv, http.MethodGet, "/api/threads?limit=2&cursor="+*page.NextCursor, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if len(page.Items) != 1 || page.Items[0]["title"] != "q1" || page.NextCursor != nil {
		t.Errorf("second page = %+v, want q1 alone and final", page)
	}
}

func TestThreads_aLimitThatIsNotOneIsRefused(t *testing.T) {
	srv, _ := threadActions(t)
	for _, q := range []string{"limit=0", "limit=1001", "limit=ten"} {
		rec := act(srv, http.MethodGet, "/api/threads?"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	// No limit at all is the default page, not an error.
	if rec := act(srv, http.MethodGet, "/api/threads", ""); rec.Code != http.StatusOK {
		t.Errorf("no limit: status = %d, want 200", rec.Code)
	}
}

func TestSearchThreads_findsByTitleAndByWhatWasSaid(t *testing.T) {
	// Given one thread whose title matches and one whose answer does
	ctx := context.Background()
	srv, st := threadActions(t)
	byTitle, err := st.Create(ctx, testSubject, "How is the mail sent?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	byContent, err := st.Create(ctx, testSubject, "Nightly job")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := st.AddQuestion(ctx, byContent.ID, "dev", "en", "what runs at night", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := st.Finish(ctx, m.ID, "The mail digest is built there.", nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// When
	rec := act(srv, http.MethodGet, "/api/threads/search?q=mail", "")

	// Then the title hit leads, the content hit follows with its snippet
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if len(body.Items) != 2 || body.Items[0]["id"] != byTitle.PublicID || body.Items[1]["id"] != byContent.PublicID {
		t.Fatalf("items = %+v, want the title hit then the content hit", body.Items)
	}
	if _, has := body.Items[0]["snippet"]; has {
		t.Errorf("a title-only hit carries a snippet: %+v", body.Items[0])
	}
	if s, _ := body.Items[1]["snippet"].(string); s == "" {
		t.Errorf("the content hit has no snippet: %+v", body.Items[1])
	}
}

func TestSearchThreads_needsAQuery(t *testing.T) {
	srv, _ := threadActions(t)
	for _, q := range []string{"", "q=", "q=%20%20"} {
		rec := act(srv, http.MethodGet, "/api/threads/search?"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestThreadSummary_isTheRowAndOnlyTheOwners(t *testing.T) {
	ctx := context.Background()
	srv, st := threadActions(t)
	mine, err := st.Create(ctx, testSubject, "How is sign-in done?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	theirs, err := st.Create(ctx, otherSubject, "Theirs")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := act(srv, http.MethodGet, fmt.Sprintf("/api/threads/%s/summary", mine.PublicID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if got["id"] != mine.PublicID || got["title"] != "How is sign-in done?" || got["title_pending"] != true {
		t.Errorf("summary = %+v", got)
	}

	if rec := act(srv, http.MethodGet, fmt.Sprintf("/api/threads/%s/summary", theirs.PublicID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("another reader's thread = %d, want 404", rec.Code)
	}
	if rec := act(srv, http.MethodGet, "/api/threads/AAAAAAAAAAAAAAAAAAAAAA/summary", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown address = %d, want 404", rec.Code)
	}
}
