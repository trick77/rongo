// Package modules groups a repository's indexed files into modules — the unit
// the routing layer reasons about.
//
// The cut is the directory, with two rules on top, and it is deliberately
// deterministic: no model is asked where a module begins. A model-proposed cut
// would be renegotiated on every re-index, which makes re-summarising on a diff
// untestable, and it would cost a call per repository before anyone has asked a
// question.
//
// Neither is the cut taken from the filesystem: it is taken from the `files`
// rows the indexer actually wrote, skipped ones excluded. A directory that is
// 90 % vendored or generated is not a module, and counting it as one invents a
// candidate no answer can ever be built from.
package modules

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Module is one routing unit: a directory prefix plus the indexed files that
// belong to it, including those folded in from directories too small to stand
// on their own.
type Module struct {
	Repo string
	// Key is the directory prefix, repo-relative. "." is the repository root,
	// which doubles as the catch-all for everything too small to be a module.
	Key        string
	Paths      []string
	ChunkCount int
	// Oversized records that the module exceeded MaxChunks and could not be
	// split, because all its files sit directly in one directory. Nothing in
	// the code branches on this — it exists so the module list shows where the
	// directory cut is doing a poor job instead of hiding it.
	Oversized bool
}

// Opts carries the two constants of the cut. They are calibrated against the
// real corpus (see TestModuleList) rather than guessed, and the chosen values
// belong in the measurement document.
type Opts struct {
	// MinChunks is the size below which a directory is not a module of its own
	// and is folded into its parent. Folding runs to a fixed point: a parent
	// that is still too small folds again, so a chain of near-empty modules
	// cannot form.
	MinChunks int
	// MaxChunks is the size above which a directory is split, its folded-in
	// child directories becoming modules of their own again. A directory whose
	// files all sit at one level has nothing to split at and stays whole,
	// marked Oversized.
	MaxChunks int
}

// group is a set of paths on its way up the tree — either about to become a
// module, or about to be merged into its parent.
type group struct {
	key    string
	paths  []string
	chunks int
}

// Cluster returns repo's modules, ordered by Key.
func Cluster(ctx context.Context, db *sql.DB, repo string, o Opts) ([]Module, error) {
	own, err := loadDirs(ctx, db, repo)
	if err != nil {
		return nil, err
	}

	// Deepest first, so a directory sees everything its children handed up
	// before it decides whether it is a module.
	dirs := make([]string, 0, len(own))
	for d := range own {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := depth(dirs[i]), depth(dirs[j])
		if di != dj {
			return di > dj
		}
		return dirs[i] > dirs[j]
	})

	pending := map[string][]group{}
	var out []Module

	emit := func(g group, oversized bool) {
		sort.Strings(g.paths)
		out = append(out, Module{
			Repo:       repo,
			Key:        g.key,
			Paths:      g.paths,
			ChunkCount: g.chunks,
			Oversized:  oversized,
		})
	}

	// Indexed rather than ranged: a fold can discover a parent directory that
	// holds no files of its own, and that parent is appended to dirs here and
	// must still be visited.
	for i := 0; i < len(dirs); i++ {
		d := dirs[i]
		self := own[d]
		carried := pending[d]
		delete(pending, d)

		total := self.chunks
		for _, c := range carried {
			total += c.chunks
		}

		// Split before merging: a directory that would grow past MaxChunks
		// gives its folded-in children back their own identity rather than
		// swallowing them.
		if o.MaxChunks > 0 && total > o.MaxChunks && len(carried) > 0 {
			for _, c := range carried {
				emit(c, false)
			}
			carried = nil
			total = self.chunks
		}

		merged := group{key: d, paths: append([]string{}, self.paths...), chunks: total}
		for _, c := range carried {
			merged.paths = append(merged.paths, c.paths...)
		}

		if len(merged.paths) == 0 {
			continue
		}
		if total >= o.MinChunks || d == "." {
			// "." has no parent to fold into, so it emits whatever is left.
			emit(merged, o.MaxChunks > 0 && total > o.MaxChunks)
			continue
		}
		p := parent(d)
		pending[p] = append(pending[p], merged)
		if _, ok := own[p]; !ok {
			// The parent holds no files of its own and would never be visited.
			own[p] = &group{key: p}
			dirs = insertSorted(dirs, p)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// loadDirs reads the indexed files of repo, grouped by directory, with the
// chunk count each contributes. Files the Selector skipped are excluded: the
// row exists so an answer can say "that file was not indexed", not so it can
// pad a module.
func loadDirs(ctx context.Context, db *sql.DB, repo string) (map[string]*group, error) {
	const q = `
SELECT f.path, COUNT(c.id)
FROM files f
LEFT JOIN chunks c ON c.file_id = f.id
WHERE f.repo = ? AND f.skip_reason = ''
GROUP BY f.id
ORDER BY f.path`

	rows, err := db.QueryContext(ctx, q, repo)
	if err != nil {
		return nil, fmt.Errorf("load files of %s: %w", repo, err)
	}
	defer rows.Close()

	dirs := map[string]*group{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		d := path.Dir(p)
		g, ok := dirs[d]
		if !ok {
			g = &group{key: d}
			dirs[d] = g
		}
		g.paths = append(g.paths, p)
		g.chunks += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read file rows of %s: %w", repo, err)
	}
	return dirs, nil
}

func parent(d string) string {
	if d == "." || d == "" {
		return "."
	}
	return path.Dir(d)
}

func depth(d string) int {
	if d == "." || d == "" {
		return 0
	}
	return strings.Count(d, "/") + 1
}

// insertSorted puts a newly discovered parent directory back into the
// deepest-first order, so it is visited after every child that feeds it.
func insertSorted(dirs []string, d string) []string {
	i := sort.Search(len(dirs), func(i int) bool {
		di, dd := depth(dirs[i]), depth(d)
		if di != dd {
			return di < dd
		}
		return dirs[i] <= d
	})
	dirs = append(dirs, "")
	copy(dirs[i+1:], dirs[i:])
	dirs[i] = d
	return dirs
}
