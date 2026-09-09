// Package gitrepo drives the git binary. rongo clones and owns its checkouts
// rather than reading through a forge API: an API cannot grep, per-file fetches
// rate-limit at this corpus size, and the "why is it like this" pipeline needs
// local history.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/repos"
)

// ErrBranchGone reports that a configured branch does not exist on the remote.
// It is a named error because the caller must surface it loudly: a silent stop
// freezes the index while every status looks healthy, and answers then come
// from months-old code.
var ErrBranchGone = errors.New("configured branch not found")

// Client runs git commands against checkouts under root.
type Client struct {
	git  string
	root string
}

// New builds a Client. gitBin comes from exttools.Resolve, which has already
// verified the binary exists.
func New(gitBin, root string) *Client {
	return &Client{git: gitBin, root: root}
}

// Dir is where a repository's checkout lives. repos.Load has already validated
// that Name is a single path segment, so this cannot escape root.
func (c *Client) Dir(spec repos.Spec) string {
	return filepath.Join(c.root, spec.Name)
}

// EnsureCloned clones the repository if it is not present. The clone keeps full
// history because the "why" pipeline reads it, and source is cheap on disk.
func (c *Client) EnsureCloned(ctx context.Context, spec repos.Spec, token string) error {
	dir := c.Dir(spec)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return fmt.Errorf("create repository root: %w", err)
	}
	if _, err := c.run(ctx, c.root, "clone", "--quiet", authURL(spec.CloneURL, token), dir); err != nil {
		return err
	}
	// git PERSISTS the clone URL as remote.origin.url, credentials and all, so
	// a token would sit in .git/config on disk from here on. Every later
	// command passes its own URL, so the stored one is only ever a liability:
	// overwrite it with the bare URL immediately.
	if token != "" {
		if _, err := c.run(ctx, dir, "remote", "set-url", "origin", spec.CloneURL); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotBranch is the ref a snapshot's commits sit on, and the branch label
// its citations carry. Named rather than defaulted: `git init` would pick master
// or main from the machine's own configuration, and that value travels with
// every citation. There is no upstream branch to be faithful to, so the honest
// label is what this is.
const SnapshotBranch = "snapshot"

// EnsureSnapshot makes a hand-extracted source drop citable, and reports the
// commit to index.
//
// A drop is an archive: files, no .git, no remote, no history. Everything past
// here reads code as `git show <sha>:<path>` and every citation carries a sha,
// so the directory needs one commit before it is a repository rongo can answer
// from. This makes that commit, and on a later call makes another only if the
// files changed — extracting a newer archive over the drop therefore costs an
// incremental index, and leaving it alone costs nothing at all.
//
// It never fetches and never resolves a default branch, because there is no
// remote to ask. A drop stays exactly as it was extracted until it is extracted
// again, which is the whole point of indexing one.
func (c *Client) EnsureSnapshot(ctx context.Context, spec repos.Spec) (string, error) {
	dir := c.Dir(spec)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing directory is the ordinary case — nobody has extracted the
		// archive yet — and it reaches the Repos page verbatim, because a silent
		// empty index would look healthy instead. Anything else (a permission
		// problem, an I/O error) is reported as itself: telling an operator to
		// extract an archive that is already sitting there sends them nowhere.
		if os.IsNotExist(err) {
			return "", fmt.Errorf(
				"snapshot %s: %s does not exist — extract the source archive there, with the archive's own top-level folder unpacked away",
				spec.Name, dir)
		}
		return "", fmt.Errorf("snapshot %s: read %s: %w", spec.Name, dir, err)
	}
	// .git does not count. `rm -rf <dir>/*` leaves it behind, because the glob
	// skips dotfiles — and a directory holding nothing else would otherwise be
	// committed as the deletion of every file, leaving the Repos page showing a
	// repository with 0 files and no error at all. That is precisely the silent
	// empty index this guard exists to prevent.
	if len(entries) == 0 || (len(entries) == 1 && entries[0].Name() == ".git") {
		return "", fmt.Errorf(
			"snapshot %s: %s holds no source — extract the source archive there, with the archive's own top-level folder unpacked away",
			spec.Name, dir)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if err := c.assertSnapshotCheckout(ctx, spec, dir); err != nil {
			return "", err
		}
	} else if _, err := c.run(ctx, dir, "init", "-q", "-b", SnapshotBranch); err != nil {
		return "", err
	}

	if _, err := c.run(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	// Commit only when the staged tree differs from HEAD. An unchanged drop must
	// return the SAME sha, or every poll would write an empty commit and the
	// poller would re-index a repository nobody touched. --quiet exits 1 for
	// "there is a difference", which is not an error here.
	changed, err := c.snapshotDiffers(ctx, dir)
	if err != nil {
		return "", err
	}
	if changed {
		if _, err := c.run(ctx, dir,
			"-c", "user.name=rongo", "-c", "user.email=rongo@localhost",
			"commit", "-q", "-m", "snapshot"); err != nil {
			return "", err
		}
	}

	out, err := c.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// assertSnapshotCheckout refuses a directory holding a clone.
//
// Flipping an entry from a clone_url to snapshot: true leaves the old checkout
// in place. Committing into it would add a commit to a real repository, label
// its branch "snapshot" on the Repos page and leave origin pointing at a remote
// nothing fetches any more — a checkout the page describes wrongly in every
// column. The operator removes it; rongo will not decide that for them.
func (c *Client) assertSnapshotCheckout(ctx context.Context, spec repos.Spec, dir string) error {
	if out, err := c.run(ctx, dir, "remote"); err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf(
			"snapshot %s: %s holds a clone with remote %q, not an extracted archive — remove the directory before indexing it as a snapshot",
			spec.Name, dir, strings.Fields(out)[0])
	}
	out, err := c.run(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		// A detached HEAD has no symbolic ref. That is not a snapshot either:
		// its commits could not be committed onto a branch.
		return fmt.Errorf(
			"snapshot %s: %s holds a git repository with a detached HEAD, not an extracted archive — remove the directory before indexing it as a snapshot",
			spec.Name, dir)
	}
	if branch := strings.TrimSpace(out); branch != SnapshotBranch {
		return fmt.Errorf(
			"snapshot %s: %s holds a git repository on branch %q, not an extracted archive — remove the directory before indexing it as a snapshot",
			spec.Name, dir, branch)
	}
	return nil
}

// snapshotDiffers reports whether the staged tree differs from HEAD. An unborn
// HEAD — the first commit — counts as a difference: there is nothing to compare
// against and everything to record.
func (c *Client) snapshotDiffers(ctx context.Context, dir string) (bool, error) {
	if _, err := c.run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return true, nil
	}
	_, err := c.run(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	// --quiet exits 1 for "differences found" and says nothing on stderr;
	// anything else is a real failure and must not read as "there are changes".
	if strings.Contains(err.Error(), "exit status 1") {
		return true, nil
	}
	return false, err
}

// HasCommit reports whether a sha is in the checkout's object store.
//
// A remote repository never needs this: a fetch only adds objects, so a
// recorded last_sha is always still there. A snapshot can lose one — deleting
// the drop and extracting a newer archive gives it a fresh `git init` and an
// empty object store, after which diffing against the recorded commit fails
// with "bad object" on every cycle and the entry never recovers. The caller
// asks first and re-indexes in full instead.
func (c *Client) HasCommit(ctx context.Context, spec repos.Spec, sha string) bool {
	_, err := c.run(ctx, c.Dir(spec), "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// OriginURL reports which remote a checkout was actually made from.
//
// It exists because EnsureCloned answers "is there a directory here", not "is
// it the right repository". A checkout is named after the YAML entry, so an
// entry whose clone_url is corrected — or whose name is reused for a different
// repository — kept serving the old code under the new name, with every
// citation labelled with a repository the lines never came from.
//
// A missing checkout is not an error and not a mismatch: it returns "" so the
// caller reads it as "nothing to compare, clone it".
//
// The comparison this feeds is an exact string match against clone_url, which
// EnsureCloned guarantees is what origin holds — it overwrites the tokened URL
// git persists. A cosmetic edit (a trailing .git, http to https) therefore reads
// as a mismatch and costs a re-index. That is the safe direction to be wrong in.
func (c *Client) OriginURL(ctx context.Context, spec repos.Spec) (string, error) {
	dir := c.Dir(spec)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", nil
	}
	out, err := c.run(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		// A repository with no origin at all is a SNAPSHOT's drop sitting where
		// a clone belongs: the entry was switched from `snapshot: true` to a
		// clone_url, or a purged snapshot's name was re-added as a clone, and
		// purge deliberately leaves the extracted tree on disk. Passing the raw
		// "No such remote" up left that entry failing on every cycle forever
		// with nothing saying why. This is the mirror of the refusal
		// assertSnapshotCheckout makes in the other direction, and it asks for
		// the same thing: rongo will not delete a tree it did not create.
		if strings.Contains(err.Error(), "No such remote") {
			return "", fmt.Errorf(
				"%s: %s holds an extracted snapshot with no remote, not a clone of %s — remove the directory before indexing it as a cloned repository",
				spec.Name, dir, spec.CloneURL)
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RemoveCheckout deletes a repository's checkout.
//
// name is re-validated even though repos.Load already did: that call validates
// what came out of the YAML, and this one is reached with a name read back out
// of the database. The operation is an os.RemoveAll, so it checks rather than
// trusts — the cost of being wrong here is deleting outside the repository root.
func (c *Client) RemoveCheckout(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("refusing to remove checkout %q: not a single path segment", name)
	}
	if err := os.RemoveAll(filepath.Join(c.root, name)); err != nil {
		return fmt.Errorf("remove checkout %s: %w", name, err)
	}
	return nil
}

// DefaultBranch asks the remote which branch it considers default. Never assume
// master: this corpus mixes master and main, and the repositories on main are
// exactly the third-party ones the cross-repo logic needs.
//
// It takes the token because a private repository cannot answer this
// anonymously: without it, an entry that omits `branch:` could never resolve
// one and would never be indexed at all.
func (c *Client) DefaultBranch(ctx context.Context, spec repos.Spec, token string) (string, error) {
	out, err := c.run(ctx, c.root, "ls-remote", "--symref", authURL(spec.CloneURL, token), "HEAD")
	if err != nil {
		return "", err
	}
	// The symref line reads: "ref: refs/heads/main\tHEAD"
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("%s: remote reported no default branch", spec.Name)
}

// Fetch updates the remote-tracking refs.
func (c *Client) Fetch(ctx context.Context, spec repos.Spec, token string) error {
	_, err := c.run(ctx, c.Dir(spec), "fetch", "--quiet", "--prune",
		authURL(spec.CloneURL, token), "+refs/heads/*:refs/remotes/origin/*")
	return err
}

// HeadSHA returns the commit the branch points at on the remote-tracking side.
// A missing branch yields ErrBranchGone so the poller can tell "branch deleted"
// apart from "network hiccup".
func (c *Client) HeadSHA(ctx context.Context, spec repos.Spec, branch string) (string, error) {
	// --verify --quiet exits 1 with NO output and NO stderr when the ref is
	// simply absent, which is the case this reports as ErrBranchGone. Anything
	// else — an unreadable checkout, a broken git — is a DIFFERENT problem, and
	// collapsing it into "branch deleted" would put a confident wrong diagnosis
	// on the Repos page while discarding the real error text.
	out, err := c.run(ctx, c.Dir(spec), "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+branch)
	if err != nil {
		if !strings.Contains(err.Error(), "exit status 1") {
			return "", err
		}
		if _, checkErr := c.run(ctx, c.Dir(spec), "rev-parse", "--git-dir"); checkErr != nil {
			return "", fmt.Errorf("%s: repository unreadable: %w", spec.Name, checkErr)
		}
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s: branch %q: %w", spec.Name, branch, ErrBranchGone)
	}
	return strings.TrimSpace(out), nil
}

// ChangedPaths lists the paths differing between two commits. This is what
// keeps a push from costing a full re-index. It stays valid across a branch
// change too, because both commits live in the same object store.
func (c *Client) ChangedPaths(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]string, error) {
	// core.quotePath=false: by default git C-quotes any path with a non-ASCII
	// byte ("f\303\244hig.go"), and the quoted form is not a path — a later
	// `git show <sha>:<path>` fails on it, so every umlaut file would drop out
	// of the index as merely "unreadable". A German corpus is full of them.
	out, err := c.run(ctx, c.Dir(spec), "-c", "core.quotePath=false",
		"diff", "--name-only", fromSHA+".."+toSHA)
	if err != nil {
		return nil, err
	}
	// Never nil: a nil path list means "index everything" to the pipeline, so
	// an empty diff would trigger a full re-index of the whole repository.
	return append([]string{}, nonEmptyLines(out)...), nil
}

// Change is one path in a diff, and whether the newer commit still has it.
type Change struct {
	Path    string
	Deleted bool
}

// ChangedEntries is ChangedPaths with the delete/modify distinction the indexer
// needs: a deleted path must have its rows removed, a modified one re-read.
//
// --name-only cannot tell the two apart, and treating a failed read as a
// deletion would conflate a broken checkout with code that is genuinely gone —
// the index would quietly drop files that still exist. --no-renames is
// deliberate: to an indexer a rename IS a delete plus an add, and asking git to
// detect renames only produces a three-field record to parse for the same
// outcome.
func (c *Client) ChangedEntries(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]Change, error) {
	out, err := c.run(ctx, c.Dir(spec), "-c", "core.quotePath=false",
		"diff", "--name-status", "--no-renames", fromSHA+".."+toSHA)
	if err != nil {
		return nil, err
	}
	changes := []Change{}
	for _, line := range nonEmptyLines(out) {
		status, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unparseable diff record %q", line)
		}
		changes = append(changes, Change{
			Path:    strings.TrimSpace(path),
			Deleted: strings.HasPrefix(status, "D"),
		})
	}
	return changes, nil
}

// ListPaths lists every tracked path at a commit, for the initial full index.
func (c *Client) ListPaths(ctx context.Context, spec repos.Spec, sha string) ([]string, error) {
	out, err := c.run(ctx, c.Dir(spec), "-c", "core.quotePath=false",
		"ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// ReadFile reads one path at one commit. Reading from the commit rather than
// the working tree keeps every chunk attributable to an exact SHA, which is
// what makes a citation verifiable later.
func (c *Client) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	dir := c.Dir(spec)
	cmd := exec.CommandContext(ctx, c.git, safeDirectory(dir, "show", sha+":"+path)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read %s at %s: %w: %s", path, shortSHA(sha), err,
			redact(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Object reports what "sha:path" names — "blob", "tree", "commit" — and its
// size in bytes, without reading it. A caller that would refuse a large or
// non-file object asks here first, so the refusal costs nothing: ReadFile
// buffers the whole object before returning it.
func (c *Client) Object(ctx context.Context, spec repos.Spec, sha, path string) (kind string, size int64, err error) {
	dir := c.Dir(spec)
	cmd := exec.CommandContext(ctx, c.git, safeDirectory(dir, "cat-file", "--batch-check")...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(sha + ":" + path + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("stat %s at %s: %w: %s", path, shortSHA(sha), err, redact(stderr.String()))
	}
	// "<oid> <type> <size>" for an object, "<name> missing" otherwise.
	fields := strings.Fields(stdout.String())
	if len(fields) != 3 {
		return "", 0, fmt.Errorf("stat %s at %s: %s", path, shortSHA(sha), strings.TrimSpace(stdout.String()))
	}
	n, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s at %s: size %q: %w", path, shortSHA(sha), fields[2], err)
	}
	return fields[1], n, nil
}

// safeDirectory prefixes args with the ownership exemption for dir.
//
// git 2.35.2+ refuses EVERY command in a repository owned by another user
// ("dubious ownership"). The container runs as uid 1000 and a snapshot's drop
// belongs to whoever unpacked the archive, so without this the first command
// after the commit fails and the entry sits in a permanent error — and the
// source viewer fails too, on a citation that is perfectly valid.
//
// It goes here rather than at the snapshot call sites because it has to hold
// for every command: an earlier version set it on the two calls EnsureSnapshot
// makes and left ListPaths, ChangedPaths, ChangedEntries, ReadFile and Object
// without it, which is most of the ones a snapshot actually needs. Every
// directory this is applied to is under the repository root rongo owns or was
// told to use, so there is nothing wider being exempted than rongo's own tree.
func safeDirectory(dir string, args ...string) []string {
	return append([]string{"-c", "safe.directory=" + dir}, args...)
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.git, safeDirectory(dir, args...)...)
	cmd.Dir = dir
	// Never let git prompt: a hung credential prompt would stall the poller
	// forever with no output to diagnose it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// redact keeps an injected token out of an error that will be logged.
		return stdout.String(), fmt.Errorf("git %s: %w: %s", subcommand(args), err,
			redact(stderr.String()))
	}
	return stdout.String(), nil
}

// authURL injects the token into an https remote for the duration of one
// command. It is never written to disk and never logged.
func authURL(raw, token string) string {
	if token == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return raw
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// credentialInURL matches the userinfo segment of any URL-shaped substring:
// a scheme, "://", everything up to an "@" that is not a slash or whitespace.
var credentialInURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)

// redact strips the userinfo from anything URL-shaped in text about to be
// logged. git quotes the remote it failed against, and that remote carries the
// token injected by authURL.
//
// This deliberately works on the raw string rather than splitting into fields
// and calling url.Parse: git wraps the remote in single quotes and often
// appends a colon, so the field is not a parseable URL and url.Parse returns an
// error with a nil User. An earlier version did exactly that and leaked the
// token through all three of git's common authentication-failure messages —
// see TestRedact_realisticGitErrorShapes, which pins every one of them.
func redact(s string) string {
	return credentialInURL.ReplaceAllString(s, "${1}REDACTED@")
}

// subcommand finds the git subcommand in args, skipping any leading `-c key=value`
// pairs, so an error message names "diff" rather than "-c".
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return "git"
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
