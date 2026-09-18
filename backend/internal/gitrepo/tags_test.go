package gitrepo

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// gitOut runs git in dir and returns its trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// taggedFixture is a remote with two commits on main, a lightweight tag on
// the first, an annotated tag on the second, and a third commit on a side
// branch tagged too. Cloned through the client so the test reads tags the
// way production does: from the checkout, never from the source directory.
func taggedFixture(t *testing.T) (*Client, repos.Spec, map[string]string) {
	t.Helper()
	src := fixtureRepo(t)
	gitRun(t, src, "tag", "1.0.0")
	writeAndCommit(t, src, "b.txt", "second\n", "second")
	gitRun(t, src, "tag", "-a", "v1.1.0", "-m", "release 1.1.0")
	gitRun(t, src, "checkout", "-qb", "hotfix", "1.0.0")
	writeAndCommit(t, src, "c.txt", "hotfix\n", "hotfix")
	gitRun(t, src, "tag", "1.0.1")
	gitRun(t, src, "checkout", "-q", "main")
	shas := map[string]string{}
	for _, tag := range []string{"1.0.0", "v1.1.0", "1.0.1"} {
		shas[tag] = gitOut(t, src, "rev-list", "-n", "1", tag)
	}
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	if err := c.EnsureCloned(context.Background(), spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	return c, spec, shas
}

func TestResolveTag_readsLightweightAnnotatedAndVPrefixed(t *testing.T) {
	c, spec, shas := taggedFixture(t)
	ctx := context.Background()
	cases := map[string]string{
		"1.0.0":  shas["1.0.0"],  // lightweight, as written
		"v1.1.0": shas["v1.1.0"], // annotated: the commit, never the tag object
		"1.1.0":  shas["v1.1.0"], // the image tag without the v the repo writes
	}
	for tag, want := range cases {
		got, err := c.ResolveTag(ctx, spec, tag)
		if err != nil {
			t.Fatalf("ResolveTag(%q) err = %v", tag, err)
		}
		if got != want {
			t.Errorf("ResolveTag(%q) = %s, want %s", tag, got, want)
		}
	}
}

func TestResolveTag_aShaShapedTagIsTheCommitItself(t *testing.T) {
	c, spec, shas := taggedFixture(t)
	got, err := c.ResolveTag(context.Background(), spec, shas["1.0.0"][:7])
	if err != nil {
		t.Fatalf("ResolveTag(short sha) err = %v", err)
	}
	if got != shas["1.0.0"] {
		t.Errorf("ResolveTag(short sha) = %s, want %s", got, shas["1.0.0"])
	}
}

func TestResolveTag_unknownIsErrTagUnknown(t *testing.T) {
	c, spec, _ := taggedFixture(t)
	_, err := c.ResolveTag(context.Background(), spec, "9.9.9")
	if !errors.Is(err, ErrTagUnknown) {
		t.Fatalf("ResolveTag(unknown) err = %v, want ErrTagUnknown", err)
	}
}

func TestFetch_bringsNewTagsAndDropsDeletedOnes(t *testing.T) {
	c, spec, _ := taggedFixture(t)
	ctx := context.Background()
	src := spec.CloneURL
	gitRun(t, src, "tag", "-d", "1.0.0")
	gitRun(t, src, "tag", "2.0.0")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	if _, err := c.ResolveTag(ctx, spec, "2.0.0"); err != nil {
		t.Errorf("a tag pushed after the clone did not arrive: %v", err)
	}
	if _, err := c.ResolveTag(ctx, spec, "1.0.0"); !errors.Is(err, ErrTagUnknown) {
		t.Errorf("a tag deleted upstream is still resolved: err = %v", err)
	}
}

func TestIsAncestor_tellsForwardReverseAndDiverged(t *testing.T) {
	c, spec, shas := taggedFixture(t)
	ctx := context.Background()
	check := func(a, b string, want bool) {
		t.Helper()
		got, err := c.IsAncestor(ctx, spec, shas[a], shas[b])
		if err != nil {
			t.Fatalf("IsAncestor(%s, %s) err = %v", a, b, err)
		}
		if got != want {
			t.Errorf("IsAncestor(%s, %s) = %v, want %v", a, b, got, want)
		}
	}
	check("1.0.0", "v1.1.0", true)  // forward
	check("v1.1.0", "1.0.0", false) // reverse
	check("v1.1.0", "1.0.1", false) // diverged: the hotfix branched before 1.1.0
	check("1.0.0", "1.0.1", true)   // the hotfix still descends from 1.0.0
}

func TestResolveTag_aMissingCheckoutIsAnErrorNotAMissingTag(t *testing.T) {
	c := newClient(t)
	spec := repos.Spec{Name: "absent", CloneURL: "file:///nowhere", Branch: "main", Enabled: true}
	_, err := c.ResolveTag(context.Background(), spec, "1.0.0")
	if err == nil || errors.Is(err, ErrTagUnknown) {
		t.Fatalf("ResolveTag(no checkout) err = %v, want a real error, not ErrTagUnknown", err)
	}
}

func TestIsAncestor_aMissingObjectIsAnErrorNotANo(t *testing.T) {
	c, spec, shas := taggedFixture(t)
	_, err := c.IsAncestor(context.Background(), spec, "0000000000000000000000000000000000000000", shas["1.0.0"])
	if err == nil {
		t.Fatal("IsAncestor(missing object) err = nil, want the exit 128 reported")
	}
}
