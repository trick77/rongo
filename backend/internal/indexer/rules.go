package indexer

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/pathglob"
	"github.com/trick77/rongo/internal/repos"
)

// SelectorOf is NewSelector over the configured settings, the exclusion list
// checked first: the server's selector and the eval harness's alike, both
// called as SelectorOf(cfg.IndexSelect()). A malformed pattern is refused,
// naming BACKEND_INDEX_EXCLUDE, where it comes from.
func SelectorOf(maxFileBytes, maxDataFileBytes, maxSchemaFileBytes int, exclude []string) (*Selector, error) {
	if err := pathglob.Validate(exclude); err != nil {
		return nil, fmt.Errorf("BACKEND_INDEX_EXCLUDE: %w", err)
	}
	return NewSelector(SelectOptions{
		MaxBytes:       maxFileBytes,
		MaxDataBytes:   maxDataFileBytes,
		MaxSchemaBytes: maxSchemaFileBytes,
		Exclude:        exclude,
	}), nil
}

// RulesOf is each repository's `index:` block from repos.yaml, by name, for
// Deps.Rules. A repository without one is absent.
func RulesOf(specs []repos.Spec) map[string]repos.Index {
	out := map[string]repos.Index{}
	for _, s := range specs {
		if len(s.Index.Exclude) > 0 || len(s.Index.Latest) > 0 {
			out[s.Name] = s.Index
		}
	}
	return out
}

// withRules is s for one repository: its own exclusion globs on top of the
// global ones, and the version directories its `latest` globs retire in tree
// (every file path of the repository). s itself is left as it is: every
// other repository shares it. idle are the latest globs that match no
// directory of tree.
func (s *Selector) withRules(r repos.Index, tree []string) (sel *Selector, idle []string) {
	out := *s
	out.exclude = slices.Concat(s.exclude, compile(r.Exclude))
	out.stale, idle = staleVersions(compile(r.Latest), tree)
	return &out, idle
}

// supersession is why a version directory is retired: the newer sibling,
// and the index.latest glob both match.
type supersession struct {
	newer, glob string
}

// superseded reports why p lies under a retired version directory, if it
// does.
func (s *Selector) superseded(p string) (string, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] != '/' {
			continue
		}
		if why, ok := s.stale[p[:i]]; ok {
			return "superseded by " + why.newer + " (index.latest " + why.glob + ")", true
		}
	}
	return "", false
}

// staleVersions maps every directory of tree that one of globs matches, and
// that is not the highest-named such directory under its parent, to the one
// that is. Directories are the proper prefixes of the file paths: git tracks
// no empty one, so a version with no file is no version. idle are the globs
// that match no directory at all.
func staleVersions(globs []excludePattern, tree []string) (stale map[string]supersession, idle []string) {
	if len(globs) == 0 {
		return nil, nil
	}
	dirs := map[string]bool{}
	for _, p := range tree {
		p = path.Clean(p)
		for i := 0; i < len(p); i++ {
			if p[i] == '/' {
				dirs[p[:i]] = true
			}
		}
	}
	type group struct {
		glob   int
		parent string
	}
	newest := map[group]string{}
	members := map[group][]string{}
	for d := range dirs {
		segs := strings.Split(d, "/")
		for i, pat := range globs {
			if !pathglob.Match(pat.segs, segs) {
				continue
			}
			g := group{i, path.Dir(d)}
			members[g] = append(members[g], d)
			if cur, ok := newest[g]; !ok || compareVersionNames(path.Base(d), path.Base(cur)) > 0 {
				newest[g] = d
			}
		}
	}
	stale = map[string]supersession{}
	used := make([]bool, len(globs))
	for g, ds := range members {
		used[g.glob] = true
		for _, d := range ds {
			if d != newest[g] {
				stale[d] = supersession{newer: newest[g], glob: globs[g.glob].text}
			}
		}
	}
	for i, u := range used {
		if !u {
			idle = append(idle, globs[i].text)
		}
	}
	return stale, idle
}

