package threads

import (
	"errors"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

// answeredTurn adds a question and finishes it, which is the only shape of
// turn a share is allowed to be frozen at.
func answeredTurn(t *testing.T, s *Store, threadID int64, question, answer string, cites ...ask.Citation) int64 {
	t.Helper()
	m, err := s.AddQuestion(t.Context(), threadID, "ba", "en", question, 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(t.Context(), m.ID, answer, cites); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return m.ID
}

func TestShare_mintsALinkAtTheNewestTurn(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	last := answeredTurn(t, s, th, "How?", "So.")

	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if sh.UpToMessageID != last {
		t.Errorf("ceiling = %d, want the newest message %d", sh.UpToMessageID, last)
	}
	// 16 bytes of randomness, base64url without padding.
	if len(sh.Token) != 22 {
		t.Errorf("token %q is %d chars, want 22", sh.Token, len(sh.Token))
	}
	if sh.Path != SharePath+sh.Token {
		t.Errorf("path = %q, want %q", sh.Path, SharePath+sh.Token)
	}
	if sh.Turns != 1 || sh.Newer != 0 {
		t.Errorf("turns/newer = %d/%d, want 1/0", sh.Turns, sh.Newer)
	}
}

func TestShare_aThreadWithNoFinishedTurnIsNotShareable(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	// Asked but never answered: the row exists from the moment the question is
	// sent, and freezing here would keep half a sentence for good.
	if _, err := s.AddQuestion(ctx, th, "ba", "en", "How?", 0); err != nil {
		t.Fatalf("add question: %v", err)
	}

	if _, err := s.Share(ctx, testSubject, th); !errors.Is(err, ErrUnfinished) {
		t.Fatalf("share of an unfinished turn = %v, want ErrUnfinished", err)
	}
}

func TestShare_anEmptyThreadIsNotShareable(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)

	if _, err := s.Share(ctx, testSubject, th); !errors.Is(err, ErrNoShare) {
		t.Fatalf("share of an empty thread = %v, want ErrNoShare", err)
	}
}

func TestShare_anotherUsersThreadCannotBeShared(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")

	if _, err := s.Share(ctx, "bruno", th); !errors.Is(err, ErrNoShare) {
		t.Fatalf("share of another user's thread = %v, want ErrNoShare", err)
	}
}

func TestSharedThread_stopsAtTheCeiling(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	// Asked after the link was made: on the record, not on the link.
	answeredTurn(t, s, th, "And then?", "Then this.")

	_, msgs, err := s.SharedThread(ctx, sh.Token)
	if err != nil {
		t.Fatalf("shared thread: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("shared thread has %d turns, want only the one below the ceiling", len(msgs))
	}
	if msgs[0].Question != "How?" {
		t.Errorf("shared turn = %q, want the one that was on the link", msgs[0].Question)
	}
}

func TestRaiseShare_takesInTheTurnsAskedSinceAndKeepsTheLink(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	first, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	answeredTurn(t, s, th, "And then?", "Then this.")

	// Before: the thread has moved on and the link has not.
	behind, err := s.ShareFor(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share for: %v", err)
	}
	if behind.Newer != 1 {
		t.Errorf("newer = %d, want the one turn asked since", behind.Newer)
	}

	raised, err := s.RaiseShare(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	// The link already sent out has to keep working: that is the whole point
	// of Update rather than "share again".
	if raised.Token != first.Token {
		t.Errorf("token changed on update: %q -> %q", first.Token, raised.Token)
	}
	if raised.Newer != 0 || raised.Turns != 2 {
		t.Errorf("turns/newer = %d/%d, want 2/0", raised.Turns, raised.Newer)
	}
	_, msgs, err := s.SharedThread(ctx, raised.Token)
	if err != nil {
		t.Fatalf("shared thread: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("shared thread has %d turns after the update, want 2", len(msgs))
	}
}

func TestRevokeShare_theLinkStopsAnsweringAndComesBackTheSame(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}

	revoked, err := s.RevokeShare(ctx, testSubject, th)
	if err != nil || !revoked {
		t.Fatalf("revoke = %v, %v", revoked, err)
	}
	if _, _, err := s.SharedThread(ctx, sh.Token); !errors.Is(err, ErrNoShare) {
		t.Errorf("revoked link reads as %v, want ErrNoShare", err)
	}
	if _, err := s.ShareFor(ctx, testSubject, th); !errors.Is(err, ErrNoShare) {
		t.Errorf("revoked link still listed on the thread: %v", err)
	}

	// Sharing again hands back the SAME link: the old one may be in somebody's
	// inbox, and a second token would leave it dead with nobody told.
	again, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("re-share: %v", err)
	}
	if again.Token != sh.Token {
		t.Errorf("re-share minted a new token: %q -> %q", sh.Token, again.Token)
	}
}

func TestRevokeShare_revokingTwiceReportsNothingToRevoke(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	if _, err := s.Share(ctx, testSubject, th); err != nil {
		t.Fatalf("share: %v", err)
	}
	if _, err := s.RevokeShare(ctx, testSubject, th); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	revoked, err := s.RevokeShare(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if revoked {
		t.Error("second revoke reported a link taken back; there was none")
	}
}

func TestSharedThread_anUnknownTokenIsTheSameAnswerAsARevokedOne(t *testing.T) {
	s, ctx, _, _ := newThreadStore(t)

	if _, _, err := s.SharedThread(ctx, "nGVwbmZ0aGF0aXNub3RyZWFs"); !errors.Is(err, ErrNoShare) {
		t.Fatalf("unknown token = %v, want ErrNoShare", err)
	}
}

func TestSharedThread_deletingTheThreadTakesTheLinkWithIt(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}

	if _, err := s.Delete(ctx, testSubject, th); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := s.SharedThread(ctx, sh.Token); !errors.Is(err, ErrNoShare) {
		t.Errorf("link to a deleted thread reads as %v, want ErrNoShare", err)
	}
}

func TestSharedCitation_servesOnlyWhatTheLinkCites(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So [1].",
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 2, SHA: "abc"})
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}

	cited, err := s.SharedCitation(ctx, sh.Token, "rongo", "a.go", "abc")
	if err != nil || !cited {
		t.Errorf("the cited file is not served: %v, %v", cited, err)
	}
	// The whole corpus is one query away from /api/source; the share must not
	// be a door to it.
	other, err := s.SharedCitation(ctx, sh.Token, "rongo", "secrets.go", "abc")
	if err != nil {
		t.Fatalf("shared citation: %v", err)
	}
	if other {
		t.Error("a file the link never cited is served through it")
	}
	// Same file, a different commit, is a different file.
	moved, err := s.SharedCitation(ctx, sh.Token, "rongo", "a.go", "def")
	if err != nil {
		t.Fatalf("shared citation: %v", err)
	}
	if moved {
		t.Error("the file is served at a commit the link never cited")
	}
}

