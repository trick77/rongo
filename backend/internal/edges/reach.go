package edges

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"unicode"
)

// MaxDefiners is the selectivity ceiling of a symbol hop: how many files may
// define a name before following it says nothing about which one this code
// depends on. ONE constant for both walks — internal/ask reads it for the
// reference walk the product runs, and Reach below reads it for the measured
// composed walk — so the two cannot disagree about which names are selective.
// Counted across ENABLED repositories, not within one.
//
// Four was measured on the real corpus (internal/ask): it drops Close (31
// files), err (23) and Error (10) while keeping a genuine service method,
// which is defined once or twice. Reach was first written with eight, and the
// flow catalogue measures the same 20 of 29 parts at four.
const MaxDefiners = 4

// inlandFanOut caps how many files one in-repo hop may return.
//
// Without it the walk is unbounded: in a Java repository most files mention
// some selective name, each becomes a crossing start, and each crossing runs
// another full hop on the far side. internal/ask bounds its walk with a hop
// budget; this is the equivalent. Files are ordered before the cut, so the cap
// takes a stable set rather than whichever rows the database returned first.
const inlandFanOut = 24

// Step says how a file was reached, so a measurement can tell an edge from the
// in-repo hops on either side of it, and so a later answer can explain itself.
type Step string

const (
	// StepInRepoBefore is a file in the SAME repository as the start, linked to
	// it by a selective symbol in either direction. It is the hop that finds
	// the file holding the literal when the interesting file merely calls it —
	// `OrdersController` calls `config.getPaymentUri()`, and the string
	// "/paymentAuth" lives in `OrdersConfigurationProperties`.
	StepInRepoBefore Step = "in-repo-before"
	// StepEdge is the crossing itself: another repository, same token.
	StepEdge Step = "edge"
	// StepInRepoAfter is the mirror of StepInRepoBefore on the far side. The
	// far end of a queue is a Spring configuration class; the file that does
	// the work is the handler it wires up, which carries no literal at all.
	StepInRepoAfter Step = "in-repo-after"
)

// Reached is one file the composed walk arrived at, with the path it took.
type Reached struct {
	Repo string
	Path string
	Step Step
	// Via is the token that crossed the boundary, empty for a hop that never
	// left the repository.
	Via     string
	Kind    Kind
	Through string // the file the hop came from, for reading a trail back
}

// Reach walks: in-repo hop, boundary crossing, in-repo hop.
//
// Neighbours alone measured 10 of 29 flow parts on the pinned corpus, and every
// miss had the same shape — the literal was in a file NEXT TO the one the
// answer needed, on one side or the other. Crossing is only useful if both ends
// can take one step inland, and that is all this adds. Two steps inland would
// start dragging the repository in, which is the failure the definer ceiling
// exists to prevent.
//
// The result is deterministic: every hop is ordered before it is used, so the
// same corpus produces the same trail, which is what makes a measurement over
// it reproducible.
func Reach(ctx context.Context, db *sql.DB, repo, path string) ([]Reached, error) {
	seen := map[[2]string]bool{{repo, path}: true}
	var out []Reached

	// One step inland from the start, so a file that only CALLS the thing
	// carrying the literal can still cross.
	starts := []fileRef{{repo: repo, path: path}}
	inland, err := inRepoNeighbours(ctx, db, repo, path)
	if err != nil {
		return nil, err
	}
	for _, f := range inland {
		key := [2]string{f.repo, f.path}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Reached{Repo: f.repo, Path: f.path, Step: StepInRepoBefore, Through: path})
		starts = append(starts, f)
	}

	// The crossings, from the start and from everything one step inland.
	var crossed []fileRef
	for _, s := range starts {
		ns, err := Neighbours(ctx, db, s.repo, s.path)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			key := [2]string{n.Repo, n.Path}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Reached{Repo: n.Repo, Path: n.Path, Step: StepEdge,
				Via: n.Value, Kind: n.Kind, Through: s.path})
			crossed = append(crossed, fileRef{repo: n.Repo, path: n.Path})
		}
	}

	// One step inland on the far side.
	for _, c := range crossed {
		far, err := inRepoNeighbours(ctx, db, c.repo, c.path)
		if err != nil {
			return nil, err
		}
		for _, f := range far {
			key := [2]string{f.repo, f.path}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Reached{Repo: f.repo, Path: f.path, Step: StepInRepoAfter, Through: c.path})
		}
	}
	return out, nil
}

