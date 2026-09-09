package repos

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_missingFileIsDistinguishableFromAnInvalidOne is what lets main.go
// treat the two differently, and the difference is the whole point.
//
// An INVALID file stops the server. It parses to nothing, so SyncSpecs never
// runs, the previous list stays in the database, and rongo would otherwise come
// up serving a complete, confident, arbitrarily stale corpus with nothing in
// the UI saying the configuration was refused — the Repos page cannot report a
// file it never loaded, it can only show what the database still holds, which
// is the LAST good file. That happened: a stray character on line one froze the
// corpus for nine hours and surfaced as a `uses` arrow pointing the wrong way.
//
// A MISSING file is a different fact — a first run before conf/ is populated,
// or a mount that is not there yet. Nothing is stale because nothing was ever
// loaded, so rongo comes up, indexes nothing, and says there is no list.
func TestLoad_missingFileIsDistinguishableFromAnInvalidOne(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		// Given: a path with no file at it
		path := filepath.Join(t.TempDir(), "absent.yaml")

		// When
		_, err := Load(path)

		// Then
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Load() err = %v, want it to wrap fs.ErrNotExist", err)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		// Given: the real defect, verbatim — a stray character on line one
		path := writeYAML(t, `7projects:
  - name: shop
    repositories:
      - name: shop-ui
        clone_url: https://forge.example.invalid/acme/shop-ui.git
`)

		// When
		_, err := Load(path)

		// Then
		if err == nil {
			t.Fatal("Load() err = nil, want a refusal")
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Load() err = %v, want it NOT to read as a missing file", err)
		}
		if !strings.Contains(err.Error(), "7projects") {
			t.Errorf("Load() err = %v, want it to name the offending key", err)
		}
	})

	t.Run("present but empty", func(t *testing.T) {
		// A file that exists and names no project is INVALID, not missing: it is
		// the truncated-deploy case repos.Load already refuses by name, and it
		// must stop the server rather than read as "nothing configured yet".
		path := writeYAML(t, "")

		_, err := Load(path)

		if err == nil {
			t.Fatal("Load() err = nil, want a refusal")
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Load() err = %v, want it NOT to read as a missing file", err)
		}
	})
}
