// Package auth identifies callers: the dev, token and proxy modes admit a
// request on what it carries, password and OIDC mode on kit's signed session
// cookie, issued at login.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"
)

// SessionCookie carries kit's signed session.
const SessionCookie = "rongo_session"

// SessionTTL is how long a session stays valid after it is issued; nothing
// renews it. Authelia runs its own, shorter inactivity window; this one only
// bounds how long rongo's own cookie is worth anything if the provider is
// never consulted again.
const SessionTTL = 30 * 24 * time.Hour

// NewSessions is the family's session store for rongo: the claims travel in
// the cookie, base64 JSON plus an HMAC, so there is no session table and
// nothing revokes a single session; rotating the key signs everyone out.
// adminGroup survives kit's cap on the groups kept in the cookie, since admin
// is read off them on every request.
func NewSessions(key []byte, secure bool, adminGroup string) (*kitauth.Sessions, error) {
	return kitauth.NewSessions(kitauth.SessionConfig{
		Name: SessionCookie, Key: key, TTL: SessionTTL, Secure: secure, KeepGroup: adminGroup,
	})
}

// User is an authenticated identity.
type User struct {
	ID      int64
	Subject string
	Email   string
	IsAdmin bool
}

// Service owns users and admits requests.
type Service struct {
	db         *sql.DB
	mode       string
	adminToken string
	// sessions reads the cookie password and OIDC mode issue; nil in the
	// modes that admit every request on what it carries.
	sessions *kitauth.Sessions
	// verifier is the password-mode account: kit's bcrypt compare, the
	// username compared by digest.
	verifier kitauth.Verifier
	// oidcAdminGroup is the group whose members an OIDC session admits as
	// admins; empty admits every user the provider let through as one.
	oidcAdminGroup string
	// proxyAdminGroup is the group whose members proxy mode admits as
	// admins; empty admits every forwarded user as one.
	proxyAdminGroup string
	// noGroupsOnce keeps the missing-groups-header warning to one line.
	noGroupsOnce sync.Once
	// admitted is the users the middleware upserted lately, so a request
	// does not write the users table on its way in: in dev, token and proxy
	// mode every request used to be an INSERT … ON CONFLICT beside the
	// indexer's own writes.
	admitMu  sync.Mutex
	admitted map[string]admission
	// upserts counts the writes, for the test that pins the caching.
	upserts atomic.Int64
}

// admission is one upserted user and when the write happened.
type admission struct {
	user User
	at   time.Time
}

// admitTTL is how long an upsert stands for a subject. An email or admin
// flag that changes at the proxy reaches the row on the next write.
const admitTTL = 5 * time.Minute

// NewService builds the auth service. adminToken is only consulted in token
// mode.
func NewService(db *sql.DB, mode string, adminToken string) *Service {
	return &Service{db: db, mode: mode, adminToken: adminToken, admitted: map[string]admission{}}
}

// SetSessions installs the cookie store password and OIDC mode issue and
// read.
func (s *Service) SetSessions(sessions *kitauth.Sessions) {
	s.sessions = sessions
}

// Admit is UpsertUser for the request path: the row is written once per
// admitTTL per subject and remembered in between, so a page of thirty
// requests is one write, not thirty.
func (s *Service) Admit(ctx context.Context, subject, email string, isAdmin bool) (User, error) {
	now := time.Now()
	s.admitMu.Lock()
	if a, ok := s.admitted[subject]; ok && now.Sub(a.at) < admitTTL && a.user.Email == email && a.user.IsAdmin == isAdmin {
		s.admitMu.Unlock()
		return a.user, nil
	}
	s.admitMu.Unlock()
	u, err := s.UpsertUser(ctx, subject, email, isAdmin)
	if err != nil {
		return User{}, err
	}
	s.admitMu.Lock()
	// Evicted on the write path, so the map holds the subjects seen lately
	// and not every subject a proxy ever sent.
	for k, a := range s.admitted {
		if now.Sub(a.at) >= admitTTL {
			delete(s.admitted, k)
		}
	}
	s.admitted[subject] = admission{user: u, at: now}
	s.admitMu.Unlock()
	return u, nil
}

