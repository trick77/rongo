package threads

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/usage"
)

// testSubject is the user threads are created for in every test that does
// not exercise cross-user isolation itself.
const testSubject = "anna"

// newThreadStore hands back a Store, a context, a thread already created for
// testSubject, and the underlying *sql.DB for tests that need to seed data
// Store has no method for (there is deliberately no Store.DB() accessor).
func newThreadStore(t *testing.T) (*Store, context.Context, int64, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, err := s.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return s, ctx, th.ID, db
}

// twoCandidateClarification is a minimal card with two candidates, for tests
// that only care about the choice being recorded, not the content.
func twoCandidateClarification() ask.Clarification {
	return ask.Clarification{
		Understanding: ask.Understanding{Intent: "how"},
		Candidates: []ask.Candidate{
			{Repo: "peeq", Branch: "master", ModuleKey: "a", Title: "A", Summary: "s", Hits: []retrieve.Hit{{ChunkID: 1}}},
			{Repo: "loom", Branch: "master", ModuleKey: "a", Title: "B", Summary: "s", Hits: []retrieve.Hit{{ChunkID: 2}}},
		},
	}
}

// insertChunk writes just enough of files/chunks for Sources to join against:
// a repo, a file and a chunk with real text and lines.
func insertChunk(t *testing.T, db *sql.DB, id int64, repo, path, text string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO repo_state (name, clone_url, branch) VALUES (?, 'x', 'master')`, repo); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES (?, ?, 'deadbeef')`, repo, path)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed file id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO chunks (id, file_id, ordinal, start_line, end_line, symbol, text, raw_text, content_hash)
		VALUES (?, ?, 0, 1, 3, '', ?, ?, ?)`,
		id, fileID, text, text, text); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
}

// deleteChunk simulates a re-index that removed a chunk.
func deleteChunk(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM chunks WHERE id = ?`, id); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}
}

func threadDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, s := range []string{"anna", "bruno"} {
		if _, err := db.Exec(`INSERT INTO users (subject, email, is_admin) VALUES (?, ?, 0)`, s, s+"@x.invalid"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	return db
}

func TestCreate_titleStartsAsTheQuestionSoTheSidebarNeverWaits(t *testing.T) {
	s := NewStore(threadDB(t))

	got, err := s.Create(context.Background(), "anna", "How is the teaser mail sent?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.Title == "" {
		t.Fatal("empty title; the sidebar entry must be there the moment the question is sent")
	}
	if !strings.HasPrefix(got.Title, "How is the teaser mail") {
		t.Errorf("title = %q, want the first words of the question", got.Title)
	}
}

func TestSetTitle_anEmptyModelTitleLeavesThePlaceholderStanding(t *testing.T) {
	// A title call that returns nothing is not a failure anyone needs to see.
	// Overwriting the placeholder with "" would blank the sidebar entry.
	s := NewStore(threadDB(t))
	th, _ := s.Create(context.Background(), "anna", "How does shipping work?")

	if err := s.SetTitle(context.Background(), th.ID, th.Title, "   "); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(context.Background(), "anna")
	if list[0].Title == "" {
		t.Error("the placeholder was overwritten with an empty title")
	}
}

func TestCreate_marksTheTitlePendingUntilTheCallSettlesIt(t *testing.T) {
	// The header shows a title, never the question's first 48 runes. It can
	// only tell the two apart if the row says which it is holding.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if !th.TitlePending {
		t.Error("a fresh thread carries the placeholder; it must say so")
	}
	list, _ := s.List(ctx, "anna")
	if !list[0].TitlePending {
		t.Error("the list must say so too; it is what the header reads")
	}
}

func TestSetTitle_anEmptyTitleStillEndsTheWaiting(t *testing.T) {
	// The title call failed, or wrote something that was not a title. The
	// placeholder stands, but nothing more is coming: a row left pending would
	// read as "New question" in the header for the rest of its life.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if err := s.SetTitle(ctx, th.ID, th.Title, ""); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(ctx, "anna")
	if list[0].TitlePending {
		t.Error("still pending after the title call came back empty")
	}
	if list[0].Title != th.Title {
		t.Errorf("title = %q, want the placeholder left standing", list[0].Title)
	}
}

