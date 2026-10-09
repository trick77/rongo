package main

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

func TestResolveExtTools_acceptsUniversalCtags(t *testing.T) {
	// Given
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0, Copyright (C) 2015-2024\njson supports json format output")
	onlyPath(t, dir)

	// When
	paths, err := resolveExtTools()

	// Then
	if err != nil {
		t.Fatalf("resolveExtTools() err = %v, want nil", err)
	}
	if paths.Ctags != filepath.Join(dir, "ctags") {
		t.Errorf("Ctags = %q, want %q", paths.Ctags, filepath.Join(dir, "ctags"))
	}
}

func TestResolveExtTools_rejectsBSDCtags(t *testing.T) {
	// Given: macOS ships this at /usr/bin/ctags. Accepting it would produce an
	// empty symbol index rather than an error, which is far worse.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "ctags", "usage: ctags [-BFTaduwvx] [-f tagsfile] file ...")
	onlyPath(t, dir)

	// When
	_, err := resolveExtTools()

	// Then
	if err == nil {
		t.Fatal("resolveExtTools() err = nil, want a rejection of BSD ctags")
	}
	if !strings.Contains(err.Error(), "universal-ctags") {
		t.Errorf("error = %q, want it to name universal-ctags so the fix is obvious", err)
	}
}

func TestResolveExtTools_reportsMissingBinary(t *testing.T) {
	// Given: git present, ctags absent.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	onlyPath(t, dir)

	// When
	_, err := resolveExtTools()

	// Then
	if err == nil {
		t.Fatal("resolveExtTools() err = nil, want an error naming ctags")
	}
	if !strings.Contains(err.Error(), "ctags not found in PATH") {
		t.Errorf("error = %q, want it to say ctags is not on PATH", err)
	}
}

func TestResolveExtTools_reportsMissingGit(t *testing.T) {
	// Given: ctags present, git absent.
	dir := t.TempDir()
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0, Copyright (C) 2015-2024\njson supports json format output")
	onlyPath(t, dir)

	// When
	_, err := resolveExtTools()

	// Then
	if err == nil || !strings.Contains(err.Error(), "git not found in PATH") {
		t.Errorf("error = %v, want it to say git is not on PATH", err)
	}
}

func TestResolveExtTools_rejectsAUniversalCtagsBuiltWithoutJSON(t *testing.T) {
	// Given: the right ctags, built without the json feature. Every extraction
	// asks for --output-format=json, so this one fails on every file, the
	// caller falls back to line windows, and the symbol index ends up empty
	// with nothing at startup saying so — the silent outcome the banner check
	// exists to prevent.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0, Copyright (C) 2015-2024\nregex can use regular expression based pattern matching")
	onlyPath(t, dir)

	// When
	_, err := resolveExtTools()

	// Then
	if err == nil {
		t.Fatal("resolveExtTools() err = nil, want a rejection of a ctags without json output")
	}
	if !strings.Contains(err.Error(), "json") {
		t.Errorf("error = %q, want it to name the missing json feature", err)
	}
}
