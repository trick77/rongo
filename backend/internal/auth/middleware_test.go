package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// protected reports whether the wrapped handler was reached.
func protected(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	user, _ := svc.UpsertUser("someone", "someone@example.invalid", false)
	token, _ := svc.CreateSession(user.ID, time.Hour)
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
