package gitrepo

import (
	"context"
	"fmt"
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
	// appends follow the last field as lines. Neither byte can occur in a
	// message git accepted, and core.quotePath=false keeps an umlaut path a
	// path (see ChangedPaths).
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
	for _, rec := range strings.Split(out, "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, "\x00")
		if len(f) != 6 {
			return nil, fmt.Errorf("unparseable log record with %d fields", len(f))
		}
		commits = append(commits, Commit{
			SHA:         f[0],
			CommittedAt: f[1],
			Author:      f[2],
			Subject:     f[3],
			Body:        strings.TrimSpace(f[4]),
			Paths:       append([]string{}, nonEmptyLines(f[5])...),
		})
	}
	return commits, nil
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
		"show", "--first-parent", "--numstat", "--format=%H%x00%aI%x00%s%x00%b%x00", sha)
	if err != nil {
		return CommitDetail{}, err
	}
	f := strings.Split(out, "\x00")
	if len(f) != 5 {
		return CommitDetail{}, fmt.Errorf("unparseable show record for %s", ShortSHA(sha))
	}
	d := CommitDetail{SHA: f[0], CommittedAt: f[1], Subject: f[2], Body: strings.TrimSpace(f[3]), Files: []FileChange{}}
	for _, line := range nonEmptyLines(f[4]) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return CommitDetail{}, fmt.Errorf("unparseable numstat line %q", line)
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		d.Files = append(d.Files, FileChange{Path: parts[2], Added: added, Deleted: deleted})
	}
	return d, nil
}