func TestSettleTitles_endsTheWaitingLeftBehindByACrash(t *testing.T) {
	// A title call cannot outlive its process. Whatever is still pending at
	// boot was orphaned, and a single-turn thread never gets a later turn to
	// settle it: without this it holds "New question" in the header for good.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if err := s.SettleTitles(ctx); err != nil {
		t.Fatalf("SettleTitles: %v", err)
	}

	list, _ := s.List(ctx, "anna")
	if list[0].TitlePending {
		t.Error("a thread orphaned mid-title must not go on waiting")
	}
	if list[0].Title != th.Title {
		t.Errorf("title = %q, want the placeholder left standing", list[0].Title)
	}
}

func TestSettlingATitle_reportsAWriteThatNeverLanded(t *testing.T) {
	// Both writes are made for their effect on a later read — the header asks
	// the list, not the writer — so a silent failure would show up as a thread
	// stuck on "New question" with nothing in the log to say why.
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, _ := s.Create(ctx, "anna", "How does shipping work?")
	db.Close()

	if err := s.SetTitle(ctx, th.ID, th.Title, ""); err == nil {
		t.Error("SetTitle reported success on a closed database")
	}
	if err := s.SettleTitles(ctx); err == nil {
		t.Error("SettleTitles reported success on a closed database")
	}
}

func TestRename_endsTheWaitingToo(t *testing.T) {
	// A name the reader typed is a title. The header must show it rather than
	// hold its place for a model call that will find the row renamed.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if _, err := s.Rename(ctx, "anna", th.ID, "Mine"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	list, _ := s.List(ctx, "anna")
	if list[0].TitlePending {
		t.Error("a typed name is a title; the thread is not waiting for one")
	}
}