// compareVersionNames orders two directory names run by run: a run of digits
// against a run of digits by its value (2027 > 2026, V10 > V9, without
// overflow for any length), anything else byte by byte. Names equal that way
// ("V01", "V1") fall back to plain bytes, so the order is total.
func compareVersionNames(a, b string) int {
	x, y := a, b
	for x != "" && y != "" {
		xr, xd := leadingRun(x)
		yr, yd := leadingRun(y)
		var c int
		if xd && yd {
			c = compareDigits(xr, yr)
		} else {
			c = strings.Compare(xr, yr)
		}
		if c != 0 {
			return c
		}
		x, y = x[len(xr):], y[len(yr):]
	}
	switch {
	case len(x) > 0:
		return 1
	case len(y) > 0:
		return -1
	}
	return strings.Compare(a, b)
}

// leadingRun is the run of digits, or of non-digits, s starts with.
func leadingRun(s string) (run string, digits bool) {
	digits = isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], digits
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// compareDigits compares two digit runs by value.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// forRepo is the indexer to run repo with: ix itself when the repository has
// no rules, else a copy whose selector carries them. tree, the whole of the
// repository's files, is asked for only when a `latest` glob needs its
// directories; a glob that matches none of them is warned of, as it does
// nothing: most likely it matches file names, which the loader cannot tell.
func (ix *Indexer) forRepo(repo string, tree func() ([]string, error)) (*Indexer, error) {
	r, ok := ix.rules[repo]
	if !ok {
		return ix, nil
	}
	var paths []string
	if len(r.Latest) > 0 {
		var err error
		if paths, err = tree(); err != nil {
			return nil, err
		}
	}
	run := *ix
	var idle []string
	run.selector, idle = ix.selector.withRules(r, paths)
	for _, g := range idle {
		ix.log.Warn("index.latest glob matches no directory; it retires nothing", "repo", repo, "glob", g)
	}
	return &run, nil
}

// forRun is forRepo for one IndexRepo run, and the listing at sha when the
// run has to reconcile the rows it does not touch (nil when not).
//
// A full run's targets are the tree. An incremental one decides from the diff
// and the file rows, which hold the tree before it:
//   - no changed path lies in a version directory: no version can come or
//     go and no target is under one, so no stale set is needed;
//   - one does, but every such directory holds files on both sides of the
//     commit: an edit inside existing versions, judged on the rows under
//     their parents with the diff applied, still with no listing;
//   - such a directory came or went: the tree is listed, and the run
//     reconciles every row against it.
func (ix *Indexer) forRun(ctx context.Context, spec repos.Spec, st RepoState, sha string, paths []string, targets []target) (*Indexer, []target, error) {
	r, ok := ix.rules[st.Name]
	if !ok {
		return ix, nil, nil
	}
	if paths == nil {
		run, err := ix.forRepo(st.Name, func() ([]string, error) { return treeOf(targets), nil })
		return run, nil, err
	}
	run := *ix
	touched := versionDirs(compile(r.Latest), targets)
	if len(touched) == 0 {
		run.selector, _ = ix.selector.withRules(r, nil)
		return &run, nil, nil
	}
	before, err := ix.rowsAround(ctx, st.Name, touched)
	if err != nil {
		return nil, nil, err
	}
	after := applyDiff(before, targets)
	if !versionsMoved(touched, before, after) {
		run.selector, _ = ix.selector.withRules(r, after)
		return &run, nil, nil
	}
	all, err := ix.targets(ctx, spec, st, sha, nil)
	if err != nil {
		return nil, nil, err
	}
	run.selector, _ = ix.selector.withRules(r, treeOf(all))
	return &run, all, nil
}

