// Package exttools locates the external binaries rongo shells out to and
// verifies they are the ones it actually needs.
//
// The dev environment runs without a container, so the binaries come from the
// developer's machine and cannot be assumed correct. ctags in particular: macOS
// ships Apple's BSD ctags at /usr/bin/ctags, which rejects long options. Using
// it would yield an empty symbol index instead of an error, so rongo refuses to
// start rather than indexing silently wrong.
package exttools

import (
	"fmt"
	"os/exec"
	"strings"
)

// Paths holds the resolved absolute paths of the external tools.
type Paths struct {
	Git   string
	Rg    string
	Ctags string
}

// Resolve finds every required binary and validates ctags. It returns the
// first problem it finds, phrased so the fix is obvious from the message.
func Resolve() (Paths, error) {
	var p Paths
	var err error

	if p.Git, err = exec.LookPath("git"); err != nil {
		return Paths{}, fmt.Errorf("git not found in PATH: %w", err)
	}
	if p.Rg, err = exec.LookPath("rg"); err != nil {
		return Paths{}, fmt.Errorf("ripgrep (rg) not found in PATH: %w", err)
	}
	if p.Ctags, err = exec.LookPath("ctags"); err != nil {
		return Paths{}, fmt.Errorf("ctags not found in PATH (install universal-ctags): %w", err)
	}
	if err := verifyUniversalCtags(p.Ctags); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// verifyUniversalCtags checks the banner rather than trusting the filename.
func verifyUniversalCtags(path string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	banner := firstLine(string(out))
	if err != nil {
		return fmt.Errorf(
			"%s --version failed (%q); macOS ships BSD ctags at /usr/bin/ctags — install universal-ctags (brew install universal-ctags)",
			path, banner)
	}
	if !strings.Contains(string(out), "Universal Ctags") {
		return fmt.Errorf(
			"%s is not universal-ctags (reports %q); install universal-ctags (brew install universal-ctags) and make sure it precedes /usr/bin on PATH",
			path, banner)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
