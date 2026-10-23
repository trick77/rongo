package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/store/storetest"
	kitauth "github.com/trick77/ship/kit/auth"
)

// testSessions is kit's session store with a fixed key, installed on svc.
func testSessions(t *testing.T, svc *auth.Service, adminGroup string) *kitauth.Sessions {
	t.Helper()
	sessions, err := auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), true, adminGroup)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSessions(sessions)
	svc.SetOIDCAdminGroup(adminGroup)
	return sessions
}

// sessionCookie is the cookie sessions issues for c.
func sessionCookie(t *testing.T, sessions *kitauth.Sessions, c auth.Claims) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, c); err != nil {
		t.Fatal(err)
	}
	return cookie(t, rec, auth.SessionCookie)
}

func TestAuthLogin_returns503WithoutOIDC(t *testing.T) {
	srv := NewServer(Deps{Auth: devAuth(t)})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestAuthLogin_isReachableWithoutASession(t *testing.T) {
	srv := NewServer(Deps{Auth: devAuth(t), OIDC: &fakeOIDC{}})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "https://auth.example.com/authorize" {
		t.Errorf("Location = %q", got)
	}
}

func TestAuthCallback_setsSessionCookieAndRedirectsToTheApp(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "oidc", "")
	sessions := testSessions(t, svc, "")
	srv := NewServer(Deps{
		Auth:     svc,
		OIDC:     &fakeOIDC{claims: auth.Claims{Subject: "sub-1", Email: "jan@example.com"}},
		Sessions: sessions,
	})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state=xyz", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q", got, "/")
	}
	c := cookie(t, rec, auth.SessionCookie)
	if c.Value == "" {
		t.Fatal("session cookie has no value")
	}
	if !c.Secure || !c.HttpOnly {
		t.Errorf("cookie = %+v, want Secure and HttpOnly", c)
	}
	// The cookie signs the subject in.
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(c)
	me := do(srv, req)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"sub-1"`) {
		t.Errorf("/api/me with the cookie = %d %s, want the subject", me.Code, me.Body.String())
	}
}

func TestAuthCallback_redirectsWithGenericErrorOnFailure(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "oidc", "")
	srv := NewServer(Deps{
		Auth:     svc,
		OIDC:     &fakeOIDC{err: errors.New("exchange oidc code: provider said no")},
		Sessions: testSessions(t, svc, ""),
	})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/callback?state=bad", nil))

	if got := rec.Header().Get("Location"); got != "/?auth_error=oidc_callback_failed" {
		t.Fatalf("Location = %q, want the generic error", got)
	}
	if strings.Contains(rec.Body.String(), "provider said no") {
		t.Errorf("body = %q, want nothing that names the failure", rec.Body.String())
	}
}

// Logout clears the cookie; there is nothing to revoke, the claims travel in
// it. The redirect names where the browser goes next.
func TestAuthLogout_clearsTheCookie(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "oidc", "")
	sessions := testSessions(t, svc, "")
	srv := NewServer(Deps{Auth: svc, Sessions: sessions})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(sessionCookie(t, sessions, auth.Claims{Subject: "sub-1"}))

	rec := do(srv, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["redirect_url"] != "/?signed_out=1" {
		t.Errorf("redirect_url = %q, want %q", body["redirect_url"], "/?signed_out=1")
	}
	if c := cookie(t, rec, auth.SessionCookie); c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("session cookie = %+v, want it cleared", c)
	}
}

func do(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func cookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q cookie in the response", name)
	return nil
}

type fakeOIDC struct {
	claims auth.Claims
	err    error
}

func (f *fakeOIDC) Start(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://auth.example.com/authorize", http.StatusFound)
}

func (f *fakeOIDC) Callback(http.ResponseWriter, *http.Request) (auth.Claims, error) {
	return f.claims, f.err
}

func TestAuthLogin_proxyModeSendsTheBrowserToTheApp(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "proxy", "")
	srv := NewServer(Deps{Auth: svc})
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	req.Header.Set(auth.ProxyUserHeader, "jdoe")

	rec := do(srv, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}

func TestAuthLogin_proxyModeHaltsWhenTheProxyNamesNobody(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "proxy", "")
	srv := NewServer(Deps{Auth: svc})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "/?auth_error=proxy" {
		t.Errorf("Location = %q, want %q", loc, "/?auth_error=proxy")
	}
}

func TestAuthLogout_proxyModeHandsOffToTheProxy(t *testing.T) {
	svc := auth.NewService(storetest.Open(t, 4), "proxy", "")
	srv := NewServer(Deps{Auth: svc})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set(auth.ProxyUserHeader, "jdoe")

	rec := do(srv, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["redirect_url"] != "/oauth/sign_out" {
		t.Errorf("redirect_url = %q, want %q", body["redirect_url"], "/oauth/sign_out")
	}
}

// An abandoned request is not let through to the logout: it gets the 500 any
// failed admit gets, never an empty 200.
func TestAuthLogout_anAbandonedRequestIsTheAdmit500(t *testing.T) {
	srv := NewServer(Deps{Auth: devAuth(t)})
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	rec := do(srv, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil).WithContext(gone))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "redirect_url") {
		t.Errorf("body = %s: the logout ran for a request with no user", rec.Body.String())
	}
}
