package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/store/storetest"
	"golang.org/x/crypto/bcrypt"
)

func passwordService(t *testing.T) *Service {
	t.Helper()
	svc := NewService(storetest.Open(t, 4), "password", "")
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := svc.SetPasswordAccount("admin", string(hash)); err != nil {
		t.Fatalf("SetPasswordAccount: %v", err)
	}
	return svc
}

func TestLoginPassword_admitsTheAccountAsAdmin(t *testing.T) {
	svc := passwordService(t)

	claims, err := svc.LoginPassword(context.Background(), "admin", "hunter2")

	if err != nil {
		t.Fatalf("LoginPassword() err = %v", err)
	}
	if claims.Subject != passwordSubject {
		t.Errorf("claims = %+v, want the fixed subject %q", claims, passwordSubject)
	}
	u, err := svc.UpsertUser(context.Background(), passwordSubject, "", true)
	if err != nil || !u.IsAdmin {
		t.Errorf("user = %+v %v, want the admin row", u, err)
	}
}

// Username and password failures are the same error: which half was wrong is
// what a guesser wants to learn.
func TestLoginPassword_rejectsEitherHalfWrong(t *testing.T) {
	svc := passwordService(t)
	for name, in := range map[string][2]string{
		"wrong user":     {"root", "hunter2"},
		"wrong password": {"admin", "hunter3"},
		"both wrong":     {"root", "letmein"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.LoginPassword(context.Background(), in[0], in[1]); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("err = %v, want ErrBadCredentials", err)
			}
		})
	}
}

// The hash is not even compared outside password mode: no account is
// configured there.
func TestLoginPassword_refusesInOtherModes(t *testing.T) {
	svc := newService(t)

	if _, err := svc.LoginPassword(context.Background(), "admin", "hunter2"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("err = %v, want ErrBadCredentials in dev mode", err)
	}
}

// A bcrypt hash that is not one is refused at setup, before any login.
func TestSetPasswordAccount_refusesAPlaintextHash(t *testing.T) {
	svc := NewService(storetest.Open(t, 4), "password", "")

	if err := svc.SetPasswordAccount("admin", "hunter2"); err == nil {
		t.Error("SetPasswordAccount() err = nil, want a refusal of a plaintext hash")
	}
}

// Password mode has no way in but the cookie the form issues.
func TestMiddleware_passwordModeAnswers401WithoutACookie(t *testing.T) {
	svc := passwordService(t)
	withSessions(t, svc, "")
	var reached bool

	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	if reached || rec.Code != http.StatusUnauthorized {
		t.Errorf("reached = %v status = %d, want 401 and the handler untouched", reached, rec.Code)
	}
}
