package config

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// testHash is a real bcrypt hash at the lowest cost: the shape is what the
// boot check cares about, and the low cost keeps the test fast.
var testHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

func TestLoad_passwordModeRequiresUserAndHash(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no user": {"BACKEND_ADMIN_PASSWORD_HASH": testHash},
		"no hash": {"BACKEND_ADMIN_USER": "admin"},
	} {
		t.Run(name, func(t *testing.T) {
			env["BACKEND_AUTH_MODE"] = "password"
			setEnv(t, env)

			_, err := Load()

			if err == nil {
				t.Fatal("Load() err = nil, want an error about the password block")
			}
		})
	}
}

// A plaintext pasted into the hash variable would fail every login with a
// generic 401 and look like a typo in the password. The boot names it.
func TestLoad_passwordModeRejectsAPlaintextHash(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_AUTH_MODE":           "password",
		"BACKEND_ADMIN_USER":          "admin",
		"BACKEND_ADMIN_PASSWORD_HASH": "hunter2",
	})

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), "not a bcrypt hash") {
		t.Fatalf("Load() err = %v, want an error naming the hash format", err)
	}
}

func TestLoad_passwordModeCookieSecureDefaultsOn(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_AUTH_MODE":           "password",
		"BACKEND_ADMIN_USER":          "admin",
		"BACKEND_ADMIN_PASSWORD_HASH": testHash,
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure = false, want true by default")
	}
	if cfg.AdminUser != "admin" || cfg.AdminPasswordHash != testHash {
		t.Errorf("account = %q/%q, want admin/%q", cfg.AdminUser, cfg.AdminPasswordHash, testHash)
	}
}

// Plain-HTTP cookies are a local-development concession, like dev mode
// itself: a listener anyone can reach does not get them.
func TestLoad_passwordModeCookieSecureOffNeedsLoopback(t *testing.T) {
	base := map[string]string{
		"BACKEND_AUTH_MODE":           "password",
		"BACKEND_ADMIN_USER":          "admin",
		"BACKEND_ADMIN_PASSWORD_HASH": testHash,
		"BACKEND_COOKIE_SECURE":       "false",
	}

	setEnv(t, base)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("loopback: Load() err = %v", err)
	}
	if cfg.CookieSecure {
		t.Error("loopback: CookieSecure = true, want false")
	}

	base["BACKEND_ADDR"] = ":8080"
	setEnv(t, base)
	if _, err := Load(); err == nil {
		t.Fatal("non-loopback: Load() err = nil, want a refusal")
	}
}