// rowsAround is the file rows under the parent of every one of dirs: every
// sibling version of each, so the newest among them can be told. A version
// at the top of the repository has the whole repository as its parent.
func (ix *Indexer) rowsAround(ctx context.Context, repo string, dirs map[string]bool) ([]string, error) {
	parents := map[string]bool{}
	for d := range dirs {
		p := path.Dir(d)
		if p == "." {
			return ix.storedTree(ctx, repo)
		}
		parents[p] = true
	}
	var out []string
	for p := range parents {
		// Every path under p/ sorts between "p/" and "p0", '0' following '/'.
		rows, err := ix.paths(ctx, `SELECT path FROM files WHERE repo = ? AND path >= ? AND path < ?`,
			repo, p+"/", p+"0")
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// applyDiff is before with the commit's targets applied: a deleted one, or
// one that is no longer a file, gone; any other one there.
func applyDiff(before []string, targets []target) []string {
	gone := map[string]bool{}
	for _, tg := range targets {
		gone[tg.path] = true
	}
	out := make([]string, 0, len(before)+len(targets))
	for _, p := range before {
		if !gone[p] {
			out = append(out, p)
		}
	}
	for _, tg := range targets {
		if !tg.deleted && (tg.mode == "" || gitrepo.Indexable(tg.mode)) {
			out = append(out, tg.path)
		}
	}
	return out
}

// versionsMoved reports whether one of dirs holds files on one side of the
// commit and none on the other.
func versionsMoved(dirs map[string]bool, before, after []string) bool {
	held := func(paths []string) map[string]bool {
		out := map[string]bool{}
		for _, p := range paths {
			for i := 0; i < len(p); i++ {
				if p[i] == '/' && dirs[p[:i]] {
					out[p[:i]] = true
				}
			}
		}
		return out
	}
	b, a := held(before), held(after)
	for d := range dirs {
		if b[d] != a[d] {
			return true
		}
	}
	return false
}

// versionDirs is every directory a latest glob matches that some target lies
// under.
func versionDirs(latest []excludePattern, targets []target) map[string]bool {
	out := map[string]bool{}
	for _, tg := range targets {
		p := path.Clean(tg.path)
		for i := 0; i < len(p); i++ {
			if p[i] != '/' || out[p[:i]] {
				continue
			}
			segs := strings.Split(p[:i], "/")
			for _, g := range latest {
				if pathglob.Match(g.segs, segs) {
					out[p[:i]] = true
					break
				}
			}
		}
	}
	return out
}

// treeOf is the file paths of a full listing, the ones a run indexes or
// records: what the files table holds once it lands, so a sweep that reads
// its directories from there sees the same versions this run did.
func treeOf(all []target) []string {
	out := make([]string, 0, len(all))
	for _, tg := range all {
		if tg.mode == "" || gitrepo.Indexable(tg.mode) {
			out = append(out, tg.path)
		}
	}
	return out
}

// reconcile brings an incremental run's untouched files in line with the
// versions at sha. A commit that adds 2028/ touches none of 2027/'s files,
// yet retires them; one that removes 2028/ brings 2027/ back. Neither shows
// in the diff, so every row is checked against the run's selector. A file
// brought back is indexed as tree lists it, mode and size included, so the
// ceilings decide before the read: its row, recorded excluded, says size 0.
func (ix *Indexer) reconcile(ctx context.Context, spec repos.Spec, st RepoState, sha string, targets, tree []target, read readFile) error {
	if _, err := ix.retire(ctx, st.Name); err != nil {
		return err
	}
	back, err := ix.reincluded(ctx, st.Name)
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(targets))
	for _, tg := range targets {
		done[tg.path] = true
	}
	listed := make(map[string]target, len(tree))
	for _, tg := range tree {
		listed[tg.path] = tg
	}
	for _, p := range back {
		tg, ok := listed[p]
		// Not listed: gone at sha, and every gone path is one of targets.
		if done[p] || !ok {
			continue
		}
		if err := ix.indexOne(ctx, spec, st, sha, tg, read); err != nil {
			return err
		}
	}
	return nil
}

// storedTree is every file row of repo, skipped ones included: a newest
// version holding only skipped files is still the newest.
func (ix *Indexer) storedTree(ctx context.Context, repo string) ([]string, error) {
	return ix.paths(ctx, `SELECT path FROM files WHERE repo = ?`, repo)
}

// reincluded is the rows recorded as excluded that ix's selector no longer
// excludes: a rule was dropped or narrowed, or a newer version left.
func (ix *Indexer) reincluded(ctx context.Context, repo string) ([]string, error) {
	excluded, err := ix.paths(ctx, `SELECT path FROM files WHERE repo = ? AND skip_reason = ?`, repo, string(SkipExcluded))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range excluded {
		if _, still := ix.selector.Excluded(p); !still {
			out = append(out, p)
		}
	}
	return out, nil
}

func (ix *Indexer) paths(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := ix.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// forStored is forRepo with the directories read from repo's file rows, as
// the start-up pass has no tree to list.
func (ix *Indexer) forStored(ctx context.Context, repo string) (*Indexer, error) {
	return ix.forRepo(repo, func() ([]string, error) { return ix.storedTree(ctx, repo) })
}

// Swept is what the start-up pass changed in one repository.
type Swept struct {
	Repo string
	// Retired files were indexed and are skipped now.
	Retired int
	// Reincluded files are held as excluded and included by the rules now;
	// a full re-index was requested for them.
	Reincluded int
}

// SweepAll is the start-up pass over every repository: Sweep, with the page
// totals refreshed where it removed something, and a full re-index requested
// for an enabled repository whose rules include again what its index holds
// as excluded. Nothing else would index those: the poller idles while HEAD
// is unchanged, and an incremental run reads only what a commit touched.
// Comparing the rows against the rules is the whole change detection: no
// fingerprint of the rules is stored. It returns the repositories it changed
// something in. Failures are logged, not fatal: the next poll still indexes
// correctly.
func (ix *Indexer) SweepAll(ctx context.Context, state *StateStore) []Swept {
	all, err := state.All(ctx)
	if err != nil {
		ix.log.Warn("exclusion sweep skipped; repository list unreadable", "err", err)
		return nil
	}
	var out []Swept
	for _, st := range all {
		// One rules pass per repository, for both directions: retiring
		// changes skip reasons, never the rows, so the directories stand.
		run, err := ix.forStored(ctx, st.Name)
		if err != nil {
			ix.log.Warn("exclusion sweep failed", "repo", st.Name, "err", err)
			continue
		}
		s := Swept{Repo: st.Name}
		changed, counts, err := run.sweep(ctx, st.Name)
		if err != nil {
			ix.log.Warn("exclusion sweep failed", "repo", st.Name, "err", err)
			continue
		}
		if changed > 0 {
			s.Retired = changed
			if err := state.SetCounts(ctx, st.Name, counts); err != nil {
				ix.log.Warn("recording the swept totals failed", "repo", st.Name, "err", err)
			}
			ix.log.Info("skipped files removed from the index", "repo", st.Name, "files", changed)
		}
		if st.Enabled {
			s.Reincluded = run.requestIfReincluded(ctx, state, st.Name)
		}
		if s.Retired > 0 || s.Reincluded > 0 {
			out = append(out, s)
		}
	}
	return out
}

// requestIfReincluded asks for a full re-index of repo when its index holds
// files as excluded that ix's rules include, and returns how many.
func (ix *Indexer) requestIfReincluded(ctx context.Context, state *StateStore, repo string) int {
	back, err := ix.reincluded(ctx, repo)
	if err != nil {
		ix.log.Warn("checking for re-included files failed", "repo", repo, "err", err)
		return 0
	}
	if len(back) == 0 {
		return 0
	}
	if _, err := state.RequestReindex(ctx, repo); err != nil {
		ix.log.Warn("requesting a full re-index failed", "repo", repo, "err", err)
		return 0
	}
	ix.log.Info("excluded files are included now; full re-index requested", "repo", repo, "files", len(back))
	return len(back)
}
