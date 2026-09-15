package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthURL_defaultsTheUsernameToGitHubs(t *testing.T) {
	// Given: an entry that names no token_user, which is every entry written
	// before the field existed. It must keep fetching exactly as it did.
	got := authURL("https://github.com/acme/repo.git", "", "ghp_secret")

	if got != "https://x-access-token:ghp_secret@github.com/acme/repo.git" {
		t.Errorf("authURL() = %q, want x-access-token as the user", got)
	}
}

func TestAuthURL_sendsTheNamedUsername(t *testing.T) {
	// Given: a Bitbucket project token, refused under any username but
	// x-token-auth.
	got := authURL("https://bitbucket.example.invalid/scm/shop/backend.git", "x-token-auth", "secret")

	if got != "https://x-token-auth:secret@bitbucket.example.invalid/scm/shop/backend.git" {
		t.Errorf("authURL() = %q, want x-token-auth as the user", got)
	}
}

func TestAuthURL_leavesAnSSHRemoteAlone(t *testing.T) {
	raw := "ssh://git@bitbucket.example.invalid:7999/shop/backend.git"

	if got := authURL(raw, "x-token-auth", "secret"); got != raw {
		t.Errorf("authURL() = %q, want the ssh remote untouched", got)
	}
}

// fakeGit is a git that only records its environment, so a test can see what
// run hands git without a remote to hand it to.
func fakeGit(t *testing.T) (bin, envFile string) {
	t.Helper()
	dir := t.TempDir()
	envFile = filepath.Join(dir, "env")
	bin = filepath.Join(dir, "git")
	script := "#!/bin/sh\nenv > " + envFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, envFile
}

func TestRun_handsGitTheConfiguredSSHCommandAndCA(t *testing.T) {
	// Given: the container's shape — a mounted key, its host's known_hosts
	// and an internal CA, none of which git would find on its own there.
	bin, envFile := fakeGit(t)
	c := New(bin, t.TempDir()).WithAuth(Auth{
		SSHKey:        "/git-auth/id_ed25519",
		SSHKnownHosts: "/git-auth/known hosts",
		CAFile:        "/git-auth/ca.pem",
	})

	// When
	if _, err := c.run(context.Background(), c.root, "fetch"); err != nil {
		t.Fatalf("run() err = %v", err)
	}

	// Then
	env := readEnv(t, envFile)
	ssh := env["GIT_SSH_COMMAND"]
	for _, want := range []string{
		"-i '/git-auth/id_ed25519'",
		`'UserKnownHostsFile="/git-auth/known hosts"'`,
		"StrictHostKeyChecking=yes",
		"BatchMode=yes",
		"IdentitiesOnly=yes",
	} {
		if !strings.Contains(ssh, want) {
			t.Errorf("GIT_SSH_COMMAND = %q, missing %q", ssh, want)
		}
	}
	if got := env["GIT_SSL_CAINFO"]; got != "/git-auth/ca.pem" {
		t.Errorf("GIT_SSL_CAINFO = %q, want the CA file", got)
	}
	if got := env["GIT_TERMINAL_PROMPT"]; got != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0 kept alongside", got)
	}
}

func TestRun_leavesGitsDefaultsWhenNothingIsConfigured(t *testing.T) {
	// Given: a bare host, where the operator's own agent and ~/.ssh are what
	// make an ssh remote work. Naming an ssh command would take that away.
	bin, envFile := fakeGit(t)
	c := New(bin, t.TempDir())
	for _, name := range []string{"GIT_SSH_COMMAND", "GIT_SSL_CAINFO"} {
		t.Setenv(name, "") // restores the developer's own value afterwards
		os.Unsetenv(name)
	}

	// When
	if _, err := c.run(context.Background(), c.root, "fetch"); err != nil {
		t.Fatalf("run() err = %v", err)
	}

	// Then
	env := readEnv(t, envFile)
	for _, name := range []string{"GIT_SSH_COMMAND", "GIT_SSL_CAINFO"} {
		if v, ok := env[name]; ok {
			t.Errorf("%s = %q, want unset", name, v)
		}
	}
}

func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}
