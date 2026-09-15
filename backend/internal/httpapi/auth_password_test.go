package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func passwordAuth(t *testing.T) *auth.Service {
	t.Helper()
	svc := auth.NewService(authDB(t), "password", "")
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	svc.SetPasswordAccount("admin", string(hash))
	return svc
}

func postPassword(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return do(srv, req)
}

func TestAuthPassword_setsTheSessionCookie(t *testing.T) {
	svc := passwordAuth(t)
	srv := NewServer(Deps{Auth: svc, CookieSecure: true})

	rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	c := cookie(t, rec, auth.SessionCookie)
	if !c.Secure || !c.HttpOnly {
		t.Errorf("cookie = %+v, want Secure and HttpOnly", c)
	}
	// The cookie is the whole login: it must open the gated routes.
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: c.Value})
	if me := do(srv, req); me.Code != http.StatusOK {
		t.Errorf("/api/me with the cookie = %d, want 200", me.Code)
	}
}

func TestAuthPassword_answersOneGeneric401(t *testing.T) {
	srv := NewServer(Deps{Auth: passwordAuth(t)})
	for name, body := range map[string]string{
		"wrong user":     `{"username":"root","password":"hunter2"}`,
		"wrong password": `{"username":"admin","password":"hunter3"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postPassword(srv, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if cs := rec.Result().Cookies(); len(cs) != 0 {
				t.Errorf("a rejected login set cookies: %+v", cs)
			}
		})
	}
}

func TestAuthPassword_rejectsAMalformedBody(t *testing.T) {
	srv := NewServer(Deps{Auth: passwordAuth(t)})

	if rec := postPassword(srv, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The route exists in every mode so the SPA can probe it, but only password
// mode has an account behind it.
func TestAuthPassword_is503OutsidePasswordMode(t *testing.T) {
	srv := NewServer(Deps{Auth: devAuth(t)})

	if rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// The SPA lands on /api/auth/login for every 401 without knowing the mode.
// In password mode that has to become the form, not a 503.
func TestAuthLogin_passwordModeRedirectsToTheForm(t *testing.T) {
	srv := NewServer(Deps{Auth: passwordAuth(t)})

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?login=password" {
		t.Fatalf("response = %d %q, want 302 to /?login=password", rec.Code, rec.Header().Get("Location"))
	}
}

// No provider, so no "sign out there as well": the marker says so.
func TestAuthLogout_passwordModeSaysLocal(t *testing.T) {
	svc := passwordAuth(t)
	token, _, err := svc.LoginPassword("admin", "hunter2")
	if err != nil {
		t.Fatalf("LoginPassword() err = %v", err)
	}
	srv := NewServer(Deps{Auth: svc})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})

	rec := do(srv, req)

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["redirect_url"] != "/?signed_out=local" {
		t.Errorf("redirect_url = %q, want /?signed_out=local", body["redirect_url"])
	}
}
