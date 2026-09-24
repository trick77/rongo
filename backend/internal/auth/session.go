// Package auth identifies callers. Phase 1 ships the dev and token modes; the
// OIDC seam exists so a later phase adds a mode, not a redesign.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SessionCookie is the cookie carrying the opaque session token.
const SessionCookie = "rongo_session"

// User is an authenticated identity.
type User struct {
	ID      int64
	Subject string
	Email   string
	IsAdmin bool
}

// Service owns users and sessions.
type Service struct {
	db         *sql.DB
	mode       string
	adminToken string
	// The password-mode account. adminUser is compared in constant time,
	// passwordHash is bcrypt.
	adminUser    string
	passwordHash []byte
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

// Admit is UpsertUser for the request path: the row is written once per
// admitTTL per subject and remembered in between, so a page of thirty
// requests is one write, not thirty.
func (s *Service) Admit(subject, email string, isAdmin bool) (User, error) {
	now := time.Now()
	s.admitMu.Lock()
	if a, ok := s.admitted[subject]; ok && now.Sub(a.at) < admitTTL && a.user.Email == email && a.user.IsAdmin == isAdmin {
		s.admitMu.Unlock()
		return a.user, nil
	}
	s.admitMu.Unlock()
	u, err := s.UpsertUser(subject, email, isAdmin)
	if err != nil {
		return User{}, err
	}
	s.admitMu.Lock()
	s.admitted[subject] = admission{user: u, at: now}
	s.admitMu.Unlock()
	return u, nil
}

// SetPasswordAccount installs the one account password mode signs in. The
// hash is bcrypt; config has already refused anything else.
func (s *Service) SetPasswordAccount(user, hash string) {
	s.adminUser = user
	s.passwordHash = []byte(hash)
}

// Mode reports the configured auth mode.
func (s *Service) Mode() string { return s.mode }

// passwordSubject is the fixed identity password mode signs in. Not the
// username: renaming the account in the environment must not orphan the
// threads that hang off users.subject.
const passwordSubject = "admin-password"

// ErrBadCredentials is the one answer a failed password login gets. Username
// and password failures are indistinguishable from outside on purpose.
var ErrBadCredentials = errors.New("bad credentials")

// LoginPassword checks the form credentials and mints a session. The bcrypt
// compare runs even when the username is wrong, so the response time does
// not tell an attacker which half they got right.
func (s *Service) LoginPassword(user, password string) (string, time.Time, error) {
	if s.mode != "password" || len(s.passwordHash) == 0 {
		return "", time.Time{}, ErrBadCredentials
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.adminUser)) == 1
	pwErr := bcrypt.CompareHashAndPassword(s.passwordHash, []byte(password))
	if !userOK || pwErr != nil {
		return "", time.Time{}, ErrBadCredentials
	}
	u, err := s.UpsertUser(passwordSubject, "", true)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := s.CreateSession(u.ID, SessionTTL)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, time.Now().Add(SessionTTL), nil
}

// UpsertUser inserts the subject or returns the existing row.
func (s *Service) UpsertUser(subject, email string, isAdmin bool) (User, error) {
	s.upserts.Add(1)
	admin := 0
	if isAdmin {
		admin = 1
	}
	if _, err := s.db.Exec(
		`INSERT INTO users (subject, email, is_admin) VALUES (?, ?, ?)
		 ON CONFLICT(subject) DO UPDATE SET email = excluded.email, is_admin = excluded.is_admin`,
		subject, email, admin,
	); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	var u User
	var adminInt int
	if err := s.db.QueryRow(
		`SELECT id, subject, email, is_admin FROM users WHERE subject = ?`, subject,
	).Scan(&u.ID, &u.Subject, &u.Email, &adminInt); err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	u.IsAdmin = adminInt == 1
	return u, nil
}

// CreateSession mints a random token, stores only its SHA-256, and returns the
// raw token to hand to the client exactly once.
func (s *Service) CreateSession(userID int64, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, time.Now().Add(ttl).UTC().Format(time.RFC3339),
	); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return token, nil
}

// UserByToken resolves a raw token to its user, rejecting expired sessions.
func (s *Service) UserByToken(token string) (User, bool) {
	var u User
	var adminInt int
	var expiresAt string
	err := s.db.QueryRow(
		`SELECT u.id, u.subject, u.email, u.is_admin, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`,
		hashToken(token),
	).Scan(&u.ID, &u.Subject, &u.Email, &adminInt, &expiresAt)
	if err != nil {
		// A bad or unknown cookie is sql.ErrNoRows — expected, not logged.
		// Anything else is a broken database, and collapsing it into the same
		// silent "not authenticated" would hide that from operators.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("session lookup failed", "err", err)
		}
		return User{}, false
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().After(exp) {
		return User{}, false
	}
	u.IsAdmin = adminInt == 1
	return u, true
}

// DeleteExpiredSessions removes every session past its expiry. Nothing else
// ever does: a session is deleted by its own token on logout and otherwise
// only stops resolving, so the table grew by every login for good. Reports
// how many rows went.
func (s *Service) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}

// DeleteSession revokes one session.
func (s *Service) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionTTL is how long a session stays valid after it is created. Authelia
// runs its own, shorter inactivity window; this one only bounds how long
// rongo's own cookie is worth anything if the provider is never consulted
// again.
const SessionTTL = 30 * 24 * time.Hour

// CreateSessionFromClaims turns a verified OIDC identity into a local user and
// a session, and reports when that session expires.
//
// adminGroup is the group whose members are admins. Empty means no group check
// at all: every user the provider let through is an admin. That is peeq's
// default and the honest one while the authorization decision still lives
// entirely in Authelia's authorization_policy — a group name invented here
// would look like a second gate without being one.
func (s *Service) CreateSessionFromClaims(claims Claims, adminGroup string) (string, time.Time, User, error) {
	if claims.Subject == "" {
		return "", time.Time{}, User{}, errors.New("oidc claims carry no subject")
	}
	u, err := s.UpsertUser(claims.Subject, claims.Email, isAdmin(claims.Groups, adminGroup))
	if err != nil {
		return "", time.Time{}, User{}, err
	}
	token, err := s.CreateSession(u.ID, SessionTTL)
	if err != nil {
		return "", time.Time{}, User{}, err
	}
	return token, time.Now().Add(SessionTTL), u, nil
}

// isAdmin reports whether the claims carry the admin group. An empty group name
// means the check is off; see CreateSessionFromClaims.
func isAdmin(groups []string, adminGroup string) bool {
	if adminGroup == "" {
		return true
	}
	for _, g := range groups {
		if g == adminGroup {
			return true
		}
	}
	return false
}
