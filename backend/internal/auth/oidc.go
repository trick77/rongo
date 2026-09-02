package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The transient cookies that carry the CSRF state and the replay nonce across
// the redirect to the provider and back. Named after the app so a second app on
// the same parent domain cannot overwrite them.
const (
	oidcStateCookieName = "rongo_oidc_state"
	oidcNonceCookieName = "rongo_oidc_nonce"
)

// oidcTransientTTL bounds how long a login may sit half-finished. Long enough
// for a password prompt and a second factor, short enough that an abandoned
// login does not leave a usable state value in the browser for the rest of the
// day.
const oidcTransientTTL = 10 * time.Minute

var (
	ErrInvalidState = errors.New("invalid oidc state")
	ErrInvalidNonce = errors.New("invalid oidc nonce")
)

// Claims is the verified identity rongo takes from the ID token. Groups is read
// straight off the token and never from the UserInfo endpoint, which is why
// Authelia's client entry needs claims_policy 'legacy' — see
// ../authelia/conf/configuration.yaml. Getting that wrong is silent: the login
// succeeds and the group-derived admin flag quietly disappears.
type Claims struct {
	Subject           string
	PreferredUsername string
	Email             string
	Name              string
	Groups            []string
}

// OIDCBackend is the testable seam over oauth2 and go-oidc. Everything that
// talks to the network lives behind it, so the state and nonce checks can be
// driven end to end without a provider.
type OIDCBackend interface {
	AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string
	Exchange(context.Context, string) (*oauth2.Token, error)
	VerifyClaims(context.Context, *oauth2.Token) (VerifiedClaims, error)
}

// VerifiedClaims is what a backend returns once it has checked the ID token's
// signature: the identity, plus the nonce the caller must compare against its
// own cookie.
type VerifiedClaims struct {
	Claims Claims
	Nonce  string
}

// OIDCServiceConfig configures OIDC login and callback handling.
type OIDCServiceConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Backend replaces the real oauth2/go-oidc pair. Only tests set it;
	// production goes through NewOIDCServiceFromDiscovery.
	Backend OIDCBackend
	// SecureCookie marks the transient cookies Secure. True whenever the
	// public URL is https.
	SecureCookie bool
}

// OIDCService drives the authorization-code flow and hands back verified
// claims. It owns no database: turning claims into a user and a session is
// Service's job.
type OIDCService struct {
	backend OIDCBackend
	secure  bool
}

// NewOIDCService builds a service around a pre-made backend, which is how a
// test injects a fake provider.
func NewOIDCService(cfg OIDCServiceConfig) *OIDCService {
	return &OIDCService{backend: cfg.Backend, secure: cfg.SecureCookie}
}

// NewOIDCServiceFromDiscovery reads the provider's discovery document and wires
// the real backend. It talks to the network, so it fails at startup rather than
// on the first login: a rongo that cannot reach Authelia should not come up
// looking healthy.
func NewOIDCServiceFromDiscovery(ctx context.Context, cfg OIDCServiceConfig) (*OIDCService, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover oidc provider: %w", err)
	}
	oauthConfig := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return &OIDCService{
		backend: realOIDCBackend{
			oauthConfig: oauthConfig,
			verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		},
		secure: cfg.SecureCookie,
	}, nil
}

// StartLogin redirects to the provider, remembering the state and nonce in
// cookies so the callback can prove the response belongs to this browser's
// request.
func (s *OIDCService) StartLogin(w http.ResponseWriter, r *http.Request) {
	state := randomToken()
	nonce := randomToken()
	http.SetCookie(w, s.transientCookie(oidcStateCookieName, state))
	http.SetCookie(w, s.transientCookie(oidcNonceCookieName, nonce))
	http.Redirect(w, r, s.backend.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// HandleCallback checks state, exchanges the code, verifies the ID token and
// checks the nonce. Every failure returns an error; none of them tells the
// browser which one it was.
func (s *OIDCService) HandleCallback(r *http.Request) (Claims, error) {
	stateCookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		return Claims{}, ErrInvalidState
	}
	nonceCookie, err := r.Cookie(oidcNonceCookieName)
	if err != nil || nonceCookie.Value == "" {
		return Claims{}, ErrInvalidNonce
	}
	token, err := s.backend.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		return Claims{}, fmt.Errorf("exchange oidc code: %w", err)
	}
	verified, err := s.backend.VerifyClaims(r.Context(), token)
	if err != nil {
		return Claims{}, fmt.Errorf("verify oidc claims: %w", err)
	}
	// An absent nonce is a failure, not a pass: a provider that drops the claim
	// would otherwise turn the replay check off silently.
	if verified.Nonce == "" || verified.Nonce != nonceCookie.Value {
		return Claims{}, ErrInvalidNonce
	}
	return verified.Claims, nil
}

// ClearTransientCookies drops the state and nonce cookies. The callback calls
// it whether it succeeded or failed, so a spent or abandoned login does not
// leave them sitting in the browser for their full lifetime.
func (s *OIDCService) ClearTransientCookies(w http.ResponseWriter) {
	http.SetCookie(w, s.expiredCookie(oidcStateCookieName))
	http.SetCookie(w, s.expiredCookie(oidcNonceCookieName))
}

func (s *OIDCService) transientCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(oidcTransientTTL),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *OIDCService) expiredCookie(name string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

type realOIDCBackend struct {
	oauthConfig oauth2.Config
	verifier    *oidc.IDTokenVerifier
}

func (b realOIDCBackend) AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string {
	return b.oauthConfig.AuthCodeURL(state, opts...)
}

func (b realOIDCBackend) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	return b.oauthConfig.Exchange(ctx, code)
}

func (b realOIDCBackend) VerifyClaims(ctx context.Context, token *oauth2.Token) (VerifiedClaims, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return VerifiedClaims{}, errors.New("missing id_token")
	}
	idToken, err := b.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return VerifiedClaims{}, err
	}
	var oidcClaims struct {
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		GivenName         string   `json:"given_name"`
		FamilyName        string   `json:"family_name"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&oidcClaims); err != nil {
		return VerifiedClaims{}, err
	}
	// Prefer given_name + family_name so the full name including the last one
	// is shown; fall back to the single "name" claim when those are absent.
	name := oidcClaims.Name
	if composed := strings.TrimSpace(oidcClaims.GivenName + " " + oidcClaims.FamilyName); composed != "" {
		name = composed
	}
	return VerifiedClaims{
		Claims: Claims{
			Subject:           idToken.Subject,
			PreferredUsername: oidcClaims.PreferredUsername,
			Email:             oidcClaims.Email,
			Name:              name,
			Groups:            oidcClaims.Groups,
		},
		Nonce: idToken.Nonce,
	}, nil
}

// randomToken mints 256 bits of randomness. A failure here means the system
// entropy source is broken, and continuing would hand out a guessable state or
// nonce, so it panics rather than returning something weaker.
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
