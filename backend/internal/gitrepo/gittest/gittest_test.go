package gittest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestInit_isAnEmptyRepositoryOnMain(t *testing.T) {
	dir := Init(t)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf(".git: %v", err)
	}
	if got := Run(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
}

func TestCommit_writesNestedPathAndReturnsHEAD(t *testing.T) {
	dir := Init(t)
	got := Commit(t, dir, "dir/sub/a.txt", []byte("body\n"), "first")
	if !sha.MatchString(got) {
		t.Fatalf("sha = %q", got)
	}
	if head := Run(t, dir, "rev-parse", "HEAD"); head != got {
		t.Errorf("HEAD = %s, want %s", head, got)
	}
	if body := Run(t, dir, "show", got+":dir/sub/a.txt"); body != "body" {
		t.Errorf("committed body = %q", body)
	}
	if author := Run(t, dir, "log", "-1", "--format=%an <%ae>"); author != "t <t@example.invalid>" {
		t.Errorf("author = %q, want the fixed identity", author)
	}
}

func TestFixture_hasOneCommitNamedFirst(t *testing.T) {
	dir := Fixture(t)
	if got := Run(t, dir, "log", "--format=%s"); got != "first" {
		t.Errorf("log = %q, want one commit \"first\"", got)
	}
	if got := Run(t, dir, "show", "HEAD:a.txt"); got != "first" {
		t.Errorf("a.txt = %q", got)
	}
}

func TestRun_turnsAutomaticMaintenanceOff(t *testing.T) {
	dir := Init(t)
	if got := Run(t, dir, "config", "gc.auto"); got != "0" {
		t.Errorf("gc.auto = %q, want 0", got)
	}
	if got := Run(t, dir, "config", "maintenance.auto"); got != "false" {
		t.Errorf("maintenance.auto = %q, want false", got)
	}
}

// recorder swallows Fatalf so a failing git command can be observed instead
// of ending the test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Helper()                   {}
func (r *recorder) Fatalf(f string, _ ...any) { r.msg = f }
func (r *recorder) Fatal(_ ...any)            { r.msg = "fatal" }

func TestRun_failsTheTestOnAGitError(t *testing.T) {
	rec := &recorder{TB: t}
	Run(rec, Init(t), "no-such-subcommand")
	if rec.msg == "" {
		t.Error("Run() did not fail the test")
	}
	rec.msg = ""
	Commit(rec, t.TempDir(), "a.txt", nil, "x")
	if rec.msg == "" {
		t.Error("Commit() outside a repository did not fail the test")
	}
}
