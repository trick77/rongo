package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewService(db, "dev", "")
}

func TestCreateSession_returnsTokenResolvableToUser(t *testing.T) {
	// Given
	svc := newService(t)
	user, err := svc.UpsertUser("dev-user", "dev@example.invalid", true)
	if err != nil {
		t.Fatalf("UpsertUser() err = %v", err)
	}

	// When
	token, err := svc.CreateSession(user.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}
	got, ok := svc.UserByToken(token)

	// Then
	if !ok {
		t.Fatal("UserByToken() ok = false, want true")
	}
	if got.ID != user.ID {
		t.Errorf("user id = %d, want %d", got.ID, user.ID)
	}
}

func TestCreateSession_storesOnlyTheHash(t *testing.T) {
	// Given: a database copy must not be replayable as a login.
	svc := newService(t)
	user, _ := svc.UpsertUser("dev-user", "dev@example.invalid", true)

	// When
	token, err := svc.CreateSession(user.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}

	// Then: the stored value must be exactly hashToken(token) — not merely
	// "not equal to the raw token", which base64, a truncated hash, a
	// constant, or an empty string would all also satisfy.
	var storedHash string
	if err := svc.db.QueryRow(
		`SELECT token_hash FROM sessions WHERE user_id = ?`, user.ID,
	).Scan(&storedHash); err != nil {
		t.Fatalf("query: %v", err)
	}
	if want := hashToken(token); storedHash != want {
		t.Errorf("token_hash = %q, want %q (sha256 of the raw token)", storedHash, want)
	}
}

func TestUserByToken_rejectsExpiredSession(t *testing.T) {
	// Given
	svc := newService(t)
	user, _ := svc.UpsertUser("dev-user", "dev@example.invalid", true)
	token, _ := svc.CreateSession(user.ID, -time.Minute) // already expired

	// When
	_, ok := svc.UserByToken(token)

	// Then
	if ok {
		t.Error("UserByToken() ok = true for an expired session, want false")
	}
}

func TestUserByToken_rejectsUnknownToken(t *testing.T) {
	svc := newService(t)

	_, ok := svc.UserByToken("not-a-real-token")

	if ok {
		t.Error("UserByToken() ok = true for an unknown token, want false")
	}
}
