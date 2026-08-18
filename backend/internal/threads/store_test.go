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

	got, err := s.Create(context.Background(), "anna", "Wie wird die Teaser-Mail verschickt?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.Title == "" {
		t.Fatal("empty title; the sidebar entry must be there the moment the question is sent")
	}
	if !strings.HasPrefix(got.Title, "Wie wird die Teaser-Mail") {
		t.Errorf("title = %q, want the first words of the question", got.Title)
	}
}

func TestSetTitle_anEmptyModelTitleLeavesThePlaceholderStanding(t *testing.T) {
	// A title call that returns nothing is not a failure anyone needs to see.
	// Overwriting the placeholder with "" would blank the sidebar entry.
	s := NewStore(threadDB(t))
	th, _ := s.Create(context.Background(), "anna", "Wie laeuft der Versand?")

	if err := s.SetTitle(context.Background(), th.ID, "   "); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(context.Background(), "anna")
	if list[0].Title == "" {
		t.Error("the placeholder was overwritten with an empty title")
	}
}

func TestFinish_storesTheAnswerWithItsEvidence(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, err := s.AddQuestion(ctx, th.ID, "ba", "Wie?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	err = s.Finish(ctx, m.ID, "So laeuft es [1].", []ask.Citation{
		{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 9},
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Answer != "So laeuft es [1]." {
		t.Fatalf("messages = %+v", msgs)
	}
	if len(msgs[0].Citations) != 1 || msgs[0].Citations[0].Branch != "master" {
		t.Errorf("citations = %+v, want the branch kept — a forge URL without it may 404", msgs[0].Citations)
	}
}

func TestFail_keepsTheQuestionInTheRecord(t *testing.T) {
	// A turn that broke still happened. Dropping the question would leave the
	// reader wondering what they asked.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "Wie?")

	if err := s.Fail(ctx, m.ID, "das Modell antwortete nicht"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	msgs, _ := s.Messages(ctx, "anna", th.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want the failed turn kept", msgs)
	}
	if msgs[0].Question != "Wie?" || msgs[0].Error == "" {
		t.Errorf("message = %+v, want the question plus the failure", msgs[0])
	}
}

func TestMessages_anotherUsersThreadIsNotReadable(t *testing.T) {
	// The subject is part of the query, not a check afterwards: a slip here
	// hands over someone else's conversation.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "Wie?")
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
	th, _ := s.Create(ctx, "anna", "Erste Frage?")
	first, _ := s.AddQuestion(ctx, th.ID, "ba", "Erste Frage?")
	_ = s.Finish(ctx, first.ID, "Erste Antwort.", nil)
	second, err := s.AddQuestion(ctx, th.ID, "dev", "Und als Dev?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	_ = s.Finish(ctx, second.ID, "Zweite Antwort.", nil)

	msgs, _ := s.Messages(ctx, "anna", th.ID)

	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both turns", len(msgs))
	}
	if msgs[0].Answer != "Erste Antwort." {
		t.Errorf("the first answer changed: %q", msgs[0].Answer)
	}
	if msgs[0].Audience != "ba" || msgs[1].Audience != "dev" {
		t.Errorf("audiences = %q/%q, want them per message", msgs[0].Audience, msgs[1].Audience)
	}
}

func TestClarifyStoresTheCardAndServesItBackWithTheThread(t *testing.T) {
	// Given a question that ended by asking
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "wie ist die Anmeldung geloest?")
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	// When
	id, err := s.Clarify(ctx, msg.ID, ask.Clarification{
		Understanding: ask.Understanding{CodeTerms: []string{"session", "oidc"}},
		Candidates: []ask.Candidate{
			{Repo: "peeq", Branch: "master", ModuleKey: "backend/internal/auth", Title: "Anmeldung in peeq", Summary: "Sitzungen ueber Cookies.", Hits: []retrieve.Hit{{ChunkID: 7}}},
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

func TestChoosingASecondCandidateLeavesTheFirstTurnUntouched(t *testing.T) {
	// The thread is a record. Two choices are two turns, and neither
	// overwrites the card or the other's answer.
	s, ctx, threadID, _ := newThreadStore(t)
	first, _ := s.AddQuestion(ctx, threadID, "ba", "frage")
	id, err := s.Clarify(ctx, first.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}

	a, _ := s.AddQuestion(ctx, threadID, "ba", "frage")
	if err := s.LinkChoice(ctx, testSubject, a.ID, id, 0); err != nil {
		t.Fatalf("link first choice: %v", err)
	}
	b, _ := s.AddQuestion(ctx, threadID, "ba", "frage")
	if err := s.LinkChoice(ctx, testSubject, b.ID, id, 1); err != nil {
		t.Fatalf("link second choice: %v", err)
	}

	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 — the card and both answers", len(msgs))
	}
	if msgs[1].FromCandidateIdx != 0 || msgs[2].FromCandidateIdx != 1 {
		t.Errorf("choices recorded as %d and %d", msgs[1].FromCandidateIdx, msgs[2].FromCandidateIdx)
	}
}

func TestSourcesComeBackAndAVanishedChunkIsSimplyMissing(t *testing.T) {
	// Given an answer stored with two sources, one of whose chunks a re-index
	// then removes
	s, ctx, threadID, db := newThreadStore(t)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "frage")
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
	got, err := s.Sources(ctx, testSubject, msg.ID)

	// Then
	if err != nil {
		t.Fatalf("sources: %v", err)
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
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "frage")
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
	got, err := s.Sources(ctx, other, msg.ID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
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
	if got, err := s.Sources(ctx, testSubject, msg.ID); err != nil || len(got) != 1 {
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
	msgA, _ := s.AddQuestion(ctx, threadA, "ba", "frage")
	id, err := s.Clarify(ctx, msgA.ID, twoCandidateClarification())
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	msgB, _ := s.AddQuestion(ctx, threadB.ID, "ba", "andere frage")

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
