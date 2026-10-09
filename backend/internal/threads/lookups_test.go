package threads

import (
	"errors"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestMessageOrdinal_isThePositionOrNothingForAnotherReadersTurn(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	second := answeredTurn(t, s, th, "And then?", "Then.")

	ord, found, err := s.MessageOrdinal(ctx, testSubject, second)
	if err != nil || !found || ord != 1 {
		t.Fatalf("MessageOrdinal = %d, %v, %v; want 1, true", ord, found, err)
	}
	if _, found, err := s.MessageOrdinal(ctx, "bruno", second); err != nil || found {
		t.Fatalf("another reader's turn: found %v, err %v", found, err)
	}
	if _, found, err := s.MessageOrdinal(ctx, testSubject, second+9999); err != nil || found {
		t.Fatalf("no such turn: found %v, err %v", found, err)
	}
}

func TestSharedTitle_isTheTitleOfALiveLinkAndNoShareForTheRest(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "How?", "So.")
	if _, err := s.Rename(ctx, testSubject, th, "The flow"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if title, err := s.SharedTitle(ctx, sh.Token); err != nil || title != "The flow" {
		t.Fatalf("SharedTitle = %q, %v", title, err)
	}
	if _, err := s.SharedTitle(ctx, "nGVwbmZ0aGF0aXNub3RyZWFs"); !errors.Is(err, ErrNoShare) {
		t.Fatalf("unknown token = %v, want ErrNoShare", err)
	}
	if _, err := s.RevokeShare(ctx, testSubject, th); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.SharedTitle(ctx, sh.Token); !errors.Is(err, ErrNoShare) {
		t.Fatalf("revoked token = %v, want ErrNoShare", err)
	}
}

func TestCitedBy_theOwnerOpensWhatTheirTurnsCiteAndNobodyElseDoes(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "What changed?", "This [1] and that [2].",
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", SHA: "aaa1111", Kind: ask.SourceCommit,
			Subject: "Newest", CommittedAt: "2026-09-17T10:00:00Z"},
		ask.Citation{Marker: 2, Repo: "rongo", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 2, SHA: "deadbeef"})

	if ok, err := s.CommitCitedBy(ctx, testSubject, "rongo", "aaa1111"); err != nil || !ok {
		t.Errorf("the owner's cited commit: %v, %v; want true", ok, err)
	}
	if ok, err := s.CitedBy(ctx, testSubject, "rongo", "a.go", "deadbeef"); err != nil || !ok {
		t.Errorf("the owner's cited file: %v, %v; want true", ok, err)
	}
	if ok, _ := s.CommitCitedBy(ctx, "bruno", "rongo", "aaa1111"); ok {
		t.Error("another reader opens the owner's cited commit")
	}
	if ok, _ := s.CitedBy(ctx, testSubject, "rongo", "", "aaa1111"); ok {
		t.Error("a commit citation opens as a file")
	}
	if ok, _ := s.CommitCitedBy(ctx, testSubject, "rongo", "deadbeef"); ok {
		t.Error("a file citation's commit opens as a commit view")
	}
}
