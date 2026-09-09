package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// dropSource writes a plain directory of files where a snapshot's checkout
// belongs — an extracted archive, with no .git anywhere in it.
func dropSource(t *testing.T, c *Client, name string, files map[string]string) repos.Spec {
	t.Helper()
	spec := repos.Spec{Name: name, Snapshot: true, Enabled: true}
	dir := c.Dir(spec)
	for path, body := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return spec
}

// TestEnsureSnapshot_commitsAPlainDirectory: the whole point. An archive has no
// history, and every read past this point is `git show <sha>:<path>` — so the
// commit is what gives a drop something citable.
func TestEnsureSnapshot_commitsAPlainDirectory(t *testing.T) {
	// Given
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{
		"go.mod":         "module acme\n",
		"internal/a.go":  "package a\n",
		"docs/README.md": "# acme\n",
	})

	// When
	sha, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v, want nil", err)
	}
	if len(sha) < 7 {
		t.Fatalf("EnsureSnapshot() sha = %q, want a commit", sha)
	}
	paths, err := c.ListPaths(context.Background(), spec, sha)
	if err != nil {
		t.Fatalf("ListPaths() err = %v", err)
	}
	got := strings.Join(paths, " ")
	for _, want := range []string{"go.mod", "internal/a.go", "docs/README.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("ListPaths() = %v, want it to list %q", paths, want)
		}
	}
	// The branch is named, not defaulted: never master, and the label the Repos
	// page shows has to be true of the checkout.
	body, err := c.ReadFile(context.Background(), spec, sha, "go.mod")
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(body) != "module acme\n" {
		t.Errorf("ReadFile() = %q, want the extracted content", body)
	}
}

// TestEnsureSnapshot_isIdempotent: this is the one-off promise. A second poll
// over an untouched drop must return the same commit, so the poller sees
// "unchanged" and never re-indexes.
func TestEnsureSnapshot_isIdempotent(t *testing.T) {
	// Given
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{"a.go": "package a\n"})
	first, err := c.EnsureSnapshot(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v", err)
	}

	// When
	second, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v, want nil", err)
	}
	if second != first {
		t.Errorf("EnsureSnapshot() sha = %q, want the unchanged %q", second, first)
	}
}

// TestEnsureSnapshot_reExtractedDropIsADiff: extracting a newer archive over the
// directory has to cost an incremental index, not a full one — the commits share
// an object store, so ChangedPaths works exactly as it does for a fetch.
func TestEnsureSnapshot_reExtractedDropIsADiff(t *testing.T) {
	// Given
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
	})
	first, err := c.EnsureSnapshot(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir(spec), "a.go"), []byte("package a // v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	second, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v, want nil", err)
	}
	if second == first {
		t.Fatal("EnsureSnapshot() returned the old sha for a changed drop")
	}
	changed, err := c.ChangedPaths(context.Background(), spec, first, second)
	if err != nil {
		t.Fatalf("ChangedPaths() err = %v", err)
	}
	if len(changed) != 1 || changed[0] != "a.go" {
		t.Errorf("ChangedPaths() = %v, want [a.go]", changed)
	}
}

// TestEnsureSnapshot_missingAndEmptyAreLoud: a directory nobody extracted into
// is the ordinary setup mistake, and it must reach the Repos page as an error
// rather than as an empty index that looks healthy.
func TestEnsureSnapshot_missingAndEmptyAreLoud(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, c *Client, spec repos.Spec)
	}{
		{"missing", func(*testing.T, *Client, repos.Spec) {}},
		{"empty", func(t *testing.T, c *Client, spec repos.Spec) {
			if err := os.MkdirAll(c.Dir(spec), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			c := newClient(t)
			spec := repos.Spec{Name: "acme-core", Snapshot: true, Enabled: true}
			tc.prepare(t, c, spec)

			// When
			_, err := c.EnsureSnapshot(context.Background(), spec)

			// Then
			if err == nil {
				t.Fatal("EnsureSnapshot() err = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), c.Dir(spec)) {
				t.Errorf("EnsureSnapshot() err = %v, want it to name the directory", err)
			}
		})
	}
}

// TestEnsureSnapshot_refusesAClone: flipping a remote entry to snapshot: true
// leaves its clone sitting in the directory. Committing over it would relabel a
// real repository's branch "snapshot" and leave origin in place — a label
// contradicting the checkout it names.
func TestEnsureSnapshot_refusesAClone(t *testing.T) {
	// Given: a genuine clone where the drop should be
	c := newClient(t)
	source := fixtureRepo(t)
	remote := repos.Spec{Name: "acme-core", CloneURL: source, Enabled: true}
	if err := c.EnsureCloned(context.Background(), remote, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	spec := repos.Spec{Name: "acme-core", Snapshot: true, Enabled: true}

	// When
	_, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err == nil {
		t.Fatal("EnsureSnapshot() err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "clone") {
		t.Errorf("EnsureSnapshot() err = %v, want it to say a clone is in the way", err)
	}
}

// TestEnsureSnapshot_dropWithGitignoreSkipsWhatItNames: a source archive carries
// only tracked files, so honouring .gitignore excludes nothing that is present —
// except build output somebody unpacked beside it, which is the right call.
func TestEnsureSnapshot_dropWithGitignoreSkipsWhatItNames(t *testing.T) {
	// Given
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{
		".gitignore":  "dist/\n",
		"a.go":        "package a\n",
		"dist/out.js": "// built\n",
	})

	// When
	sha, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v, want nil", err)
	}
	paths, err := c.ListPaths(context.Background(), spec, sha)
	if err != nil {
		t.Fatalf("ListPaths() err = %v", err)
	}
	if got := strings.Join(paths, " "); strings.Contains(got, "dist/out.js") {
		t.Errorf("ListPaths() = %v, want dist/ left out", paths)
	}
}
