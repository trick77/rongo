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
	// Part is the free-form part it plays — backend, ui, consumer, contract.
	Part string
	// Description is one human sentence saying what it does.
	Description string
	// Uses names the siblings and libraries it depends on, sorted.
	Uses []string
	// Library says this is a shared library: a project of one that any
	// project's uses may name. See repos.Spec.Library.
	Library bool
	// Image is the container image it is built into, tag-less, or empty.
	// See repos.Spec.Image.
	Image string
}

// Project is one product.
type Project struct {
	Name string
	// Members are sorted by name, so every caller — the card, the page, the
	// prompt block — lists them in the same order on every turn. A library a
	// member uses is among them, marked Library: a product is searched with
	// the code it is built on, and a thread pinned to the product may narrow
	// to the library. The library's own project of one lists it too.
	Members []Repo
}

// Map answers the two questions routing asks: which project a repository
// belongs to, and which repositories a project holds.
type Map struct {
	of       map[string]string // repository -> its own project (a library: itself)
	projects map[string]Project
	library  map[string]bool     // repositories from the libraries block
	usedBy   map[string][]string // library -> the products it is a member of, sorted
}

// Load reads the whole grouping in two queries. There are a handful of
// repositories and a handful of edges, and this is called once per turn the way
// repodeps.DependsOn is, so it is deliberately not cached: a project renamed in
// repos.yaml takes effect on the next question, not on the next restart.
func Load(ctx context.Context, db *sql.DB) (Map, error) {
	m := Map{of: map[string]string{}, projects: map[string]Project{}, library: map[string]bool{}, usedBy: map[string][]string{}}

	// enabled = 1: a parked repository is not offered on a clarification card
	// and does not count towards a project's membership. Offering one would ask
	// the reader to choose a product that answers nothing, and a project whose
	// members are all parked disappears entirely rather than becoming an option
	// with no code behind it.
	rows, err := db.QueryContext(ctx,
		`SELECT name, project, part, description, library, image FROM repo_state WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return Map{}, err
	}
	defer rows.Close()

	members := map[string][]Repo{}
	for rows.Next() {
		var name, project, part, description, image string
		var library int
		if err := rows.Scan(&name, &project, &part, &description, &library, &image); err != nil {
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
		if library == 1 {
			m.library[name] = true
		}
		members[project] = append(members[project], Repo{Name: name, Part: part, Description: description, Library: library == 1, Image: image})
	}
	if err := rows.Err(); err != nil {
		return Map{}, err
	}

	if err := m.loadUses(ctx, db, members); err != nil {
		return Map{}, err
	}
	m.shareLibraries(members)

	for name, ms := range members {
		sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
		m.projects[name] = Project{Name: name, Members: ms}
	}
	return m, nil
}

// shareLibraries makes every library a member of each product whose members
// use it, following library-to-library edges: a backend that uses acme-http,
// which uses acme-commons, is built on both. The library's own project of
// one is untouched, and of[library] still names it — which product a
// library hit belongs to is decided per turn, by the products beside it, in
// Distinct and Fold.
func (m Map) shareLibraries(members map[string][]Repo) {
	uses := map[string][]string{}
	for _, ms := range members {
		for _, r := range ms {
			uses[r.Name] = r.Uses
		}
	}
	// A library's own row sits under its own name. A hand-written row that
	// says library = 1 under another project is not one, and is left alone
	// rather than taking the turn down.
	byName := map[string]Repo{}
	for lib := range m.library {
		for _, r := range members[lib] {
			if r.Name == lib {
				byName[lib] = r
			}
		}
		if _, ok := byName[lib]; !ok {
			delete(m.library, lib)
		}
	}
	for project, ms := range members {
		if m.library[project] {
			continue
		}
		reached := map[string]bool{}
		var walk func(from string)
		walk = func(from string) {
			for _, u := range uses[from] {
				if m.library[u] && !reached[u] {
					reached[u] = true
					walk(u)
				}
			}
		}
		for _, r := range ms {
			walk(r.Name)
		}
		for lib := range reached {
			members[project] = append(members[project], byName[lib])
			m.usedBy[lib] = append(m.usedBy[lib], project)
		}
	}
	for lib := range m.usedBy {
		sort.Strings(m.usedBy[lib])
	}
}

// loadUses attaches the declared edges. An edge whose target is neither a
// member of the same project nor a library is dropped rather than kept:
// repos.Load already refuses one, so reaching here means the row predates that
// check or was written by hand, and drawing an arrow out of a project is worse
// than drawing none. A library target is the one edge that legitimately leaves
// the project, and a parked library is no target at all: it is absent from
// the map, so the edge is dropped like the rest.
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
		if !ok || (m.of[repo] != m.of[uses] && !m.library[uses]) {
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
// choose between, not three. A library beside a product that uses it is that
// product's; on its own, or beside products that do not use it, it is a
// project of one like any repository.
func (m Map) Distinct(repos []string) int {
	return len(m.Fold(repos))
}

// Fold groups repositories by the project a turn should treat them as, in
// first-seen order: every non-library under its own project, then every
// library under each present product that uses it, or under itself when
// none is present. A library used by two present products lands in both,
// because the hit is evidence for either and a card offering the two must
// carry it on both entries.
func (m Map) Fold(repos []string) map[string][]string {
	out := map[string][]string{}
	for _, r := range repos {
		if !m.library[r] {
			p := m.Of(r)
			out[p] = append(out[p], r)
		}
	}
	for _, r := range repos {
		if !m.library[r] {
			continue
		}
		placed := false
		for _, p := range m.usedBy[r] {
			if _, ok := out[p]; ok {
				out[p] = append(out[p], r)
				placed = true
			}
		}
		if !placed {
			out[r] = append(out[r], r)
		}
	}
	return out
}

// IsLibrary reports whether a repository came from the libraries block.
func (m Map) IsLibrary(repo string) bool {
	return m.library[repo]
}

// UsedBy lists the products a library is a member of, sorted; nil for
// anything else.
func (m Map) UsedBy(library string) []string {
	return m.usedBy[library]
}

// Covered lists the projects the given repositories cover ENTIRELY, sorted.
//
// Entirely is the whole point. Scope.Projects is what suppresses the comparison
// prompt rule, and a reader who named one repository of a three-repo project
// asked about that repository: the turn must behave exactly as it did before
// projects existed, which it does only if a partial cover counts for nothing.
//
// A product's own repositories are what has to be there; a library it uses
// is not required. A turn on the product's members alone is still a turn on
// the product, and a card written before the library was declared stored
// those members and nothing else. The library's own project of one is
// covered by the library alone, as before.
func (m Map) Covered(repos []string) []string {
	have := make(map[string]bool, len(repos))
	for _, r := range repos {
		have[r] = true
	}
	var out []string
	for name, p := range m.projects {
		whole := len(p.Members) > 0
		for _, r := range p.Members {
			if !have[r.Name] && !(r.Library && !m.library[name]) {
				whole = false
				break
			}
		}
		if whole {
			out = append(out, name)
		}
	}
	// A library's own project of one steps aside when any repository of a
	// product that uses it is present — Fold's rule, whether or not that
	// product is covered whole. The turn is about the product, built on the
	// library; a card that stored the library beside one member of the
	// product must not resume as a turn about the library compared with
	// that member.
	present := make(map[string]bool, len(repos))
	for _, r := range repos {
		if !m.library[r] {
			present[m.Of(r)] = true
		}
	}
	kept := out[:0]
	for _, name := range out {
		absorbed := false
		for _, p := range m.usedBy[name] {
			if present[p] {
				absorbed = true
				break
			}
		}
		if !absorbed {
			kept = append(kept, name)
		}
	}
	out = kept
	sort.Strings(out)
	return out
}
