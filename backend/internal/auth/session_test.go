package auth

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/store/storetest"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db := storetest.Open(t, 1536)
	return NewService(db, "dev", "")
}

func TestCreateSession_returnsTokenResolvableToUser(t *testing.T) {
	// Given
	svc := newService(t)
	user, err := svc.UpsertUser(context.Background(), "dev-user", "dev@example.invalid", true)
	if err != nil {
		t.Fatalf("UpsertUser() err = %v", err)
	}

	// When
	token, err := svc.CreateSession(context.Background(), user.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}
	got, ok := svc.UserByToken(context.Background(), token)

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
	user, _ := svc.UpsertUser(context.Background(), "dev-user", "dev@example.invalid", true)

	// When
	token, err := svc.CreateSession(context.Background(), user.ID, time.Hour)
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
	user, _ := svc.UpsertUser(context.Background(), "dev-user", "dev@example.invalid", true)
	token, _ := svc.CreateSession(context.Background(), user.ID, -time.Minute) // already expired

	// When
	_, ok := svc.UserByToken(context.Background(), token)

	// Then
	if ok {
		t.Error("UserByToken() ok = true for an expired session, want false")
	}
}

func TestUserByToken_rejectsUnknownToken(t *testing.T) {
	svc := newService(t)

	_, ok := svc.UserByToken(context.Background(), "not-a-real-token")

	if ok {
		t.Error("UserByToken() ok = true for an unknown token, want false")
	}
}

func TestAnAbandonedRequestStopsItsSessionQueries(t *testing.T) {
	// Given a session, and a request whose client has gone away
	svc := newService(t)
	user, _ := svc.UpsertUser(context.Background(), "dev-user", "dev@example.invalid", true)
	token, _ := svc.CreateSession(context.Background(), user.ID, time.Minute)
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	// When / Then nothing is read or written on its behalf
	if _, ok := svc.UserByToken(gone, token); ok {
		t.Error("UserByToken() resolved a session for a cancelled request")
	}
	if _, err := svc.UpsertUser(gone, "late", "", false); err == nil {
		t.Error("UpsertUser() wrote for a cancelled request")
	}
	if _, err := svc.CreateSession(gone, user.ID, time.Minute); err == nil {
		t.Error("CreateSession() wrote for a cancelled request")
	}
	if err := svc.DeleteSession(gone, token); err == nil {
		t.Error("DeleteSession() wrote for a cancelled request")
	}
	if _, ok := svc.UserByToken(context.Background(), token); !ok {
		t.Error("the session is gone, want it untouched by the cancelled delete")
	}
}

func TestDeleteExpiredSessions_takesOnlyWhatHasExpired(t *testing.T) {
	svc := newService(t)
	u, err := svc.UpsertUser(context.Background(), "jan", "jan@example.invalid", true)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	live, err := svc.CreateSession(context.Background(), u.ID, time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), u.ID, time.Second); err != nil {
		t.Fatalf("create: %v", err)
	}

	n, err := svc.DeleteExpiredSessions(context.Background(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}

	if n != 1 {
		t.Errorf("deleted %d sessions, want the one that expired", n)
	}
	if _, ok := svc.UserByToken(context.Background(), live); !ok {
		t.Error("the live session went with it")
	}
}
