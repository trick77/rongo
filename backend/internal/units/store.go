package units

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Sync replaces one repository's units and unit_deps in one transaction, for
// repodeps.Sync's reason: a module that leaves the build must stop being a
// unit, and a half-written table would name parts of two different commits.
func Sync(ctx context.Context, db *sql.DB, repo string, us []Unit, deps []Dep) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM units WHERE repo = ?`, repo); err != nil {
		return fmt.Errorf("clear units: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM unit_deps WHERE repo = ?`, repo); err != nil {
		return fmt.Errorf("clear unit_deps: %w", err)
	}
	for _, u := range us {
		if u.Key == "." {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO units (repo, key, kind, name, tags) VALUES (?, ?, ?, ?, ?)`,
			repo, u.Key, string(u.Kind), u.Name, strings.Join(u.Tags, " ")); err != nil {
			return fmt.Errorf("insert unit %s: %w", u.Key, err)
		}
	}
	for _, d := range deps {
		if d.From == "." || d.To == "." {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO unit_deps (repo, from_key, to_key, coordinate) VALUES (?, ?, ?, ?)`,
			repo, d.From, d.To, d.Coordinate); err != nil {
			return fmt.Errorf("insert unit dep %s: %w", d.From, err)
		}
	}
	return tx.Commit()
}

// importFrom matches the module specifier of an ES import or re-export.
var importFrom = regexp.MustCompile(`(?m)(?:from|import|require\()\s*["']([^"']+)["']`)

// ImportDeps reads the dependencies an nx workspace does not write down in
// any manifest: an app importing "@lib" uses the library that alias points
// into. It reads the indexed chunks rather than the checkout, so it runs
// after the file pass and costs no second read of the tree; Sync writes
// what it returns together with the declared edges.
//
// aliases is tsconfig.base.json's paths as Aliases returns them. A specifier
// that starts with an alias resolves to the unit whose key the alias's target
// lies under; anything else — a package, a relative path — is not a unit edge.
// Ordered by from then to, so a sync writes them the same way each run.
func ImportDeps(ctx context.Context, db *sql.DB, repo string, us []Unit, aliases map[string]string) ([]Dep, error) {
	if len(aliases) == 0 || len(us) == 0 {
		return nil, nil
	}
	targetUnit := map[string]string{} // alias -> unit key
	for alias, dir := range aliases {
		if u := Of(us, dir+"/x"); u != nil {
			targetUnit[alias] = u.Key
		}
	}
	if len(targetUnit) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT f.path, GROUP_CONCAT(c.raw_text, char(10))
		FROM files f JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND (f.path LIKE '%.ts' OR f.path LIKE '%.tsx' OR f.path LIKE '%.js' OR f.path LIKE '%.jsx' OR f.path LIKE '%.mts')
		GROUP BY f.id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := map[Dep]bool{}
	for rows.Next() {
		var p, text string
		if err := rows.Scan(&p, &text); err != nil {
			return nil, err
		}
		from := Of(us, p)
		if from == nil {
			continue
		}
		for _, m := range importFrom.FindAllStringSubmatch(text, -1) {
			spec := m[1]
			for alias, to := range targetUnit {
				if (spec == alias || strings.HasPrefix(spec, alias+"/")) && to != from.Key {
					edges[Dep{Repo: repo, From: from.Key, To: to}] = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Dep, 0, len(edges))
	for d := range edges {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out, nil
}

// Load reads one repository's units and dependencies, units ordered by key
// and dependencies by from, to, coordinate.
func Load(ctx context.Context, db *sql.DB, repo string) ([]Unit, []Dep, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, kind, name, tags FROM units WHERE repo = ? ORDER BY key`, repo)
	if err != nil {
		return nil, nil, err
	}
	var us []Unit
	for rows.Next() {
		var u Unit
		var tags string
		if err := rows.Scan(&u.Key, &u.Kind, &u.Name, &tags); err != nil {
			rows.Close()
			return nil, nil, err
		}
		u.Repo = repo
		u.Tags = strings.Fields(tags)
		us = append(us, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	drows, err := db.QueryContext(ctx,
		`SELECT from_key, to_key, coordinate FROM unit_deps WHERE repo = ? ORDER BY from_key, to_key, coordinate`, repo)
	if err != nil {
		return nil, nil, err
	}
	defer drows.Close()
	var deps []Dep
	for drows.Next() {
		var d Dep
		if err := drows.Scan(&d.From, &d.To, &d.Coordinate); err != nil {
			return nil, nil, err
		}
		d.Repo = repo
		deps = append(deps, d)
	}
	return us, deps, drows.Err()
}

// Linked reports whether two units of one repository are joined by a
// declared dependency in either direction — the in-repository form of the
// manifest edge routing reads across repositories: parts of one build that
// use each other are one mechanism, and asking which was meant would make
// the reader pick half an answer.
func Linked(ctx context.Context, db *sql.DB, repo, a, b string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM unit_deps
		WHERE repo = ? AND ((from_key = ? AND to_key = ?) OR (from_key = ? AND to_key = ?))`,
		repo, a, b, b, a).Scan(&n)
	return n > 0, err
}

// Describe renders one repository's structure for the answer prompt:
// what it is built from and which part uses which. Templated, never
// written by a model, and never a source — the same class of input as the
// project block, and closed by the same rule. Empty for a repository with no
// units, so a single-build repository adds nothing to the prompt.
func Describe(repo string, us []Unit, deps []Dep) string {
	if len(us) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nRepository %q is built from %d parts:\n", repo, len(us))
	byKey := map[string]Unit{}
	for _, u := range us {
		byKey[u.Key] = u
		fmt.Fprintf(&b, "  %s (%s, %s)\n", u.Name, describeKind(u.Kind), u.Key)
	}
	var lines []string
	external := map[string][]string{}
	for _, d := range deps {
		from, ok := byKey[d.From]
		if !ok {
			continue
		}
		if d.To != "" {
			if to, ok := byKey[d.To]; ok {
				lines = append(lines, fmt.Sprintf("  %s uses %s.", from.Name, to.Name))
			}
			continue
		}
		external[from.Name] = append(external[from.Name], d.Coordinate)
	}
	if len(lines) > 0 {
		b.WriteString("Declared connections inside the repository:\n")
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	names := make([]string, 0, len(external))
	for n := range external {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "  %s also depends on %s, which are outside this repository.\n", n, strings.Join(external[n], ", "))
	}
	return b.String()
}

func describeKind(k Kind) string {
	switch k {
	case KindNxApp:
		return "application"
	case KindNxLib:
		return "library"
	case KindMavenService, KindGradleService:
		return "service"
	case KindMavenLibrary, KindGradleLibrary:
		return "library"
	case KindGoModule:
		return "Go module"
	}
	return string(k)
}
