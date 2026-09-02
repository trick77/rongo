// Package auth identifies callers. Phase 1 ships the dev and token modes; the
// OIDC seam exists so a later phase adds a mode, not a redesign.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
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
}

// NewService builds the auth service. adminToken is only consulted in token
// mode.
func NewService(db *sql.DB, mode string, adminToken string) *Service {
	return &Service{db: db, mode: mode, adminToken: adminToken}
}

// UpsertUser inserts the subject or returns the existing row.
func (s *Service) UpsertUser(subject, email string, isAdmin bool) (User, error) {
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
