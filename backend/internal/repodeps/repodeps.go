// Package repodeps records which repository publishes which coordinate and
// which repositories pull it.
//
// This is the hard signal the routing step needs: if one candidate's repository
// depends on another's, they are parts of one mechanism and asking which one is
// meant would force the reader to pick half an answer. Deriving that from a
// model's opinion about two module names would be a guess; a manifest is not.
//
// Only go.mod is parsed. The corpus holds no Maven, npm or .NET repository, and
// a parser no test can drive against real input is a liability rather than
// coverage.
package repodeps

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
)

// Parse reads one go.mod. The module path is what the repository publishes;
// every requirement is a coordinate it pulls, indirect ones included — an
// indirect dependency is still a real edge between two repositories.
func Parse(goMod []byte) (string, []string, error) {
	f, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		return "", nil, fmt.Errorf("parse go.mod: %w", err)
	}
	if f.Module == nil || f.Module.Mod.Path == "" {
		return "", nil, fmt.Errorf("parse go.mod: no module line")
	}
	requires := make([]string, 0, len(f.Require))
	for _, r := range f.Require {
		if r.Mod.Path != "" {
			requires = append(requires, r.Mod.Path)
		}
	}
	return f.Module.Mod.Path, requires, nil
}

// Sync replaces every row for one repository, in one transaction. mods maps a
// repo-relative go.mod path to its contents; a repository may hold several
// (peeq keeps its module under backend/).
//
// Replace rather than insert: a repository that drops a dependency must stop
// depending on it, or the routing step keeps suppressing a clarification that
// has become correct.
func Sync(ctx context.Context, db *sql.DB, repo string, mods map[string][]byte) error {
	type row struct{ coordinate, direction string }
	var rows []row
	for path, body := range mods {
		publishes, requires, err := Parse(body)
		if err != nil {
			// One unparsable manifest must not throw away the others. It is
			// logged by the caller; here it is simply not an edge.
			return fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row{publishes, "publishes"})
		for _, r := range requires {
			rows = append(rows, row{r, "requires"})
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM repo_deps WHERE repo = ?`, repo); err != nil {
		return fmt.Errorf("clear repo_deps: %w", err)
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO repo_deps (repo, coordinate, direction) VALUES (?, ?, ?)`,
			repo, r.coordinate, r.direction); err != nil {
			return fmt.Errorf("insert repo_deps: %w", err)
		}
	}
	return tx.Commit()
}

// DependsOn reports whether a pulls something b publishes.
//
// It is a JOIN, not a lookup: a repository requires far more coordinates than
// the corpus publishes, and only the pairs that meet are edges. An unindexed
// dependency is not a routing candidate anyway — the spec forbids crossing into
// one.
func DependsOn(ctx context.Context, db *sql.DB, a, b string) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT need.coordinate, have.coordinate
		FROM repo_deps AS need
		JOIN repo_deps AS have ON have.direction = 'publishes' AND have.repo = ?
		WHERE need.repo = ? AND need.direction = 'requires'`, b, a)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var need, have string
		if err := rows.Scan(&need, &have); err != nil {
			return false, err
		}
		if need == have || strings.HasPrefix(need, have+"/") {
			return true, nil
		}
	}
	return false, rows.Err()
}
