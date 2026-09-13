package threads

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedThreads creates n threads for subject, titled "thread 1" … "thread n",
// and answers each with the given text when one is passed.
func seedThreads(t *testing.T, s *Store, subject string, n int, answer string) []Thread {
	t.Helper()
	ctx := context.Background()
	out := make([]Thread, 0, n)
	for i := 1; i <= n; i++ {
		th, err := s.Create(ctx, subject, fmt.Sprintf("thread %d", i))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if answer != "" {
			m, err := s.AddQuestion(ctx, th.ID, "dev", "en", fmt.Sprintf("thread %d", i), 0)
			if err != nil {
				t.Fatalf("add question: %v", err)
			}
			if err := s.Finish(ctx, m.ID, answer, nil); err != nil {
				t.Fatalf("finish: %v", err)
			}
		}
		out = append(out, th)
	}
	return out
}

func TestListPage_pagesByCursorNewestFirst(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	seedThreads(t, s, testSubject, 25, "")

	var (
		got    []string
		cursor string
		pages  int
	)
	for {
		page, err := s.ListPage(ctx, testSubject, ListOptions{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, th := range page.Items {
			got = append(got, th.Title)
		}
		if page.NextCursor == nil {
			break
		}
		// The cursor is the last row's address, never its row number.
		if !address.MatchString(*page.NextCursor) {
			t.Fatalf("cursor = %q, want a public id", *page.NextCursor)
		}
		cursor = *page.NextCursor
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3 (10, 10, 5)", pages)
	}
	if len(got) != 25 || got[0] != "thread 25" || got[24] != "thread 1" {
		t.Errorf("titles = %v, want 25 down to 1 with no repeat", got)
	}
}

func TestListPage_aFullLastPageCostsOneEmptyRequest(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	seedThreads(t, s, testSubject, 10, "")

	first, err := s.ListPage(ctx, testSubject, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.NextCursor == nil {
		t.Fatal("a full page must offer a cursor: the store cannot know it was the last")
	}
	second, err := s.ListPage(ctx, testSubject, ListOptions{Limit: 10, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second.Items) != 0 || second.NextCursor != nil {
		t.Errorf("second page = %+v, want empty and final", second)
	}
}

func TestListPage_aCursorNamingNoRowOfMineEndsTheList(t *testing.T) {
	// The browser appends what it is handed, so a cursor that no longer
	// resolves — someone else's thread, or one deleted mid-scroll — must
	// answer "nothing more", never page one again.
	s := NewStore(threadDB(t))
	ctx := context.Background()
	mine := seedThreads(t, s, testSubject, 3, "")
	theirs := seedThreads(t, s, "bruno", 3, "")
	if _, err := s.Delete(ctx, testSubject, mine[1].ID); err != nil {
		t.Fatal(err)
	}

	for name, cursor := range map[string]string{"theirs": theirs[1].PublicID, "deleted": mine[1].PublicID} {
		page, err := s.ListPage(ctx, testSubject, ListOptions{Cursor: cursor})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(page.Items) != 0 || page.NextCursor != nil {
			t.Errorf("%s: page = %+v, want empty and final", name, page)
		}
	}
}

func TestListPage_defaultsAndCapsTheLimit(t *testing.T) {
	if got := EffectiveLimit(0); got != DefaultListLimit {
		t.Errorf("EffectiveLimit(0) = %d, want %d", got, DefaultListLimit)
	}
	if got := EffectiveLimit(5000); got != MaxListLimit {
		t.Errorf("EffectiveLimit(5000) = %d, want %d", got, MaxListLimit)
	}
	if got := EffectiveLimit(7); got != 7 {
		t.Errorf("EffectiveLimit(7) = %d", got)
	}
}

func TestGet_isOneRowAndOnlyTheOwners(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)

	got, ok, err := s.Get(ctx, testSubject, th)
	if err != nil || !ok {
		t.Fatalf("get = %v, %v", ok, err)
	}
	if got.ID != th || got.Title != "frage" || !address.MatchString(got.PublicID) {
		t.Errorf("thread = %+v", got)
	}
	if _, ok, err := s.Get(ctx, "bruno", th); err != nil || ok {
		t.Errorf("bruno reads anna's thread: ok=%v err=%v", ok, err)
	}
}

func TestSearch_titleHitsComeFirstThenContentWithASnippet(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	// Found by content only: the title says nothing about mailing.
	byContent := seedThreads(t, s, testSubject, 1, "The teaser mail is sent by the nightly cron.")[0]
	// Found by title, and its answer says something else entirely.
	byTitle, err := s.Create(ctx, testSubject, "Where is the mail template?")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.AddQuestion(ctx, byTitle.ID, "dev", "en", "Where is the mail template?", 0)
	_ = s.Finish(ctx, m.ID, "In the resources folder.", nil)
	// Neither.
	seedThreads(t, s, testSubject, 1, "Nothing to do with it.")

	hits, err := s.Search(ctx, testSubject, "mail", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want two", hits)
	}
	if hits[0].ID != byTitle.ID || hits[1].ID != byContent.ID {
		t.Errorf("order = %v, want title hit then content hit", []int64{hits[0].ID, hits[1].ID})
	}
	if !strings.Contains(hits[1].Snippet, "«mail»") {
		t.Errorf("content hit snippet = %q, want the match marked", hits[1].Snippet)
	}
	// A title hit whose messages also match gets the snippet too: "mail" is
	// in its question.
	if !strings.Contains(hits[0].Snippet, "«mail»") {
		t.Errorf("title hit snippet = %q, want its own match marked", hits[0].Snippet)
	}
}

func TestSearch_oneRowPerThreadHoweverManyTurnsMatch(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	for i := 0; i < 3; i++ {
		m, err := s.AddQuestion(ctx, th, "dev", "en", "again", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, m.ID, "the widget factory builds widgets", nil); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := s.Search(ctx, testSubject, "widget", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != th {
		t.Errorf("hits = %+v, want the one thread once", hits)
	}
}

func TestSearch_prefixMatchesEveryTermAndIsPerReader(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	mine := seedThreads(t, s, testSubject, 1, "the nightly cron sends the teaser")[0]
	seedThreads(t, s, "bruno", 1, "the nightly cron sends the teaser")

	hits, err := s.Search(ctx, testSubject, "night teas", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != mine.ID {
		t.Errorf("hits = %+v, want anna's thread alone", hits)
	}
	if hits, _ := s.Search(ctx, testSubject, "night mail", 0); len(hits) != 0 {
		t.Errorf("every term is required, got %+v", hits)
	}
}

func TestSearch_operatorsArePunctuationAndTitlesFoldLikeAnswers(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	seedThreads(t, s, testSubject, 1, "plain")
	odd, err := s.Create(ctx, testSubject, "100% done_ on the Übersicht")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.AddQuestion(ctx, odd.ID, "dev", "en", "what about foo-bar: \"quoted\"", 0)
	_ = s.Finish(ctx, m.ID, "", nil)

	// Case and accents fold in a title as they do in an answer, and a "%" or
	// a "_" is punctuation the tokenizer drops, not a wildcard.
	for _, q := range []string{"100 done", "ubersicht", "ÜBERSICHT", "foo bar"} {
		hits, err := s.Search(ctx, testSubject, q, 0)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(hits) != 1 || hits[0].ID != odd.ID {
			t.Errorf("%q matched %+v, want the one odd title", q, hits)
		}
	}
	// Bare-word FTS syntax, which would be a parse error unquoted, and
	// terms that tokenize to nothing at all.
	for _, q := range []string{"foo-bar:", `"quoted"`, "NOT", "%", "_"} {
		if _, err := s.Search(ctx, testSubject, q, 0); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	if hits, _ := s.Search(ctx, testSubject, "   ", 0); len(hits) != 0 {
		t.Errorf("blank search = %+v, want nothing", hits)
	}
}

func TestThreadFTS_followsTheTitle(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	if _, err := s.Rename(ctx, testSubject, th, "The kettle question"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, testSubject, "kettle", 0); len(hits) != 1 {
		t.Errorf("renamed title not found: %+v", hits)
	}
	if hits, _ := s.Search(ctx, testSubject, "frage", 0); len(hits) != 0 {
		t.Errorf("the old title is still found: %+v", hits)
	}
	if _, err := s.Delete(ctx, testSubject, th); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, testSubject, "kettle", 0); len(hits) != 0 {
		t.Errorf("a deleted thread is still found: %+v", hits)
	}
}

func TestSearch_isCapped(t *testing.T) {
	s := NewStore(threadDB(t))
	ctx := context.Background()
	seedThreads(t, s, testSubject, 6, "")

	hits, err := s.Search(ctx, testSubject, "thread", 4)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 4 {
		t.Errorf("hits = %d, want the cap of 4", len(hits))
	}
	if got := ftsPrefixQuery(`a "b" c`); got != `"a"* """b"""* "c"*` {
		t.Errorf("fts query = %s", got)
	}
}

func TestMessageFTS_followsTheRecord(t *testing.T) {
	s, ctx, th, db := newThreadStore(t)
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM message_fts WHERE message_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		return n
	}
	m, err := s.AddQuestion(ctx, th, "dev", "en", "where is the kettle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if count("kettle") != 1 {
		t.Error("the question is not indexed on insert")
	}
	if err := s.Finish(ctx, m.ID, "in the pantry", nil); err != nil {
		t.Fatal(err)
	}
	if count("pantry") != 1 || count("kettle") != 1 {
		t.Error("the answer is not indexed when it lands, or the question was lost")
	}
	if _, err := s.Delete(ctx, testSubject, th); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_fts`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d index rows outlive the deleted thread", left)
	}
}