type fileRef struct{ repo, path string }

// inRepoNeighbours returns files in the same repository linked to this one by a
// selective symbol, in EITHER direction: files defining a name this file's
// source mentions, and files whose source mentions a name this file defines.
//
// Both directions are needed and the corpus says why. Forward finds the
// configuration class a controller calls into. Backward finds the handler that
// a queue's configuration wires up — the configuration names the handler, the
// handler names nothing.
//
// Matching is on WHOLE identifiers, never on substrings. `strings.Contains`
// made the symbol `Item` match `ItemsController`, `OrderItem` and `LineItem`,
// which is a different and much looser walk than the one internal/ask runs;
// the text is tokenized the same way ask tokenizes it instead.
func inRepoNeighbours(ctx context.Context, db *sql.DB, repo, path string) ([]fileRef, error) {
	var text string
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(GROUP_CONCAT(c.raw_text, char(10)), '')
		FROM files f LEFT JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND f.path = ?
		GROUP BY f.id`, repo, path).Scan(&text)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mentioned := identifierSet(text)

	// Every symbol this repository defines, with the file defining it, dropping
	// names too common across the ESTATE to say anything.
	rows, err := db.QueryContext(ctx, `
		SELECT s.name, f.path
		FROM symbols s
		JOIN files f ON f.id = s.file_id
		WHERE f.repo = ?
		  AND (SELECT COUNT(DISTINCT s2.file_id)
		         FROM symbols s2
		         JOIN files f2 ON f2.id = s2.file_id
		         JOIN repo_state r2 ON r2.name = f2.repo AND r2.enabled = 1
		        WHERE s2.name = s.name) <= ?`, repo, MaxDefiners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hit := map[string]bool{}
	mine := map[string]bool{}
	for rows.Next() {
		var name, defPath string
		if err := rows.Scan(&name, &defPath); err != nil {
			return nil, err
		}
		if len(name) <= 2 {
			continue
		}
		if defPath == path {
			mine[name] = true
			continue
		}
		// Forward: this file mentions a name that file defines.
		if mentioned[name] {
			hit[defPath] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Backward: another file in this repository mentions a name THIS file
	// defines. Done by reading the repository's chunks once and tokenizing
	// them, rather than by joining every chunk against every symbol row, which
	// is a cartesian product the DISTINCT only hides the cost of.
	if len(mine) > 0 {
		back, err := db.QueryContext(ctx, `
			SELECT f.path, COALESCE(GROUP_CONCAT(c.raw_text, char(10)), '')
			FROM files f JOIN chunks c ON c.file_id = f.id
			WHERE f.repo = ? AND f.path <> ?
			GROUP BY f.path`, repo, path)
		if err != nil {
			return nil, err
		}
		for back.Next() {
			var p, body string
			if err := back.Scan(&p, &body); err != nil {
				back.Close()
				return nil, err
			}
			if hit[p] {
				continue
			}
			for name := range identifierSet(body) {
				if mine[name] {
					hit[p] = true
					break
				}
			}
		}
		back.Close()
		if err := back.Err(); err != nil {
			return nil, err
		}
	}

	paths := make([]string, 0, len(hit))
	for p := range hit {
		paths = append(paths, p)
	}
	// Ordered before the cap, so the hop is the same set on every run.
	sort.Strings(paths)
	if len(paths) > inlandFanOut {
		paths = paths[:inlandFanOut]
	}
	out := make([]fileRef, 0, len(paths))
	for _, p := range paths {
		out = append(out, fileRef{repo: repo, path: p})
	}
	return out, nil
}

// identifierSet tokenizes source the way internal/ask does: on anything that is
// not a letter, a digit or an underscore, keeping names longer than two
// characters. Kept identical on purpose — two walks disagreeing about what an
// identifier is would make a gathered set depend on which one found it.
func identifierSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len(f) > 2 {
			out[f] = true
		}
	}
	return out
}
