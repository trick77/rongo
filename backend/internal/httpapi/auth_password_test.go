package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/store/storetest"
	"github.com/trick77/ship/kit/ratelimit"
	"golang.org/x/crypto/bcrypt"
)

func passwordAuth(t *testing.T) *auth.Service {
	t.Helper()
	svc := auth.NewService(storetest.Open(t, 4), "password", "")
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := svc.SetPasswordAccount("admin", string(hash)); err != nil {
		t.Fatalf("SetPasswordAccount: %v", err)
	}
	return svc
}

// passwordServer is a server in password mode with kit's sessions.
func passwordServer(t *testing.T) *Server {
	t.Helper()
	svc := passwordAuth(t)
	return NewServer(Deps{Auth: svc, Sessions: testSessions(t, svc, "")})
}

func postPassword(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return do(srv, req)
}

func TestAuthPassword_setsTheSessionCookie(t *testing.T) {
	srv := passwordServer(t)

	rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	c := cookie(t, rec, auth.SessionCookie)
	if !c.Secure || !c.HttpOnly {
		t.Errorf("cookie = %+v, want Secure and HttpOnly", c)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(c)
	if me := do(srv, req); me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"is_admin":true`) {
		t.Errorf("/api/me with the cookie = %d %s, want the admin account", me.Code, me.Body.String())
	}
}

func TestAuthPassword_answersOneGeneric401(t *testing.T) {
	srv := passwordServer(t)
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
			// The brake between attempts; the gap is short, not zero.
			time.Sleep(loginGap)
		})
	}
}

func TestAuthPassword_rejectsAMalformedBody(t *testing.T) {
	if rec := postPassword(passwordServer(t), `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAuthPassword_is503OutsidePasswordMode(t *testing.T) {
	srv := NewServer(Deps{Auth: devAuth(t)})

	if rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAuthLogin_passwordModeRedirectsToTheForm(t *testing.T) {
	srv := passwordServer(t)

	rec := do(srv, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?login=password" {
		t.Fatalf("response = %d %q, want 302 to /?login=password", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthLogout_passwordModeSaysLocal(t *testing.T) {
	svc := passwordAuth(t)
	sessions := testSessions(t, svc, "")
	srv := NewServer(Deps{Auth: svc, Sessions: sessions})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(sessionCookie(t, sessions, auth.Claims{Subject: "admin-password"}))

	rec := do(srv, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/?signed_out=local"`) {
		t.Errorf("logout = %d %s, want the local sign-out target", rec.Code, rec.Body.String())
	}
}

// The brake is kit's throttle per client: a second attempt inside the gap
// queues, and past the queue's end it is refused with a 429 that says when to
// come back. Another client is not held.
func TestPasswordLoginIsThrottledPerClient(t *testing.T) {
	srv := passwordServer(t)
	srv.logins = ratelimit.NewThrottle(50*time.Millisecond, 0)
	wrong := `{"username":"admin","password":"hunter3"}`

	if rec := postPassword(srv, wrong); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first attempt = %d, want 401", rec.Code)
	}
	rec := postPassword(srv, wrong)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second attempt inside the gap = %d, Retry-After %q; want 429 with a wait", rec.Code, rec.Header().Get("Retry-After"))
	}

	other := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(wrong))
	other.Header.Set("Content-Type", "application/json")
	other.RemoteAddr = "198.51.100.7:4242"
	if rec := do(srv, other); rec.Code != http.StatusUnauthorized {
		t.Errorf("another client = %d, want 401 at once", rec.Code)
	}

	time.Sleep(60 * time.Millisecond)
	if rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`); rec.Code != http.StatusNoContent {
		t.Errorf("after the gap = %d, want 204", rec.Code)
	}
}
