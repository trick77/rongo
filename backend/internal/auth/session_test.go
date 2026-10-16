package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/store/storetest"
	kitauth "github.com/trick77/ship/kit/auth"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db := storetest.Open(t, 1536)
	return NewService(db, "dev", "")
}

// withSessions installs a session store on svc with a fixed key.
func withSessions(t *testing.T, svc *Service, adminGroup string) *kitauth.Sessions {
	t.Helper()
	sessions, err := NewSessions([]byte("0123456789abcdef0123456789abcdef"), true, adminGroup)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSessions(sessions)
	svc.SetOIDCAdminGroup(adminGroup)
	return sessions
}

// issued is the cookie Sessions.Issue sets for c.
func issued(t *testing.T, sessions *kitauth.Sessions, c Claims) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, c); err != nil {
		t.Fatal(err)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == SessionCookie {
			return ck
		}
	}
	t.Fatal("no session cookie issued")
	return nil
}

// userSeen runs h behind svc.Middleware with cookie and reports the user the
// handler saw.
func userSeen(t *testing.T, svc *Service, cookie *http.Cookie) (User, int) {
	t.Helper()
	var got User
	var ok bool
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = UserFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && !ok {
		t.Fatal("handler reached with no user on the context")
	}
	return got, rec.Code
}

// A session kit issued signs its subject in, admin by the group in its claims.
func TestMiddleware_aSessionSignsItsSubjectIn(t *testing.T) {
	svc := newService(t)
	svc.mode = "oidc"
	sessions := withSessions(t, svc, "Rongo")

	u, code := userSeen(t, svc, issued(t, sessions, Claims{Subject: "authelia-sub-1", Email: "jan@example.com", Groups: []string{"Rongo"}}))

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if u.Subject != "authelia-sub-1" || u.Email != "jan@example.com" || !u.IsAdmin {
		t.Errorf("user = %+v, want the admin member of Rongo", u)
	}
}

// Admin is read off the claims on every request: the configured group
// changing reaches a live session, as it does in every service of the family.
func TestMiddleware_adminFollowsTheGroupOnEveryRequest(t *testing.T) {
	svc := newService(t)
	svc.mode = "oidc"
	sessions := withSessions(t, svc, "Rongo")
	cookie := issued(t, sessions, Claims{Subject: "sub-1", Groups: []string{"Rongo"}})

	if u, _ := userSeen(t, svc, cookie); !u.IsAdmin {
		t.Fatal("a member of the admin group is not admin")
	}
	svc.SetOIDCAdminGroup("Other")
	if u, _ := userSeen(t, svc, cookie); u.IsAdmin {
		t.Error("still admin after the group moved")
	}
}

// The password account is admin by mode, whatever its claims carry.
func TestMiddleware_thePasswordSessionIsAdmin(t *testing.T) {
	svc := newService(t)
	svc.mode = "password"
	sessions := withSessions(t, svc, "")

	u, code := userSeen(t, svc, issued(t, sessions, Claims{Subject: passwordSubject}))

	if code != http.StatusOK || u.Subject != passwordSubject || !u.IsAdmin {
		t.Errorf("status %d user %+v, want the admin password account", code, u)
	}
}

// A cookie kit did not sign is no session: a mode without another way in
// answers 401.
func TestMiddleware_rejectsAForgedCookie(t *testing.T) {
	svc := newService(t)
	svc.mode = "password"
	withSessions(t, svc, "")

	if _, code := userSeen(t, svc, &http.Cookie{Name: SessionCookie, Value: "eyJzdWIiOiJ4In0.forged"}); code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
}

// The store is written once per subject and admit interval, not per request.
func TestMiddleware_aSessionAdmitsWithoutWritingOnEveryRequest(t *testing.T) {
	svc := newService(t)
	svc.mode = "oidc"
	sessions := withSessions(t, svc, "")
	cookie := issued(t, sessions, Claims{Subject: "sub-1"})

	for range 5 {
		if _, code := userSeen(t, svc, cookie); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
	}
	if n := svc.upserts.Load(); n != 1 {
		t.Errorf("wrote the user %d times, want once", n)
	}
	if _, err := svc.Admit(context.Background(), "sub-1", "", true); err != nil {
		t.Fatal(err)
	}
}

// The password account's admin-by-mode holds in password mode only: in oidc
// mode a subject that happens to spell it is judged by its groups like any
// other.
func TestMiddleware_thePasswordSubjectIsNoAdminInOIDCMode(t *testing.T) {
	svc := newService(t)
	svc.mode = "oidc"
	sessions := withSessions(t, svc, "Rongo")

	u, code := userSeen(t, svc, issued(t, sessions, Claims{Subject: passwordSubject}))

	if code != http.StatusOK || u.IsAdmin {
		t.Errorf("status %d user %+v, want signed in and not admin", code, u)
	}
}
