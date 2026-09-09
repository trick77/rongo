package gitrepo

import (
	"context"
	"strings"
	"testing"
)

// TestReadFile_andObject_reportWhatTheyCouldNotRead covers the failure side of
// the two commands that build their own exec.Command rather than going through
// run() — the pair that has to carry the safe.directory exemption on its own.
//
// The messages matter as much as the errors: a citation that will not open is
// reported to whoever is reading an answer, and "read X at Y" naming the path
// and the short sha is the difference between a diagnosable report and "it
// broke".
func TestReadFile_andObject_reportWhatTheyCouldNotRead(t *testing.T) {
	// Given: a snapshot with one committed file
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{"a.go": "package a\n"})
	sha, err := c.EnsureSnapshot(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureSnapshot() err = %v", err)
	}

	t.Run("ReadFile on a path the commit does not have", func(t *testing.T) {
		// When
		_, err := c.ReadFile(context.Background(), spec, sha, "nope/missing.go")

		// Then
		if err == nil {
			t.Fatal("ReadFile() err = nil, want a failure")
		}
		if !strings.Contains(err.Error(), "nope/missing.go") {
			t.Errorf("ReadFile() err = %v, want it to name the path", err)
		}
		if !strings.Contains(err.Error(), ShortSHA(sha)) {
			t.Errorf("ReadFile() err = %v, want it to name the commit", err)
		}
	})

	t.Run("Object on a path the commit does not have", func(t *testing.T) {
		// cat-file --batch-check answers "<name> missing" and exits 0, so this
		// is the two-field branch rather than a command failure.
		_, _, err := c.Object(context.Background(), spec, sha, "nope/missing.go")

		if err == nil {
			t.Fatal("Object() err = nil, want a failure")
		}
		if !strings.Contains(err.Error(), "nope/missing.go") {
			t.Errorf("Object() err = %v, want it to name the path", err)
		}
	})

	t.Run("Object on a commit that does not exist", func(t *testing.T) {
		_, _, err := c.Object(context.Background(), spec,
			"0000000000000000000000000000000000000000", "a.go")

		if err == nil {
			t.Fatal("Object() err = nil, want a failure")
		}
	})

	t.Run("Object on a file it does have", func(t *testing.T) {
		// The success path beside them, so a change that broke parsing could
		// not pass by making everything fail.
		kind, size, err := c.Object(context.Background(), spec, sha, "a.go")

		if err != nil {
			t.Fatalf("Object() err = %v", err)
		}
		if kind != "blob" {
			t.Errorf("kind = %q, want blob", kind)
		}
		if size != int64(len("package a\n")) {
			t.Errorf("size = %d, want %d", size, len("package a\n"))
		}
	})
}
