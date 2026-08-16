package exttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable shell script that prints body for --version.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' '" + body + "'\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// onlyPath points PATH at dir so the test controls which binaries exist.
func onlyPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func TestResolve_acceptsUniversalCtags(t *testing.T) {
	// Given
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "rg", "ripgrep 15.2.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0, Copyright (C) 2015-2024")
	onlyPath(t, dir)

	// When
	paths, err := Resolve()

	// Then
	if err != nil {
		t.Fatalf("Resolve() err = %v, want nil", err)
	}
	if paths.Ctags != filepath.Join(dir, "ctags") {
		t.Errorf("Ctags = %q, want %q", paths.Ctags, filepath.Join(dir, "ctags"))
	}
}

func TestResolve_rejectsBSDCtags(t *testing.T) {
	// Given: macOS ships this at /usr/bin/ctags. Accepting it would produce an
	// empty symbol index rather than an error, which is far worse.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "rg", "ripgrep 15.2.0")
	fakeBin(t, dir, "ctags", "usage: ctags [-BFTaduwvx] [-f tagsfile] file ...")
	onlyPath(t, dir)

	// When
	_, err := Resolve()

	// Then
	if err == nil {
		t.Fatal("Resolve() err = nil, want a rejection of BSD ctags")
	}
	if !strings.Contains(err.Error(), "universal-ctags") {
		t.Errorf("error = %q, want it to name universal-ctags so the fix is obvious", err)
	}
}

func TestResolve_reportsMissingBinary(t *testing.T) {
	// Given: git and ctags present, rg absent.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0")
	onlyPath(t, dir)

	// When
	_, err := Resolve()

	// Then
	if err == nil {
		t.Fatal("Resolve() err = nil, want an error naming ripgrep")
	}
	if !strings.Contains(err.Error(), "rg") {
		t.Errorf("error = %q, want it to name rg", err)
	}
}
