package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/auth"
)

// A deployment in dev or token mode has no OIDC service. The route must say so
// rather than panic on a nil dependency.
func TestAuthLogin_returns503WithoutOIDC(t *testing.T) {
	// Given
	srv := NewServer(Deps{Auth: devAuth(t)})

	// When
	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	// Then
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// Login must not sit behind requireAuth: the caller has no session yet, so a
// 401 here would be a redirect loop between the SPA and the login route.
func TestAuthLogin_isReachableWithoutASession(t *testing.T) {
	// Given
	srv := NewServer(Deps{Auth: devAuth(t), OIDC: &fakeOIDC{}})

	// When
	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	// Then
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "https://auth.example.com/authorize" {
		t.Errorf("Location = %q", got)
	}
}

func TestAuthCallback_setsSessionCookieAndRedirectsToTheApp(t *testing.T) {
	// Given
	srv := NewServer(Deps{
		Auth:         devAuth(t),
		OIDC:         &fakeOIDC{claims: auth.Claims{Subject: "sub-1", Email: "jan@example.com"}},
		CookieSecure: true,
	})

	// When
	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state=xyz", nil))

	// Then
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
	if !c.Secure {
		t.Error("session cookie is not Secure although the public URL is https")
	}
}

// The browser learns only that the callback failed. Which of the four failure
// modes it was stays in the log, because telling them apart from outside is
// what probing the callback is for.
func TestAuthCallback_redirectsWithGenericErrorOnFailure(t *testing.T) {
	// Given
	srv := NewServer(Deps{
		Auth: devAuth(t),
		OIDC: &fakeOIDC{err: errors.New("exchange oidc code: provider said no")},
	})

	// When
	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/callback?state=bad", nil))

	// Then
	if got := rec.Header().Get("Location"); got != "/?auth_error=oidc_callback_failed" {
		t.Fatalf("Location = %q, want the generic error", got)
	}
	if strings.Contains(rec.Body.String(), "provider said no") {
		t.Errorf("body = %q, want nothing that names the failure", rec.Body.String())
	}
}

// Success or failure, the transient cookies go: an abandoned login must not
// leave a usable state value behind for its full lifetime.
func TestAuthCallback_clearsTransientCookiesOnFailure(t *testing.T) {
	// Given
	f := &fakeOIDC{err: errors.New("bad state")}
	srv := NewServer(Deps{Auth: devAuth(t), OIDC: f})

	// When
	do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil))

	// Then
	if !f.cleared {
		t.Fatal("ClearTransientCookies was not called after a failed callback")
	}
}

func TestAuthLogout_revokesTheSessionAndClearsTheCookie(t *testing.T) {
	// Given
	svc := devAuth(t)
	token, _, _, err := svc.CreateSessionFromClaims(auth.Claims{Subject: "sub-1"}, "")
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() err = %v", err)
	}
	srv := NewServer(Deps{Auth: svc, CookieSecure: true})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})

	// When
	rec := do(srv, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Not "/": the provider's session survives this, so landing on the app root
	// would redirect to the provider and sign the user straight back in.
	if body["redirect_url"] != "/?signed_out=1" {
		t.Errorf("redirect_url = %q, want %q", body["redirect_url"], "/?signed_out=1")
	}
	if c := cookie(t, rec, auth.SessionCookie); c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("session cookie = %+v, want it cleared", c)
	}
	if _, ok := svc.UserByToken(token); ok {
		t.Error("the session still resolves after logout")
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
	claims  auth.Claims
	err     error
	cleared bool
}

func (f *fakeOIDC) StartLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://auth.example.com/authorize", http.StatusFound)
}

func (f *fakeOIDC) HandleCallback(*http.Request) (auth.Claims, error) {
	return f.claims, f.err
}

func (f *fakeOIDC) ClearTransientCookies(http.ResponseWriter) { f.cleared = true }
