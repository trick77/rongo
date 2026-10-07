package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	token, _, err := svc.LoginPassword(context.Background(), "admin", "hunter2")
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

// bcrypt makes one guess slow; it does not make a thousand of them slow.
// Past a few failures from one address every attempt waits first, doubling
// to a cap; a good login clears the record, and an old one expires.
func TestPasswordLoginSlowsAfterFailures(t *testing.T) {
	srv := NewServer(Deps{Auth: passwordAuth(t)})
	var slept []time.Duration
	clock := time.Unix(1_700_000_000, 0)
	srv.logins.now = func() time.Time { return clock }
	srv.logins.sleep = func(_ context.Context, d time.Duration) bool {
		slept = append(slept, d)
		return true
	}
	wrong := `{"username":"admin","password":"hunter3"}`

	for range loginFreeFailures + 6 {
		if rec := postPassword(srv, wrong); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, loginDelayCap}
	if !slices.Equal(slept, want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}

	// Another address is not this one's guesser.
	slept = nil
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(wrong))
	req.RemoteAddr = "198.51.100.7:4242"
	if rec := do(srv, req); rec.Code != http.StatusUnauthorized || len(slept) != 0 {
		t.Fatalf("other address: status %d slept %v, want 401 at once", rec.Code, slept)
	}

	// The right password still waits its turn, then clears the record.
	if rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("good login = %d, want 204", rec.Code)
	}
	slept = nil
	postPassword(srv, wrong)
	if len(slept) != 0 {
		t.Errorf("after a good login slept %v, want nothing", slept)
	}

	// An old record expires.
	for range loginFreeFailures {
		postPassword(srv, wrong)
	}
	clock = clock.Add(loginFailureTTL)
	slept = nil
	postPassword(srv, wrong)
	if len(slept) != 0 {
		t.Errorf("after the record expired slept %v, want nothing", slept)
	}

	// A caller who gives up during the wait gets no verdict.
	for range loginFreeFailures {
		postPassword(srv, wrong)
	}
	srv.logins.sleep = func(context.Context, time.Duration) bool { return false }
	// An explicit status: a handler returning without one answers 200, which
	// the SPA reads as signed in.
	if rec := postPassword(srv, `{"username":"admin","password":"hunter2"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an abandoned wait answered %d, want 503", rec.Code)
	}
}

// Parallel guesses must not all read the same count and sleep together:
// each attempt is counted when it starts, so the tenth concurrent one waits
// as long as the tenth sequential one.
func TestLoginThrottle_countsAnAttemptWhenItStarts(t *testing.T) {
	th := newLoginThrottle()
	var mu sync.Mutex
	var slept []time.Duration
	th.sleep = func(_ context.Context, d time.Duration) bool {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return true
	}
	key := loginKey("192.0.2.1", "admin")
	var wg sync.WaitGroup
	for range loginFreeFailures + 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			th.wait(context.Background(), key)
		}()
	}
	wg.Wait()
	slices.Sort(slept)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if !slices.Equal(slept, want) {
		t.Errorf("slept %v, want %v", slept, want)
	}

	// Another account name from the same address has its own count.
	slept = nil
	th.wait(context.Background(), loginKey("192.0.2.1", "root"))
	if len(slept) != 0 {
		t.Errorf("another username slept %v, want nothing", slept)
	}
}

// The map is capped: past it new keys share one overflow count, so a flood
// of addresses or names neither grows memory nor escapes the brake.
func TestLoginThrottle_isCapped(t *testing.T) {
	th := newLoginThrottle()
	th.max = 3
	var slept []time.Duration
	th.sleep = func(_ context.Context, d time.Duration) bool {
		slept = append(slept, d)
		return true
	}
	for i := range 3 + loginFreeFailures + 1 {
		th.wait(context.Background(), loginKey("192.0.2.1", fmt.Sprint("user", i)))
	}
	th.mu.Lock()
	n := len(th.seen)
	th.mu.Unlock()
	if n > 4 {
		t.Errorf("tracked %d keys, want at most the cap plus the overflow", n)
	}
	if !slices.Equal(slept, []time.Duration{time.Second}) {
		t.Errorf("slept %v, want the overflow count to brake", slept)
	}

	// The sweep, when it comes round, drops what expired.
	clock := time.Now().Add(loginFailureTTL)
	th.now = func() time.Time { return clock }
	th.sweepEvery = 1
	th.wait(context.Background(), loginKey("192.0.2.9", "admin"))
	th.mu.Lock()
	n = len(th.seen)
	th.mu.Unlock()
	if n != 1 {
		t.Errorf("tracked %d keys after the sweep, want only the fresh one", n)
	}
}
