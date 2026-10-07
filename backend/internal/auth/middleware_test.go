package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// protected reports whether the wrapped handler was reached.
func protected(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_devModeAutoLogsInAsAdmin(t *testing.T) {
	// Given
	svc := newService(t)
	var got User
	var reached bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The user is attached to the request passed downstream, so it must be
		// read here rather than from the original request.
		got, reached = mustUser(t, r)
		w.WriteHeader(http.StatusOK)
	})

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	svc.Middleware(handler).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatal("handler not reached; dev mode should sign the caller in")
	}
	if got.Subject != devSubject {
		t.Errorf("subject = %q, want %q", got.Subject, devSubject)
	}
	if !got.IsAdmin {
		t.Error("dev user is not admin, want admin")
	}
}

// mustUser reads the authenticated user off the request context.
func mustUser(t *testing.T, r *http.Request) (User, bool) {
	t.Helper()
	u, ok := UserFrom(r.Context())
	return u, ok
}

func TestMiddleware_anAbandonedRequestIsNotAFailedLogin(t *testing.T) {
	// Given dev mode, and a request whose client went away before the user
	// row was written
	svc := newService(t)
	var reached bool
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil).WithContext(gone)
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then nobody is let through, and nothing is reported as a server error
	if reached {
		t.Error("handler reached for a request with no user")
	}
	if rec.Code == http.StatusInternalServerError {
		t.Error("an abandoned request answered 500, want it dropped quietly")
	}
}

func TestMiddleware_proxyModeSignsInTheForwardedUser(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "proxy"
	var got User
	var reached bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, reached = mustUser(t, r)
		w.WriteHeader(http.StatusOK)
	})

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set(ProxyUserHeader, "jdoe")
	req.Header.Set(ProxyEmailHeader, "jdoe@cluster.local")
	rec := httptest.NewRecorder()
	svc.Middleware(handler).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatalf("handler not reached, status %d; proxy mode should trust the header", rec.Code)
	}
	if got.Subject != "jdoe" {
		t.Errorf("subject = %q, want %q", got.Subject, "jdoe")
	}
	if got.Email != "jdoe@cluster.local" {
		t.Errorf("email = %q, want %q", got.Email, "jdoe@cluster.local")
	}
	if !got.IsAdmin {
		t.Error("proxy user is not admin, want admin")
	}
}

// proxyAdmin is whether proxy mode admits a caller sending groups as admin.
func proxyAdmin(t *testing.T, svc *Service, groups string) bool {
	t.Helper()
	var got User
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = mustUser(t, r)
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set(ProxyUserHeader, "jdoe")
	if groups != "" {
		req.Header.Set(ProxyGroupsHeader, groups)
	}
	rec := httptest.NewRecorder()
	svc.Middleware(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the caller let in either way", rec.Code)
	}
	return got.IsAdmin
}

// With an admin group set, the proxy's group header decides: a member may
// re-index, everyone else the proxy let in may only ask.
func TestProxyModeNonMemberIsNotAdmin(t *testing.T) {
	svc := newService(t)
	svc.mode = "proxy"
	svc.SetProxyAdminGroup("rongo-admins")

	if proxyAdmin(t, svc, "") {
		t.Error("a caller with no groups is admin")
	}
	if proxyAdmin(t, svc, "devs, readers") {
		t.Error("a non-member is admin")
	}
	if !proxyAdmin(t, svc, "devs, rongo-admins ,readers") {
		t.Error("a member is not admin")
	}
}

// No group configured is no check: whom the proxy lets in is its decision.
func TestProxyModeEmptyGroupEveryoneIsAdmin(t *testing.T) {
	svc := newService(t)
	svc.mode = "proxy"

	if !proxyAdmin(t, svc, "") || !proxyAdmin(t, svc, "devs") {
		t.Error("a forwarded user is not admin without an admin group")
	}
}

func TestMiddleware_proxyModeRejectsAMissingHeader(t *testing.T) {
	// Given: a request that reached the process without the proxy's header,
	// which is what a skip-auth path on the proxy forwards.
	svc := newService(t)
	svc.mode = "proxy"
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set(ProxyUserHeader, "   ")
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if reached {
		t.Fatal("handler reached without a forwarded user")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_tokenModeRejectsMissingToken(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if reached {
		t.Error("handler reached without a token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_tokenModeAcceptsBearerToken(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer s3cret-token")
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatalf("handler not reached; status = %d", rec.Code)
	}
}

func TestMiddleware_tokenModeRejectsWrongToken(t *testing.T) {
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	if reached {
		t.Error("handler reached with the wrong token")
	}
}

func TestMiddleware_failsClosedForUnauthenticatedModes(t *testing.T) {
	// Given: "oidc" (not wired yet) and any unrecognized mode must both fail
	// closed with 401 rather than let the request through or 500.
	for _, mode := range []string{"", "kerberos", "oidc"} {
		t.Run(mode, func(t *testing.T) {
			svc := newService(t)
			svc.mode = mode
			var reached bool

			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			rec := httptest.NewRecorder()
			svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

			if reached {
				t.Errorf("handler reached in mode %q, want fail-closed", mode)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestMiddleware_acceptsSessionCookie(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	user, _ := svc.UpsertUser(context.Background(), "someone", "someone@example.invalid", false)
	token, _ := svc.CreateSession(context.Background(), user.ID, time.Hour)
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatalf("handler not reached; status = %d", rec.Code)
	}
}

func TestMiddleware_admitsWithoutWritingOnEveryRequest(t *testing.T) {
	// Every request in dev, token and proxy mode used to upsert the user on
	// its way in — one write beside the indexer's per request. The row is
	// written once and remembered for a while.
	svc := newService(t)
	var reached int
	h := svc.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	if reached != 5 {
		t.Fatalf("reached the handler %d times, want 5", reached)
	}
	if n := svc.upserts.Load(); n != 1 {
		t.Errorf("wrote the user %d times, want once", n)
	}
	// A changed identity is written again: the proxy's say wins over the
	// memory of it.
	if _, err := svc.Admit(context.Background(), devSubject, "other@example.invalid", true); err != nil {
		t.Fatal(err)
	}
	if n := svc.upserts.Load(); n != 2 {
		t.Errorf("a changed email wrote the user %d times in all, want 2", n)
	}
}

func TestAdmit_forgetsSubjectsNotSeenLately(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Admit(context.Background(), "old", "old@example.invalid", true); err != nil {
		t.Fatal(err)
	}
	svc.admitMu.Lock()
	a := svc.admitted["old"]
	a.at = a.at.Add(-2 * admitTTL)
	svc.admitted["old"] = a
	svc.admitMu.Unlock()

	if _, err := svc.Admit(context.Background(), "new", "new@example.invalid", true); err != nil {
		t.Fatal(err)
	}

	svc.admitMu.Lock()
	defer svc.admitMu.Unlock()
	if _, ok := svc.admitted["old"]; ok {
		t.Error("a subject past the interval is still remembered")
	}
	if _, ok := svc.admitted["new"]; !ok {
		t.Error("the subject just admitted is not remembered")
	}
}
