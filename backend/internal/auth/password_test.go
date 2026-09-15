package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func passwordService(t *testing.T) *Service {
	t.Helper()
	svc := newService(t)
	svc.mode = "password"
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	svc.SetPasswordAccount("admin", string(hash))
	return svc
}

func TestLoginPassword_mintsAnAdminSession(t *testing.T) {
	svc := passwordService(t)

	token, _, err := svc.LoginPassword("admin", "hunter2")

	if err != nil {
		t.Fatalf("LoginPassword() err = %v", err)
	}
	u, ok := svc.UserByToken(token)
	if !ok {
		t.Fatal("the minted token does not resolve")
	}
	if u.Subject != passwordSubject || !u.IsAdmin {
		t.Errorf("user = %+v, want admin %q", u, passwordSubject)
	}
}

// Username and password failures are the same error: which half was wrong is
// what a guesser wants to learn.
func TestLoginPassword_rejectsEitherHalfWrong(t *testing.T) {
	svc := passwordService(t)
	for name, in := range map[string][2]string{
		"wrong user":     {"root", "hunter2"},
		"wrong password": {"admin", "hunter3"},
		"empty":          {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.LoginPassword(in[0], in[1])
			if !errors.Is(err, ErrBadCredentials) {
				t.Fatalf("err = %v, want ErrBadCredentials", err)
			}
		})
	}
}

// Outside password mode there is no account, so the right credentials for one
// must not sign anyone in.
func TestLoginPassword_refusesInOtherModes(t *testing.T) {
	svc := newService(t)
	svc.SetPasswordAccount("admin", "$2a$04$irrelevant")

	if _, _, err := svc.LoginPassword("admin", "anything"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}

// The middleware's only way in is the cookie: a bearer token that would pass
// token mode is nothing here.
func TestMiddleware_passwordModeAnswers401WithoutACookie(t *testing.T) {
	svc := passwordService(t)
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer hunter2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
