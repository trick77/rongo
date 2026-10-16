package auth

import (
	"context"
	"testing"
)

func TestAdmitClaims_admitsAMemberOfTheGroupAsAdmin(t *testing.T) {
	svc := newService(t)
	claims := Claims{Subject: "authelia-sub-1", Email: "jan@example.com", Groups: []string{"Rongo"}}

	svc.SetOIDCAdminGroup("Rongo")

	user, err := svc.AdmitClaims(context.Background(), claims)

	if err != nil {
		t.Fatalf("AdmitClaims() err = %v", err)
	}
	if user.Subject != "authelia-sub-1" || user.Email != "jan@example.com" {
		t.Errorf("user = %+v, want the subject and email of the claims", user)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true for a member of the admin group")
	}
}

// The same subject signing in twice is one user, not two: threads are keyed by
// subject, so a second row would hide the first login's history.
func TestAdmitClaims_reusesUserForSameSubject(t *testing.T) {
	svc := newService(t)
	claims := Claims{Subject: "authelia-sub-1", Email: "jan@example.com"}

	first, err := svc.AdmitClaims(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.AdmitClaims(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID {
		t.Errorf("ids = %d and %d, want the same user", first.ID, second.ID)
	}
}

func TestAdmitClaims_deniesAdminWithoutTheGroup(t *testing.T) {
	svc := newService(t)
	claims := Claims{Subject: "authelia-sub-2", Groups: []string{"Readers"}}
	svc.SetOIDCAdminGroup("Rongo")

	user, err := svc.AdmitClaims(context.Background(), claims)

	if err != nil {
		t.Fatalf("AdmitClaims() err = %v", err)
	}
	if user.IsAdmin {
		t.Error("IsAdmin = true, want false outside the admin group")
	}
}

// Empty means no group check: every user the provider let through is admin.
func TestAdmitClaims_emptyAdminGroupGrantsAdmin(t *testing.T) {
	svc := newService(t)

	user, err := svc.AdmitClaims(context.Background(), Claims{Subject: "authelia-sub-3"})

	if err != nil {
		t.Fatalf("AdmitClaims() err = %v", err)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true with no admin group configured")
	}
}

func TestAdmitClaims_rejectsClaimsWithoutSubject(t *testing.T) {
	svc := newService(t)

	if _, err := svc.AdmitClaims(context.Background(), Claims{Email: "jan@example.com"}); err == nil {
		t.Fatal("AdmitClaims() err = nil, want an error for claims without a subject")
	}
}

// The group is compared as kit compares groups, case folded: an operator who
// typed it unlike the IdP spells it still gets admins.
func TestAdmitClaims_foldsTheGroupsCase(t *testing.T) {
	svc := newService(t)
	svc.SetOIDCAdminGroup("rongo")

	user, err := svc.AdmitClaims(context.Background(), Claims{Subject: "sub-4", Groups: []string{"Rongo"}})

	if err != nil || !user.IsAdmin {
		t.Errorf("user = %+v %v, want admin by the folded group", user, err)
	}
}