func TestSharedCitation_aTurnAboveTheCeilingCitesNothing(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	// Cited by a turn asked after the link was made.
	answeredTurn(t, s, th, "And then?", "Then this [1].",
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", Path: "later.go", SHA: "abc"})

	cited, err := s.SharedCitation(ctx, sh.Token, "rongo", "later.go", "abc")
	if err != nil {
		t.Fatalf("shared citation: %v", err)
	}
	if cited {
		t.Error("a file cited above the ceiling is served through the link")
	}
}

func TestSharedCitation_arevokedLinkCitesNothing(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So [1].",
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", Path: "a.go", SHA: "abc"})
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if _, err := s.RevokeShare(ctx, testSubject, th); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	cited, err := s.SharedCitation(ctx, sh.Token, "rongo", "a.go", "abc")
	if err != nil {
		t.Fatalf("shared citation: %v", err)
	}
	if cited {
		t.Error("a revoked link still opens the files it used to cite")
	}
}

func TestShares_listsOnlyLiveLinksAndOnlyThisReadersOwn(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	if _, err := s.Share(ctx, testSubject, th); err != nil {
		t.Fatalf("share: %v", err)
	}
	// A second thread, shared then taken back.
	gone, err := s.Create(ctx, testSubject, "andere frage")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	answeredTurn(t, s, gone.ID, "How else?", "Like this.")
	if _, err := s.Share(ctx, testSubject, gone.ID); err != nil {
		t.Fatalf("share: %v", err)
	}
	if _, err := s.RevokeShare(ctx, testSubject, gone.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	list, err := s.Shares(ctx, testSubject)
	if err != nil {
		t.Fatalf("shares: %v", err)
	}
	if len(list) != 1 || list[0].ThreadID != th {
		t.Fatalf("shares = %+v, want only the live link on thread %d", list, th)
	}
	other, err := s.Shares(ctx, "bruno")
	if err != nil {
		t.Fatalf("shares: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("another reader sees %d of these links, want none", len(other))
	}
}

func TestList_marksTheThreadsThatAreLiveOnALink(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	quiet, err := s.Create(ctx, testSubject, "nicht geteilt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Share(ctx, testSubject, th); err != nil {
		t.Fatalf("share: %v", err)
	}

	list, err := s.List(ctx, testSubject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range list {
		switch row.ID {
		case th:
			if !row.Shared {
				t.Error("the shared thread is not marked on the rail")
			}
		case quiet.ID:
			if row.Shared {
				t.Error("a thread with no link is marked as shared")
			}
		}
	}
}

// Every share method reads or writes, so every one of them has a branch for a
// database that will not answer. Those branches decide whether an operator
// sees the real reason in the log or a caller sees an empty list and believes
// it — a shut database is the cheapest way to reach all of them at once.
func TestShareStore_everyCallReportsABrokenDatabase(t *testing.T) {
	s, ctx, th, db := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := map[string]func() error{
		"Share":          func() error { _, err := s.Share(ctx, testSubject, th); return err },
		"RaiseShare":     func() error { _, err := s.RaiseShare(ctx, testSubject, th); return err },
		"RevokeShare":    func() error { _, err := s.RevokeShare(ctx, testSubject, th); return err },
		"ShareFor":       func() error { _, err := s.ShareFor(ctx, testSubject, th); return err },
		"Shares":         func() error { _, err := s.Shares(ctx, testSubject); return err },
		"SharedIDs":      func() error { _, err := s.SharedIDs(ctx, testSubject); return err },
		"SharedThread":   func() error { _, _, err := s.SharedThread(ctx, sh.Token); return err },
		"SharedCitation": func() error { _, err := s.SharedCitation(ctx, sh.Token, "r", "p", "s"); return err },
		"List":           func() error { _, err := s.List(ctx, testSubject); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s returned no error against a closed database", name)
		}
	}
}

func TestRaiseShare_aThreadWithNoLinkHasNothingToRaise(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")

	if _, err := s.RaiseShare(ctx, testSubject, th); !errors.Is(err, ErrNoShare) {
		t.Fatalf("raise without a link = %v, want ErrNoShare", err)
	}
}

func TestRaiseShare_isRefusedWhileTheTurnIsStillBeingWritten(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	if _, err := s.Share(ctx, testSubject, th); err != nil {
		t.Fatalf("share: %v", err)
	}
	if _, err := s.AddQuestion(ctx, th, "ba", "en", "And then?", 0); err != nil {
		t.Fatalf("add question: %v", err)
	}

	if _, err := s.RaiseShare(ctx, testSubject, th); !errors.Is(err, ErrUnfinished) {
		t.Fatalf("raise over an unfinished turn = %v, want ErrUnfinished", err)
	}
}

func TestRaiseShare_anEmptyThreadHasNothingToRaise(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)

	if _, err := s.RaiseShare(ctx, testSubject, th); !errors.Is(err, ErrNoShare) {
		t.Fatalf("raise on an empty thread = %v, want ErrNoShare", err)
	}
}

func TestShareFor_aThreadWithNoLinkSaysSo(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")

	if _, err := s.ShareFor(ctx, testSubject, th); !errors.Is(err, ErrNoShare) {
		t.Fatalf("share for an unshared thread = %v, want ErrNoShare", err)
	}
}

func TestShare_aTurnThatEndedInACardCanBeShared(t *testing.T) {
	// A card IS how that turn ended, so the thread is not mid-stream: freezing
	// here keeps a complete record, and the reader of the link sees the card
	// as the record it is.
	s, ctx, th, _ := newThreadStore(t)
	m, err := s.AddQuestion(ctx, th, "ba", "en", "Where does the budget live?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if _, err := s.Clarify(ctx, m.ID, twoCandidateClarification()); err != nil {
		t.Fatalf("clarify: %v", err)
	}

	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share of a carded turn: %v", err)
	}
	_, msgs, err := s.SharedThread(ctx, sh.Token)
	if err != nil {
		t.Fatalf("shared thread: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Clarification == nil {
		t.Fatalf("the card did not travel with the link: %+v", msgs)
	}
	if len(msgs[0].Clarification.Candidates) != 2 {
		t.Errorf("candidates = %d, want the two the card offered", len(msgs[0].Clarification.Candidates))
	}
}

func TestShare_aTurnThatFailedCanBeShared(t *testing.T) {
	// A failure is how that turn ended, and the record keeps it: a shared
	// thread that quietly dropped the turns that went wrong is a different
	// thread from the one that was shared.
	s, ctx, th, _ := newThreadStore(t)
	m, err := s.AddQuestion(ctx, th, "ba", "en", "How?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Fail(ctx, m.ID, "the upstream did not answer"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share of a failed turn: %v", err)
	}
	_, msgs, err := s.SharedThread(ctx, sh.Token)
	if err != nil {
		t.Fatalf("shared thread: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Error == "" {
		t.Fatalf("the failure did not travel with the link: %+v", msgs)
	}
}
