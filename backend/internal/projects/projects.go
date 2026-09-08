// Package projects reads the grouping rongo searches by: which repositories
// make up a product, what part each one plays, and which of them call which.
//
// It is the hand-declared twin of repodeps. A manifest edge is derived from
// go.mod and says two repositories are one mechanism; a project is written in
// repos.yaml and says the same thing where no manifest can — a Go backend and a
// TypeScript UI share nothing a parser could read. Both are declarations, never
// a model's opinion about two repository names.
//
// Nothing here is derived from code, and nothing here is citable. A project's
// structure is configuration: it tells the router what to offer and the answer
// prompt which repository plays which part, and a claim resting on it is
// reported as configuration rather than as something read in the sources.
package projects

import (
	"context"
	"database/sql"
	"sort"
)

// Repo is one member of a project.
type Repo struct {
	Name string
	// Kind is the free-form part it plays — backend, ui, consumer, contract.
	Kind string
	// Description is one human sentence saying what it does.
	Description string
	// Uses names the siblings it depends on, sorted.
	Uses []string
}

// Project is one product.
type Project struct {
	Name string
	// Members are sorted by name, so every caller — the card, the page, the
	// prompt block — lists them in the same order on every turn.
	Members []Repo
}

// Map answers the two questions routing asks: which project a repository
// belongs to, and which repositories a project holds.
type Map struct {
	of       map[string]string // repository -> project
	projects map[string]Project
}

// Load reads the whole grouping in two queries. There are a handful of
// repositories and a handful of edges, and this is called once per turn the way
// repodeps.DependsOn is, so it is deliberately not cached: a project renamed in
// repos.yaml takes effect on the next question, not on the next restart.
func Load(ctx context.Context, db *sql.DB) (Map, error) {
	m := Map{of: map[string]string{}, projects: map[string]Project{}}

	rows, err := db.QueryContext(ctx,
		`SELECT name, project, kind, description FROM repo_state ORDER BY name`)
	if err != nil {
		return Map{}, err
	}
	defer rows.Close()

	members := map[string][]Repo{}
	for rows.Next() {
		var name, project, kind, description string
		if err := rows.Scan(&name, &project, &kind, &description); err != nil {
			return Map{}, err
		}
		// An empty project can only come from a row written before this
		// shipped — repos.Load refuses an entry without one. Grouping those
		// under "" would make every such repository one nameless product and
		// card them as a single button, so each stands as a project of its own.
		if project == "" {
			project = name
		}
		m.of[name] = project
		members[project] = append(members[project], Repo{Name: name, Kind: kind, Description: description})
	}
	if err := rows.Err(); err != nil {
		return Map{}, err
	}

	if err := m.loadUses(ctx, db, members); err != nil {
		return Map{}, err
	}

	for name, ms := range members {
		sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
		m.projects[name] = Project{Name: name, Members: ms}
	}
	return m, nil
}

// loadUses attaches the declared edges. An edge whose target is not a member of
// the same project is dropped rather than kept: repos.Load already refuses one,
// so reaching here means the row predates that check or was written by hand,
// and drawing an arrow out of a project is worse than drawing none.
func (m Map) loadUses(ctx context.Context, db *sql.DB, members map[string][]Repo) error {
	rows, err := db.QueryContext(ctx, `SELECT repo, uses FROM repo_uses ORDER BY repo, uses`)
	if err != nil {
		return err
	}
	defer rows.Close()

	at := map[string]*Repo{}
	for project := range members {
		for i := range members[project] {
			at[members[project][i].Name] = &members[project][i]
		}
	}
	for rows.Next() {
		var repo, uses string
		if err := rows.Scan(&repo, &uses); err != nil {
			return err
		}
		r, ok := at[repo]
		if !ok || m.of[repo] != m.of[uses] {
			continue
		}
		r.Uses = append(r.Uses, uses)
	}
	return rows.Err()
}

// Of returns a repository's project, or the repository's own name when the map
// has never heard of it. Never the empty string: an empty label would group
// every unknown repository into one product, where the fallback degrades to the
// behaviour rongo had before projects existed.
func (m Map) Of(repo string) string {
	if p, ok := m.of[repo]; ok {
		return p
	}
	return repo
}

// Members lists a project's repositories, sorted. Empty for a project the map
// does not carry.
func (m Map) Members(project string) []string {
	p, ok := m.projects[project]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(p.Members))
	for _, r := range p.Members {
		out = append(out, r.Name)
	}
	return out
}

// Project returns one project whole, for the page and the prompt block.
func (m Map) Project(name string) (Project, bool) {
	p, ok := m.projects[name]
	return p, ok
}

// All lists every project, sorted by name.
func (m Map) All() []Project {
	out := make([]Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Distinct counts the projects a set of repositories spans. This is what the
// repository rung asks: three repositories of one product are one thing to
// choose between, not three.
func (m Map) Distinct(repos []string) int {
	seen := map[string]bool{}
	for _, r := range repos {
		seen[m.Of(r)] = true
	}
	return len(seen)
}

// Covered lists the projects the given repositories cover ENTIRELY, sorted.
//
// Entirely is the whole point. Scope.Projects is what suppresses the comparison
// prompt rule, and a reader who named one repository of a three-repo project
// asked about that repository: the turn must behave exactly as it did before
// projects existed, which it does only if a partial cover counts for nothing.
func (m Map) Covered(repos []string) []string {
	have := make(map[string]bool, len(repos))
	for _, r := range repos {
		have[r] = true
	}
	var out []string
	for name, p := range m.projects {
		whole := len(p.Members) > 0
		for _, r := range p.Members {
			if !have[r.Name] {
				whole = false
				break
			}
		}
		if whole {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
