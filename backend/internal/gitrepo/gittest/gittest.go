// Package gittest builds the local git fixtures every git-backed test starts
// from. Test-only: nothing outside a _test.go file imports it. It imports
// nothing of rongo, so gitrepo's own tests can use it without a cycle.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run runs git in dir with a fixed identity, so a developer's own config
// cannot change what a fixture looks like, and returns the trimmed output.
// Automatic maintenance is off: a commit may otherwise detach a background gc
// that is still writing under .git while t.TempDir removes it, which failed
// once on CI as "directory not empty".
func Run(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // the arguments are the test's own fixture commands, not request data
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Commit writes body to name under dir, creating parent directories, commits
// it with msg and returns the new HEAD.
func Commit(tb testing.TB, dir, name string, body []byte, msg string) string {
	tb.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o600); err != nil {
		tb.Fatal(err)
	}
	Run(tb, dir, "add", name)
	Run(tb, dir, "commit", "-qm", msg)
	return Run(tb, dir, "rev-parse", "HEAD")
}

// Init creates an empty repository on branch main under the test's temp dir.
func Init(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	Run(tb, dir, "init", "-q", "-b", "main")
	return dir
}

// Fixture is a repository on main with one commit, "first", adding a.txt. The
// "remote" is a directory on disk, so no test ever touches the network.
func Fixture(tb testing.TB) string {
	tb.Helper()
	dir := Init(tb)
	Commit(tb, dir, "a.txt", []byte("first\n"), "first")
	return dir
}
