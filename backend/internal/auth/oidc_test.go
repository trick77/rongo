package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestStartLogin_redirectsAndSetsStateAndNonce(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{
		ClientID:    "rongo",
		RedirectURL: "https://rongo.example.com/api/auth/callback",
		Backend:     fakeOIDCBackend{authURL: "https://auth.example.com/authorize"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	rec := httptest.NewRecorder()

	// When
	svc.StartLogin(rec, req)

	// Then
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://auth.example.com/authorize") {
		t.Fatalf("Location = %q", loc)
	}
	if got := len(rec.Result().Cookies()); got != 2 {
		t.Fatalf("cookies = %d, want state and nonce", got)
	}
}

func TestHandleCallback_returnsClaimsOnValidStateAndNonce(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{
		ClientID: "rongo",
		Backend: fakeOIDCBackend{
			claims: Claims{
				Subject:           "sub-1",
				PreferredUsername: "jan",
				Email:             "jan@example.com",
				Groups:            []string{"Rongo"},
			},
			nonce: validNonce,
		},
	})
	req := callbackWithValidStateAndNonce(t, svc)

	// When
	claims, err := svc.HandleCallback(req)

	// Then
	if err != nil {
		t.Fatalf("HandleCallback() err = %v", err)
	}
	if claims.Subject != "sub-1" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "sub-1")
	}
	if claims.PreferredUsername != "jan" {
		t.Errorf("PreferredUsername = %q, want %q", claims.PreferredUsername, "jan")
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "Rongo" {
		t.Errorf("Groups = %v, want [Rongo]", claims.Groups)
	}
}

func TestHandleCallback_rejectsStateMismatch(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{ClientID: "rongo", Backend: fakeOIDCBackend{}})
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?state=bad&code=abc", nil)

	// When
	_, err := svc.HandleCallback(req)

	// Then
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
}

// A provider that stops returning the nonce claim must fail, not pass. Treating
// an absent nonce as "nothing to compare" would switch the replay check off
// without anything looking broken.
func TestHandleCallback_rejectsAbsentNonceClaim(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{
		ClientID: "rongo",
		Backend:  fakeOIDCBackend{claims: Claims{Subject: "sub-1"}},
	})
	req := callbackWithValidStateAndNonce(t, svc)

	// When
	_, err := svc.HandleCallback(req)

	// Then
	if !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("err = %v, want ErrInvalidNonce", err)
	}
}

func TestHandleCallback_rejectsNonceMismatch(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{
		ClientID: "rongo",
		Backend:  fakeOIDCBackend{claims: Claims{Subject: "sub-1"}, nonce: "someone-elses-nonce"},
	})
	req := callbackWithValidStateAndNonce(t, svc)

	// When
	_, err := svc.HandleCallback(req)

	// Then
	if !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("err = %v, want ErrInvalidNonce", err)
	}
}

func TestHandleCallback_wrapsExchangeFailure(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{
		ClientID: "rongo",
		Backend:  fakeOIDCBackend{exchangeErr: errors.New("provider said no")},
	})
	req := callbackWithValidStateAndNonce(t, svc)

	// When
	_, err := svc.HandleCallback(req)

	// Then
	if err == nil || !strings.Contains(err.Error(), "exchange oidc code") {
		t.Fatalf("err = %v, want it to name the exchange step", err)
	}
}

func TestClearTransientCookies_expiresStateAndNonce(t *testing.T) {
	// Given
	svc := NewOIDCService(OIDCServiceConfig{ClientID: "rongo", Backend: fakeOIDCBackend{}})
	rec := httptest.NewRecorder()

	// When
	svc.ClearTransientCookies(rec)

	// Then
	cleared := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	if !cleared[oidcStateCookieName] || !cleared[oidcNonceCookieName] {
		t.Fatalf("cleared = %v, want both state and nonce", cleared)
	}
}

// validNonce is the value callbackWithValidStateAndNonce forces into the nonce
// cookie, so a fake backend can return a matching one.
const validNonce = "valid-nonce"

// callbackWithValidStateAndNonce drives a real StartLogin and turns the cookies
// it set into a callback request, so the state travels the way it does in a
// browser instead of being hand-written. The nonce cookie is overwritten with a
// known value the fake backend can echo.
func callbackWithValidStateAndNonce(t *testing.T, svc *OIDCService) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.StartLogin(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))

	var state string
	callback := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case oidcStateCookieName:
			state = c.Value
		case oidcNonceCookieName:
			c.Value = validNonce
		}
		callback.AddCookie(c)
	}
	callback.URL.RawQuery = "code=abc&state=" + state
	return callback
}

type fakeOIDCBackend struct {
	authURL     string
	claims      Claims
	nonce       string
	exchangeErr error
}

func (f fakeOIDCBackend) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	url := f.authURL
	if url == "" {
		url = "https://auth.example.com/authorize"
	}
	return url + "?state=" + state
}

func (f fakeOIDCBackend) Exchange(context.Context, string) (*oauth2.Token, error) {
	if f.exchangeErr != nil {
		return nil, f.exchangeErr
	}
	return &oauth2.Token{}, nil
}

func (f fakeOIDCBackend) VerifyClaims(context.Context, *oauth2.Token) (VerifiedClaims, error) {
	return VerifiedClaims{Claims: f.claims, Nonce: f.nonce}, nil
}
