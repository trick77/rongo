package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateSessionFromClaims_mintsResolvableSession(t *testing.T) {
	// Given
	svc := newService(t)
	claims := Claims{Subject: "authelia-sub-1", Email: "jan@example.com", Groups: []string{"Rongo"}}

	// When
	token, expiresAt, user, err := svc.CreateSessionFromClaims(claims, "Rongo")

	// Then
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() err = %v", err)
	}
	if user.Subject != "authelia-sub-1" {
		t.Errorf("Subject = %q, want %q", user.Subject, "authelia-sub-1")
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true for a member of the admin group")
	}
	if got, ok := svc.UserByToken(token); !ok || got.ID != user.ID {
		t.Errorf("UserByToken() = (%+v, %v), want the user just created", got, ok)
	}
	if until := time.Until(expiresAt); until <= 0 || until > SessionTTL {
		t.Errorf("expiresAt is %v away, want within (0, %v]", until, SessionTTL)
	}
}

// The same subject signing in twice is one user, not two: threads are keyed by
// subject, so a second row would hide the first login's history.
func TestCreateSessionFromClaims_reusesUserForSameSubject(t *testing.T) {
	// Given
	svc := newService(t)
	claims := Claims{Subject: "authelia-sub-1", Email: "jan@example.com"}
	_, _, first, err := svc.CreateSessionFromClaims(claims, "")
	if err != nil {
		t.Fatalf("first CreateSessionFromClaims() err = %v", err)
	}

	// When
	_, _, second, err := svc.CreateSessionFromClaims(claims, "")

	// Then
	if err != nil {
		t.Fatalf("second CreateSessionFromClaims() err = %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second login user id = %d, want %d", second.ID, first.ID)
	}
}

func TestCreateSessionFromClaims_deniesAdminWithoutTheGroup(t *testing.T) {
	// Given
	svc := newService(t)
	claims := Claims{Subject: "sub-2", Groups: []string{"Loom", "Tools"}}

	// When
	_, _, user, err := svc.CreateSessionFromClaims(claims, "Rongo")

	// Then
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() err = %v", err)
	}
	if user.IsAdmin {
		t.Error("IsAdmin = true, want false for a user outside the admin group")
	}
}

// An empty admin group means the check is off, not that nobody is admin — the
// opposite reading would lock every deployment out of the admin surface by
// default.
func TestCreateSessionFromClaims_emptyAdminGroupGrantsAdmin(t *testing.T) {
	// Given
	svc := newService(t)
	claims := Claims{Subject: "sub-3"}

	// When
	_, _, user, err := svc.CreateSessionFromClaims(claims, "")

	// Then
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() err = %v", err)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true when no admin group is configured")
	}
}

func TestCreateSessionFromClaims_rejectsClaimsWithoutSubject(t *testing.T) {
	// Given
	svc := newService(t)

	// When
	_, _, _, err := svc.CreateSessionFromClaims(Claims{Email: "jan@example.com"}, "")

	// Then
	if err == nil {
		t.Fatal("CreateSessionFromClaims() err = nil, want an error for empty subject")
	}
}

// A clear that differs from SetSessionCookie in Path or Secure leaves the
// original cookie in the browser and the user signed in.
func TestClearSessionCookie_matchesTheAttributesItClears(t *testing.T) {
	// Given
	const secure = true
	set := httptest.NewRecorder()
	SetSessionCookie(set, "token", secure, time.Hour)
	want := set.Result().Cookies()[0]

	// When
	rec := httptest.NewRecorder()
	ClearSessionCookie(rec, secure)

	// Then
	got := rec.Result().Cookies()[0]
	if got.Name != want.Name || got.Path != want.Path || got.Secure != want.Secure ||
		got.HttpOnly != want.HttpOnly || got.SameSite != want.SameSite {
		t.Fatalf("cleared cookie = %+v, want the attributes of %+v", got, want)
	}
	if got.Value != "" || got.MaxAge >= 0 {
		t.Errorf("cleared cookie value = %q, MaxAge = %d; want empty and negative", got.Value, got.MaxAge)
	}
}