func TestSetTitle_replacesThePlaceholderItWasHanded(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if err := s.SetTitle(ctx, th.ID, th.Title, "Shipping, end to end"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(ctx, "anna")
	if list[0].Title != "Shipping, end to end" {
		t.Errorf("title = %q, want the model's", list[0].Title)
	}
	if list[0].TitlePending {
		t.Error("the title arrived; the row must stop reading as pending")
	}
}

func TestSetTitle_leavesARenamedThreadAlone(t *testing.T) {
	// The title call lands seconds after the question. A reader who renamed
	// the thread in that window keeps their name: the row no longer holds the
	// placeholder the background write was handed.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	if _, err := s.Rename(ctx, "anna", th.ID, "Mine"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := s.SetTitle(ctx, th.ID, th.Title, "The model's idea"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(ctx, "anna")
	if list[0].Title != "Mine" {
		t.Errorf("title = %q, want the typed name to have survived", list[0].Title)
	}
}

func TestRename_refusesAnEmptyTitleAndAnotherReadersThread(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	// Given an empty title
	renamed, err := s.Rename(ctx, "anna", th.ID, "   ")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed {
		t.Error("an empty title renamed the thread")
	}

	// Given someone else's thread
	renamed, err = s.Rename(ctx, "bob", th.ID, "Bob's")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed {
		t.Error("bob renamed anna's thread")
	}

	list, _ := s.List(ctx, "anna")
	if !strings.HasPrefix(list[0].Title, "How does shipping") {
		t.Errorf("title = %q, want the placeholder untouched", list[0].Title)
	}
}

func TestDelete_takesTheThreadAndEverythingUnderIt(t *testing.T) {
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, _ := s.Create(ctx, "anna", "How does shipping work?")
	m, err := s.AddQuestion(ctx, th.ID, "ba", "en", "How does shipping work?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := s.Finish(ctx, m.ID, "It ships.", []ask.Citation{{Repo: "r", Path: "a.go", StartLine: 1, EndLine: 2}}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// When
	deleted, err := s.Delete(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Then
	if !deleted {
		t.Fatal("Delete reported no row")
	}
	list, _ := s.List(ctx, "anna")
	if len(list) != 0 {
		t.Errorf("threads = %d, want the list empty", len(list))
	}
	// The cascades are the schema's, but nothing else deletes a message, so
	// a row left behind here would outlive every reader of it.
	var messages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Errorf("messages = %d, want the cascade to have taken them", messages)
	}
	var citations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM citations`).Scan(&citations); err != nil {
		t.Fatalf("count citations: %v", err)
	}
	if citations != 0 {
		t.Errorf("citations = %d, want the cascade to have taken them", citations)
	}
}

func TestDelete_leavesAnotherReadersThreadStanding(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")

	deleted, err := s.Delete(ctx, "bob", th.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if deleted {
		t.Error("bob deleted anna's thread")
	}
	list, _ := s.List(ctx, "anna")
	if len(list) != 1 {
		t.Errorf("threads = %d, want anna's thread untouched", len(list))
	}
}

func TestFinish_storesTheAnswerWithItsEvidence(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, err := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	err = s.Finish(ctx, m.ID, "That is how it works [1].", []ask.Citation{
		{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 9, SHA: "0123abc"},
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Answer != "That is how it works [1]." {
		t.Fatalf("messages = %+v", msgs)
	}
	if len(msgs[0].Citations) != 1 || msgs[0].Citations[0].Branch != "master" {
		t.Errorf("citations = %+v, want the branch kept — a forge URL without it may 404", msgs[0].Citations)
	}
	// The commit travels too: the source viewer reads the file at it, so the
	// cited lines stay the cited lines after the branch has moved on.
	if msgs[0].Citations[0].SHA != "0123abc" {
		t.Errorf("citations = %+v, want the commit kept", msgs[0].Citations)
	}
}

func TestSaveUsage_everyCallOfATurnComesBackWithTheThreadEvenWhenItAskedOrFailed(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	failed, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	answered, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How exactly?", 0)

	// A turn that failed after its gates ran still paid for the gates.
	if err := s.Fail(ctx, failed.ID, "The turn failed."); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := s.SaveUsage(ctx, failed.ID, []usage.Call{
		{Step: "understand", Model: "mimo-v2.5", Prompt: 100, Completion: 20},
		{Step: "embed", Model: "text-embedding-3-small", Prompt: 12},
	}); err != nil {
		t.Fatalf("SaveUsage: %v", err)
	}
	if err := s.SaveUsage(ctx, answered.ID, []usage.Call{
		{Step: "understand", Model: "mimo-v2.5", Prompt: 110, Completion: 22},
		{Step: "answer", Model: "mimo-v2.5-pro", Prompt: 2000, Completion: 400},
	}); err != nil {
		t.Fatalf("SaveUsage: %v", err)
	}
	// Saving nothing is not an error and writes nothing.
	if err := s.SaveUsage(ctx, answered.ID, nil); err != nil {
		t.Fatalf("SaveUsage(nil): %v", err)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if got := msgs[0].Calls; len(got) != 2 || got[0].Step != "understand" || got[1].Model != "text-embedding-3-small" || got[1].Prompt != 12 {
		t.Errorf("failed turn's calls = %+v, want its two gate calls in order", got)
	}
	if got := msgs[1].Calls; len(got) != 2 || got[1].Step != "answer" || got[1].Completion != 400 {
		t.Errorf("answered turn's calls = %+v", got)
	}

	// Another user's thread stays unreadable, usage included.
	other, _ := s.Messages(ctx, "bruno", th.ID)
	if len(other) != 0 {
		t.Errorf("bruno can read anna's usage: %+v", other)
	}
}

func TestFail_keepsTheQuestionInTheRecord(t *testing.T) {
	// A turn that broke still happened. Dropping the question would leave the
	// reader wondering what they asked.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)

	if err := s.Fail(ctx, m.ID, "the model did not answer"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	msgs, _ := s.Messages(ctx, "anna", th.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want the failed turn kept", msgs)
	}
	if msgs[0].Question != "How?" || msgs[0].Error == "" {
		t.Errorf("message = %+v, want the question plus the failure", msgs[0])
	}
}

func TestMessages_anotherUsersThreadIsNotReadable(t *testing.T) {
	// The subject is part of the query, not a check afterwards: a slip here
	// hands over someone else's conversation.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	_ = s.Finish(ctx, m.ID, "geheim", nil)

	got, err := s.Messages(ctx, "bruno", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("bruno read anna's thread: %+v", got)
	}
	owns, err := s.Owns(ctx, "bruno", th.ID)
	if err != nil {
		t.Fatalf("Owns: %v", err)
	}
	if owns {
		t.Error("Owns said bruno owns anna's thread")
	}
}

func TestAddQuestion_appendsRatherThanRewrites(t *testing.T) {
	// The thread is a record. A follow-up adds a turn; nothing replaces one.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "First question?")
	first, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "First question?", 0)
	_ = s.Finish(ctx, first.ID, "First answer.", nil)
	second, err := s.AddQuestion(ctx, th.ID, "dev", "en", "Und als Dev?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	_ = s.Finish(ctx, second.ID, "Second answer.", nil)

	msgs, _ := s.Messages(ctx, "anna", th.ID)

	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both turns", len(msgs))
	}
	if msgs[0].Answer != "First answer." {
		t.Errorf("the first answer changed: %q", msgs[0].Answer)
	}
	if msgs[0].Audience != "ba" || msgs[1].Audience != "dev" {
		t.Errorf("audiences = %q/%q, want them per message", msgs[0].Audience, msgs[1].Audience)
	}
}

func TestClarifyStoresTheCardAndServesItBackWithTheThread(t *testing.T) {
	// Given a question that ended by asking
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "how is sign-in done?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	// When
	id, err := s.Clarify(ctx, msg.ID, ask.Clarification{
		Understanding: ask.Understanding{CodeTerms: []string{"session", "oidc"}},
		Candidates: []ask.Candidate{
			{Repo: "peeq", Branch: "master", ModuleKey: "backend/internal/auth", Title: "Sign-in in peeq", Summary: "Sessions over cookies.", Hits: []retrieve.Hit{{ChunkID: 7}}},
			{Repo: "loom", Branch: "master", ModuleKey: "backend/internal/auth", Title: "Anmeldung in loom", Summary: "Dasselbe, anderes Produkt.", Hits: []retrieve.Hit{{ChunkID: 9}}},
		},
	})
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}

	// Then: the thread serves the card, so a reload renders it instead of a
	// turn that looks stuck
	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if msgs[0].Clarification == nil {
		t.Fatal("the stored turn must carry its clarification")
	}
	if len(msgs[0].Clarification.Candidates) != 2 {
		t.Errorf("got %d candidates", len(msgs[0].Clarification.Candidates))
	}

	// And the hits the card was built from come back for the resumed turn
	u, hits, err := s.CandidateHits(ctx, testSubject, id, 1)
	if err != nil {
		t.Fatalf("candidate hits: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != 9 {
		t.Errorf("got hits %v, want the second candidate's", hits)
	}
	if len(u.CodeTerms) != 2 {
		t.Error("the understanding must come back with them")
	}
}

func TestAClarificationIsAnsweredOnceAndTheCardSaysSo(t *testing.T) {
	// One card, one answer. The fact lives on the answer's
	// from_clarification_id, and Clarification reports it so the handler can
	// refuse a second resume before it costs a model call.
	s, ctx, threadID, _ := newThreadStore(t)
	first, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	id, err := s.Clarify(ctx, first.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}

	clar, err := s.Clarification(ctx, testSubject, first.ID)
	if err != nil || clar == nil {
		t.Fatalf("clarification: %v", err)
	}
	if clar.Answered {
		t.Error("a card nobody has answered must not read as answered")
	}

	a, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	if err := s.LinkChoice(ctx, testSubject, a.ID, id, 0); err != nil {
		t.Fatalf("link choice: %v", err)
	}

	clar, err = s.Clarification(ctx, testSubject, first.ID)
	if err != nil || clar == nil {
		t.Fatalf("clarification: %v", err)
	}
	if !clar.Answered {
		t.Error("the answer that came out of the card closes it")
	}

	// Card and answer both stay in the record, and the choice is still
	// readable on the turn that resumed.
	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 — the card and its answer", len(msgs))
	}
	if msgs[0].Clarification == nil {
		t.Error("the card stays on the turn that asked")
	}
	if msgs[1].FromCandidateIdx != 0 || msgs[1].FromClarificationID != id {
		t.Errorf("choice recorded as %d on clarification %d", msgs[1].FromCandidateIdx, msgs[1].FromClarificationID)
	}
}

func TestSourcesComeBackAndAVanishedChunkIsSimplyMissing(t *testing.T) {
	// Given an answer stored with two sources, one of whose chunks a re-index
	// then removes
	s, ctx, threadID, db := newThreadStore(t)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	insertChunk(t, db, 2, "peeq", "b.go", "package b")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{ChunkID: 1, Reason: "hit", Hop: 0},
		{ChunkID: 2, Reason: "reference:NewGrant", Hop: 1},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	deleteChunk(t, db, 2)

	// When
	got, total, err := s.Sources(ctx, testSubject, msg.ID)

	// Then
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 — message_sources still holds both rows", total)
	}
	if len(got) != 1 || got[0].ChunkID != 1 {
		t.Fatalf("got %v, want only the surviving chunk", got)
	}
	if got[0].Text == "" {
		t.Error("a source must come back with its text, or the answer cannot be rewritten from it")
	}
}

func TestAForeignSubjectGetsNothingFromClarificationCandidateHitsOrSources(t *testing.T) {
	// A raw id off the wire must never answer for a thread it does not own —
	// the store re-checks ownership itself rather than trusting a handler to
	// have already done so.
	const other = "bruno"
	s, ctx, threadID, db := newThreadStore(t)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	id, err := s.Clarify(ctx, msg.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{{ChunkID: 1, Reason: "hit"}}); err != nil {
		t.Fatalf("save sources: %v", err)
	}

	// Then: bruno, who owns none of this, gets nothing back
	clar, err := s.Clarification(ctx, other, msg.ID)
	if err != nil {
		t.Fatalf("Clarification: %v", err)
	}
	if clar != nil {
		t.Error("a foreign subject got another user's clarification card")
	}
	if _, _, err := s.CandidateHits(ctx, other, id, 0); err == nil {
		t.Error("CandidateHits gave a foreign subject another user's hits")
	}
	got, total, err := s.Sources(ctx, other, msg.ID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if total != 0 {
		t.Errorf("total for a foreign subject = %d, want 0", total)
	}
	if len(got) != 0 {
		t.Errorf("Sources gave a foreign subject %d sources, want none", len(got))
	}

	// And the rightful owner still gets everything, unaffected by the checks
	clar, err = s.Clarification(ctx, testSubject, msg.ID)
	if err != nil || clar == nil {
		t.Fatalf("Clarification for the owner: %+v, %v", clar, err)
	}
	if _, hits, err := s.CandidateHits(ctx, testSubject, id, 0); err != nil || len(hits) != 1 {
		t.Errorf("CandidateHits for the owner: hits=%v, err=%v", hits, err)
	}
	if got, total, err := s.Sources(ctx, testSubject, msg.ID); err != nil || len(got) != 1 || total != 1 {
		t.Errorf("Sources for the owner: got=%v, err=%v", got, err)
	}
}

func TestLinkChoiceRefusesToPairAMessageAndAClarificationFromDifferentThreads(t *testing.T) {
	// No handler is trusted to have paired two browser-supplied ids
	// correctly: a mismatched pair would silently attribute an answer to a
	// card it never came from.
	s, ctx, threadA, _ := newThreadStore(t)
	threadB, err := s.Create(ctx, testSubject, "andere frage")
	if err != nil {
		t.Fatalf("create second thread: %v", err)
	}
	msgA, _ := s.AddQuestion(ctx, threadA, "ba", "en", "frage", 0)
	id, err := s.Clarify(ctx, msgA.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	msgB, _ := s.AddQuestion(ctx, threadB.ID, "ba", "en", "andere frage", 0)

	// When: msgB belongs to threadB, id's clarification belongs to threadA
	err = s.LinkChoice(ctx, testSubject, msgB.ID, id, 0)

	// Then: refused, and nothing was linked
	if err == nil {
		t.Fatal("LinkChoice linked a message to a clarification from another thread")
	}
	msgs, err := s.Messages(ctx, testSubject, threadB.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if msgs[0].FromCandidateIdx != -1 {
		t.Errorf("FromCandidateIdx = %d, want -1 — the cross-thread link must not have taken effect", msgs[0].FromCandidateIdx)
	}
}

func TestClarifyRemembersThatTheCardWasTooBroadToShow(t *testing.T) {
	// A turn that ended by asking for a narrower question stores the same
	// record as a card — the repositories it matched, in order — and has to
	// come back as that panel after a reload rather than as a card offering
	// twenty buttons.
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "how is retry done?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	if _, err := s.Clarify(ctx, msg.ID, ask.Clarification{
		TooBroad: true,
		Candidates: []ask.Candidate{
			{Repo: "peeq", Branch: "master"},
			{Repo: "loom", Branch: "main"},
			{Repo: "ledger", Branch: "main"},
			{Repo: "gateway", Branch: "main"},
			{Repo: "ingest", Branch: "main"},
		},
	}); err != nil {
		t.Fatalf("clarify: %v", err)
	}

	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if msgs[0].Clarification == nil {
		t.Fatal("the stored turn must carry what it asked")
	}
	if !msgs[0].Clarification.TooBroad {
		t.Error("a too-broad turn must not come back as an ordinary card")
	}
	if len(msgs[0].Clarification.Candidates) != 5 {
		t.Errorf("got %d repositories, want all five", len(msgs[0].Clarification.Candidates))
	}
}

func TestAnOrdinaryCardIsNotTooBroad(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	if _, err := s.Clarify(ctx, msg.ID, twoCandidateClarification()); err != nil {
		t.Fatalf("clarify: %v", err)
	}
	msgs, _ := s.Messages(ctx, testSubject, threadID)
	if msgs[0].Clarification.TooBroad {
		t.Error("two candidates are a card, not a corpus too wide to ask about")
	}
}

func TestATurnResumedFromTheTooBroadPanelSaysWhatItNarrowedTo(t *testing.T) {
	// The card collapses to "Chosen: <title>". The panel has no titles, so it
	// collapses to the repositories that were picked — and after a reload
	// those have to come from the record, not from a click the page no longer
	// remembers.
	s, ctx, threadID, _ := newThreadStore(t)
	asked, _ := s.AddQuestion(ctx, threadID, "ba", "en", "how is retry done?", 0)
	clarID, err := s.Clarify(ctx, asked.ID, ask.Clarification{
		TooBroad:   true,
		Candidates: []ask.Candidate{{Repo: "peeq", Branch: "master"}, {Repo: "loom", Branch: "main"}},
	})
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}

	answered, _ := s.AddQuestion(ctx, threadID, "ba", "en", "how is retry done?", 0)
	if err := s.SetScope(ctx, answered.ID, ask.Scope{Known: []string{"peeq", "loom"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	// -1 is the column's "no candidate": the answer came from the panel as a
	// whole, not from a row on it.
	if err := s.LinkChoice(ctx, testSubject, answered.ID, clarID, -1); err != nil {
		t.Fatalf("link choice: %v", err)
	}

	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	got := msgs[1].NarrowedTo
	if len(got) != 2 || got[0] != "peeq" || got[1] != "loom" {
		t.Errorf("narrowed to %v, want both repositories the reader picked", got)
	}
	if len(msgs[0].NarrowedTo) != 0 {
		t.Error("the turn that ASKED narrowed to nothing; only the one that answered did")
	}
}

func TestATurnResumedFromACardNarrowsToNothingItHasToRepeat(t *testing.T) {
	// A card already says what was chosen, by title, on the card itself. The
	// answering turn repeating it as a repository list would put the same
	// decision in the record twice, in two shapes.
	s, ctx, threadID, _ := newThreadStore(t)
	asked, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	clarID, err := s.Clarify(ctx, asked.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	answered, _ := s.AddQuestion(ctx, threadID, "ba", "en", "frage", 0)
	if err := s.SetScope(ctx, answered.ID, ask.Scope{Known: []string{"peeq"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	if err := s.LinkChoice(ctx, testSubject, answered.ID, clarID, 1); err != nil {
		t.Fatalf("link choice: %v", err)
	}

	msgs, _ := s.Messages(ctx, testSubject, threadID)
	if len(msgs[1].NarrowedTo) != 0 {
		t.Errorf("narrowed to %v, want nothing — the card names the choice itself", msgs[1].NarrowedTo)
	}
}
