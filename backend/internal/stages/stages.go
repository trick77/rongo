// Package stages is the deployment stages an infrastructure repository
// declares: which directory is prod, which is intg, and what a reader calls
// them. It is what lets "how often is the digest sent in production" narrow
// the infrastructure repository to its prod directory, and what labels a
// source under that directory as the deployed value for that stage.
//
// Declared in repos.yaml, stored by SyncSpecs, read fresh per turn. Nothing
// here is inferred from the tree or from a model.
package stages

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/trick77/rongo/internal/repos"
)

// Stage is one declared stage of one repository.
type Stage struct {
	Repo    string
	Name    string
	Prefix  string
	Aliases []string
}

// Set is every declared stage of every enabled repository.
type Set []Stage

// Execer is what Sync writes through: a *sql.DB or the *sql.Tx SyncSpecs
// already holds.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Sync replaces one repository's stages, for repo_uses' reason: a stage that
// leaves repos.yaml must leave the table, or a question would go on
// narrowing to a directory nobody declared.
func Sync(ctx context.Context, db Execer, repo string, stages []repos.Stage) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM repo_stages WHERE repo = ?`, repo); err != nil {
		return fmt.Errorf("clear repo_stages for %s: %w", repo, err)
	}
	for _, s := range stages {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO repo_stages (repo, name, prefix, aliases) VALUES (?, ?, ?, ?)`,
			repo, s.Name, s.Prefix, strings.Join(s.Aliases, " ")); err != nil {
			return fmt.Errorf("insert stage %s/%s: %w", repo, s.Name, err)
		}
	}
	return nil
}

// Load reads the stages of every ENABLED repository. A parked repository's
// stages narrow nothing and label nothing, the same as its files.
func Load(ctx context.Context, db *sql.DB) (Set, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.repo, s.name, s.prefix, s.aliases
		FROM repo_stages s
		JOIN repo_state r ON r.name = s.repo AND r.enabled = 1
		ORDER BY s.repo, s.name`)
	if err != nil {
		return nil, fmt.Errorf("load stages: %w", err)
	}
	defer rows.Close()
	var out Set
	for rows.Next() {
		var s Stage
		var aliases string
		if err := rows.Scan(&s.Repo, &s.Name, &s.Prefix, &aliases); err != nil {
			return nil, err
		}
		if aliases != "" {
			s.Aliases = strings.Fields(aliases)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Names is every distinct stage name, sorted: what the understanding prompt
// offers the model to choose from.
func (s Set) Names() []string {
	seen := map[string]bool{}
	var out []string
	for _, st := range s {
		if !seen[st.Name] {
			seen[st.Name] = true
			out = append(out, st.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Resolve maps one word — a stage name or alias, in any case — to the stage
// name, and reports false for a word that names no stage.
func (s Set) Resolve(word string) (string, bool) {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return "", false
	}
	for _, st := range s {
		if st.Name == w {
			return st.Name, true
		}
		for _, a := range st.Aliases {
			if a == w {
				return st.Name, true
			}
		}
	}
	return "", false
}

// Mentioned is the stages the question names as whole words, by name or
// alias, distinct and in the order they occur. The reader's own wording,
// which is why an ordinary word is never a stage word (repos.Load refuses
// it): a mention narrows the turn.
func (s Set) Mentioned(question string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range words(question) {
		if name, ok := s.Resolve(w); ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Prefixes is the path prefix each repository declaring stage name searches
// under, keyed by repository. A repository that declares stages but not
// this one is mapped to an impossible prefix, so it contributes nothing:
// asking for prod in a repository with no prod is not asking for all of it.
// Repositories declaring no stages at all are absent and stay unrestricted.
func (s Set) Prefixes(name string) map[string]string {
	if name == "" {
		return nil
	}
	out := map[string]string{}
	for _, st := range s {
		if _, ok := out[st.Repo]; !ok {
			out[st.Repo] = noSuchStage
		}
		if st.Name == name {
			out[st.Repo] = st.Prefix
		}
	}
	return out
}

// noSuchStage is a prefix no repo-relative path starts with.
const noSuchStage = "//"

// Of is the stage a path of repo belongs to, or "" when it lies under no
// declared stage directory.
func (s Set) Of(repo, path string) string {
	for _, st := range s {
		if st.Repo == repo && strings.HasPrefix(path, st.Prefix) {
			return st.Name
		}
	}
	return ""
}

// words splits a question into lower-cased words. Letters and digits are
// word characters; everything else separates, so "prod," and "(prod)" both
// yield prod.
func words(q string) []string {
	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