// SetPasswordAccount installs the one account password mode signs in. The
// hash is bcrypt; config has already refused anything else, kit refuses it
// again.
func (s *Service) SetPasswordAccount(user, hash string) error {
	v, err := kitauth.BcryptVerifier(user, hash)
	if err != nil {
		return err
	}
	s.verifier = v
	return nil
}

// SetOIDCAdminGroup installs the group an OIDC session's claims must carry
// for admin. Empty is no check.
func (s *Service) SetOIDCAdminGroup(group string) {
	s.oidcAdminGroup = group
}

// SetProxyAdminGroup installs the group proxy mode reads from the proxy's
// group header to decide admin. Empty is no check.
func (s *Service) SetProxyAdminGroup(group string) {
	s.proxyAdminGroup = group
}

// Mode reports the configured auth mode.
func (s *Service) Mode() string { return s.mode }

// passwordSubject is the fixed identity password mode signs in. Not the
// username: renaming the account in the environment must not orphan the
// threads that hang off users.subject.
const passwordSubject = "admin-password"

// ErrBadCredentials is the one answer a failed password login gets. Username
// and password failures are indistinguishable from outside on purpose.
var ErrBadCredentials = kitauth.ErrBadCredentials

// LoginPassword checks the form credentials and admits the one account. The
// claims are the session to issue: kit's verifier takes the same time
// whichever half is wrong, so the response time does not tell an attacker
// which one they got right.
func (s *Service) LoginPassword(ctx context.Context, user, password string) (Claims, error) {
	if s.mode != "password" || s.verifier == nil {
		return Claims{}, ErrBadCredentials
	}
	if _, err := s.verifier.Check(user, password); err != nil {
		return Claims{}, ErrBadCredentials
	}
	if _, err := s.UpsertUser(ctx, passwordSubject, "", true); err != nil {
		return Claims{}, err
	}
	return Claims{Subject: passwordSubject}, nil
}

// UpsertUser inserts the subject or returns the existing row.
func (s *Service) UpsertUser(ctx context.Context, subject, email string, isAdmin bool) (User, error) {
	s.upserts.Add(1)
	admin := 0
	if isAdmin {
		admin = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO users (subject, email, is_admin) VALUES (?, ?, ?)
		 ON CONFLICT(subject) DO UPDATE SET email = excluded.email, is_admin = excluded.is_admin`,
		subject, email, admin,
	); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	var u User
	var adminInt int
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, subject, email, is_admin FROM users WHERE subject = ?`, subject,
	).Scan(&u.ID, &u.Subject, &u.Email, &adminInt); err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	u.IsAdmin = adminInt == 1
	return u, nil
}

// AdmitClaims turns a verified OIDC identity into a local user; the claims
// are the session to issue, and admin is decided again from their groups on
// every request (Middleware), so a changed admin group reaches live sessions.
//
// The admin group (SetOIDCAdminGroup) is the group whose members are admins,
// compared as kit compares groups, case folded. Empty means no group check at
// all: every user the provider let through is an admin. That is peeq's
// default and the honest one while the authorization decision still lives
// entirely in Authelia's authorization_policy — a group name invented here
// would look like a second gate without being one.
func (s *Service) AdmitClaims(ctx context.Context, claims Claims) (User, error) {
	if claims.Subject == "" {
		return User{}, errors.New("oidc claims carry no subject")
	}
	return s.UpsertUser(ctx, claims.Subject, claims.Email, claims.HasGroup(s.oidcAdminGroup))
}

// isAdmin reports whether the proxy's groups carry the admin group, spelled
// as the proxy sends it. An empty group name means the check is off.
func isAdmin(groups []string, adminGroup string) bool {
	return adminGroup == "" || slices.Contains(groups, adminGroup)
}
