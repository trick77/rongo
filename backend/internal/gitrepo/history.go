package gitrepo

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/repos"
)

// Commit is one entry of a branch's history as the commit lane stores it:
// what changed, when, and the message that says why. Author is kept for a
// later choice and never served; a shared page is public.
type Commit struct {
	SHA string
	// CommittedAt is the author date in ISO 8601 with offset, as git prints
	// %aI. The author date is the one a reader means by "when was this
	// done"; a rebase moves the committer date without changing the work.
	CommittedAt string
	Author      string
	Subject     string
	Body        string
	// Paths are the files the commit changed against its first parent.
	Paths []string
}

// Log lists the first-parent history of toSHA, newest first, up to limit
// entries; with fromSHA set only the commits fromSHA does not reach, which
// is what an incremental poll adds.
//
// First parent, no merge filtering: a squash-merged branch is one commit per
// pull request already, and a merge-commit branch collapses to one entry
// per merge, whose paths are what the merge brought in. Either way a reader
// asking "what changed" gets one line per change, never the side branch's
// work-in-progress commits.
func (c *Client) Log(ctx context.Context, spec repos.Spec, fromSHA, toSHA string, limit int) ([]Commit, error) {
	rng := toSHA
	if fromSHA != "" {
		rng = fromSHA + ".." + toSHA
	}
	// %x1e opens a record, %x00 separates its fields; the paths --name-only
	// appends follow the last field as lines. git refuses only NUL in a
	// message, so the fields are split on NUL alone and 0x1e is looked for
	// only in the path lines, where git quotes a control byte.
	// core.quotePath=false keeps an umlaut path a path (see ChangedPaths).
	out, err := c.run(ctx, c.Dir(spec), "-c", "core.quotePath=false",
		"log", "--first-parent", "--name-only", "-n", strconv.Itoa(limit),
		"--format=%x1e%H%x00%aI%x00%an%x00%s%x00%b%x00", rng)
	if err != nil {
		return nil, err
	}
	return parseLog(out)
}

func parseLog(out string) ([]Commit, error) {
	commits := []Commit{}
	if strings.TrimSpace(out) == "" {
		return commits, nil
	}
	// Five fields per commit; the sixth piece holds its paths and, after the
	// last 0x1e, the next commit's sha.
	f := strings.Split(out, "\x00")
	head, ok := strings.CutPrefix(f[0], "\x1e")
	for i := 0; ; i += 5 {
		if !ok || i+5 >= len(f) {
			return nil, fmt.Errorf("unparseable log record at field %d", i)
		}
		paths, next, more := f[i+5], "", false
		if at := strings.LastIndex(paths, "\x1e"); at >= 0 {
			paths, next, more = paths[:at], paths[at+1:], true
		}
		commits = append(commits, Commit{
			SHA:         head,
			CommittedAt: f[i+1],
			Author:      f[i+2],
			Subject:     f[i+3],
			Body:        stripTrailers(f[i+4]),
			Paths:       gitPaths(nonEmptyLines(paths)),
		})
		if !more {
			return commits, nil
		}
		head = next
	}
}

// gitPaths unquotes each path line; never nil, so an empty commit stores
// an empty list.
func gitPaths(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, gitPath(l))
	}
	return out
}

// trailerRe matches a git trailer line: Signed-off-by, Co-authored-by,
// Reviewed-by, Reported-by, and the rest of the "-by:" family. Every one
// names a person, most with an email.
var trailerRe = regexp.MustCompile(`(?im)^[A-Za-z-]+-by:[^\n]*\n?`)

// stripTrailers drops the trailer lines from a message body. The author is
// stored and never served, and a trailer is the same fact by another door:
// the body reaches the answer prompt and the public commit view.
func stripTrailers(body string) string {
	return strings.TrimSpace(trailerRe.ReplaceAllString(body, ""))
}

// CommitDetail is what the commit view shows: the message and date, with
// the per-file line counts a reader uses to judge a change's size. No
// author: the view is reachable from a shared page.
type CommitDetail struct {
	SHA         string
	CommittedAt string
	Subject     string
	Body        string
	Files       []FileChange
}

// FileChange is one path of a commit with its added and deleted line
// counts; both are zero for a binary file, which git reports as "-".
type FileChange struct {
	Path    string
	Added   int
	Deleted int
}

// Show reads one commit for the commit view. It fails on a commit the object
// store does not hold, which the caller turns into a 404 rather than an
// error page: a citation outlives a re-extracted snapshot.
func (c *Client) Show(ctx context.Context, spec repos.Spec, sha string) (CommitDetail, error) {
	out, err := c.run(ctx, c.Dir(spec), "-c", "core.quotePath=false",
		"show", "--first-parent", "--no-renames", "--numstat", "--format=%H%x00%aI%x00%s%x00%b%x00", sha)
	if err != nil {
		return CommitDetail{}, err
	}
	f := strings.Split(out, "\x00")
	if len(f) != 5 {
		return CommitDetail{}, fmt.Errorf("unparseable show record for %s", ShortSHA(sha))
	}
	d := CommitDetail{SHA: f[0], CommittedAt: f[1], Subject: f[2], Body: stripTrailers(f[3]), Files: []FileChange{}}
	for _, line := range nonEmptyLines(f[4]) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return CommitDetail{}, fmt.Errorf("unparseable numstat line %q", line)
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		d.Files = append(d.Files, FileChange{Path: gitPath(parts[2]), Added: added, Deleted: deleted})
	}
	return d, nil
}
