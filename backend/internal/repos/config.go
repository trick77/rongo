// Package repos loads and validates the repository list. The list lives in a
// YAML file rather than a database table so it is versionable and reviewable in
// a diff; credentials deliberately do NOT live there, because that file ends up
// in a repository or a ticket sooner or later.
package repos

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is one repository rongo indexes.
type Spec struct {
	// Name identifies the repository and becomes its directory under
	// BACKEND_REPO_ROOT, so it must be a safe single path segment.
	Name string
	// CloneURL must not embed credentials; see TokenEnv. Empty for a Snapshot,
	// which is the one entry shape with no remote at all.
	CloneURL string
	// Snapshot marks a repository the operator extracted by hand into
	// BACKEND_REPO_ROOT/<Name> — a source archive with no .git, no remote and no
	// upstream. rongo commits it once and indexes that commit, so every path
	// downstream still reads a real sha; it is never fetched, so it stays as it
	// was extracted until it is extracted again.
	//
	// Storage does not carry this as a column: a snapshot's CloneURL is empty,
	// which already says it, and a column could only ever disagree.
	Snapshot bool
	// Branch is optional. Empty means "resolve the remote's default branch",
	// which is NOT necessarily master — this corpus mixes master and main.
	Branch string
	// TokenEnv names the environment variable holding the access token for this
	// repository's forge. The value never appears in the YAML. It only applies
	// to an https remote: an ssh remote authenticates with a key, and naming a
	// token on one is refused rather than silently ignored.
	TokenEnv string
	// TokenUser is the basic-auth username sent alongside the token. Empty
	// means x-access-token, which GitHub accepts. Bitbucket Data Center
	// checks it: a personal HTTP access token goes with the user's own name,
	// a project or repository token with x-token-auth. It is not a secret.
	TokenUser string
	// TokenAuth is how the token travels: "" (basic auth, the token as the
	// password of TokenUser) or "bearer" (an Authorization: Bearer header,
	// no username at all). Bitbucket Data Center takes bearer for every
	// token kind, which spares the operator the username question.
	TokenAuth string
	// Enabled defaults to true; set it false to stop indexing without deleting.
	Enabled bool
	// Project names the product this repository is part of. It is not written
	// on the entry: it is the name of the block the entry sits in, copied down
	// here so everything past Load stays a flat list of repositories. It is the
	// unit a reader is asked to choose between, so a repository that stands
	// alone is a project of one, conventionally named after itself.
	Project string
	// Library marks an entry from the top-level `libraries:` block: a
	// repository several products are built on, declared once and owned by
	// none of them. It stands as a project of one named after itself, so
	// Project equals Name, and it is the one legal target of a `uses` edge
	// from another project. It is not searched as part of any product that
	// uses it: a project turn reaches it the way it reaches any other
	// repository, through a symbol hop or a manifest edge.
	Library bool
	// Part is a free-form token for the part this repository plays — backend,
	// ui, consumer, contract. Deliberately not an enum: a closed vocabulary
	// rejects a real corpus the first time it needs a word not on the list, and
	// nothing branches on this deterministically. It reaches the answer prompt,
	// where a model reads "consumer" perfectly well.
	//
	// Named for what the Projects page has always called this column. It was
	// `kind` in the YAML alone, which meant a reader comparing the page against
	// the file had to know the two words were one field.
	Part string
	// Description is one human-written sentence saying what this repository
	// does. It is what separates a Kafka receiver from a second HTTP backend,
	// which Part alone cannot. Never embedded, never indexed, never cited.
	Description string
	// Uses names the repositories this one depends on — declared by the
	// consumer, the same direction as go.mod's require. A target is either a
	// sibling INSIDE the same project or a library; a library itself may only
	// use other libraries. Any other coupling across projects is repo_deps'
	// business, read from a manifest rather than declared by hand.
	Uses []string
	// Stages are the deployment stages an infrastructure repository holds,
	// each a directory of the checkout. Declared, never inferred from the
	// tree: which directories are stages and what a reader calls them is
	// not a fact the files carry. Empty for every ordinary repository.
	Stages []Stage
}

// Stage is one deployment stage of an infrastructure repository: a name the
// reader can ask for, the directory its files live under, and the other
// words the reader may use for it.
type Stage struct {
	// Name is the stage as its directory calls it and as the answer names
	// it: prod, intg.
	Name string
	// Prefix is the repo-relative directory, with a trailing slash: "prod/".
	// Every path under it is that stage's; a path under none is no stage's.
	Prefix string
	// Aliases are other whole words that name this stage, lower-cased:
	// production, produktion. A question containing one narrows to the
	// stage, so an ordinary word is refused here.
	Aliases []string
}

type file struct {
	// Libraries are the repositories no single product owns: declared once
	// here, named by `uses` from any project. See Spec.Library.
	Libraries []rawSpec    `yaml:"libraries"`
	Projects  []rawProject `yaml:"projects"`
}

// rawProject is one product and the repositories it is built from. The project
// CONTAINS its repositories rather than each repository naming its project:
// that is how a reader thinks about it, it states the grouping once instead of
// once per entry, and a repository cannot end up in two projects at all.
type rawProject struct {
	Name string `yaml:"name"`
	// Enabled parks the whole product in one edit. A *bool for rawSpec's
	// reason: a plain bool defaults to false, which would silently park every
	// project in the file.
	Enabled      *bool     `yaml:"enabled"`
	Repositories []rawSpec `yaml:"repositories"`
}

// rawSpec exists so Enabled can default to true. A plain bool would default to
// false and silently disable every entry that omits the field.
type rawSpec struct {
	Name        string     `yaml:"name"`
	CloneURL    string     `yaml:"clone_url"`
	Branch      string     `yaml:"branch"`
	TokenEnv    string     `yaml:"token_env"`
	TokenUser   string     `yaml:"token_user"`
	TokenAuth   string     `yaml:"token_auth"`
	Enabled     *bool      `yaml:"enabled"`
	Snapshot    bool       `yaml:"snapshot"`
	Part        string     `yaml:"part"`
	Description string     `yaml:"description"`
	Uses        []string   `yaml:"uses"`
	Stages      []rawStage `yaml:"stages"`
}

type rawStage struct {
	Name    string   `yaml:"name"`
	Path    string   `yaml:"path"`
	Aliases []string `yaml:"aliases"`
}

// Load reads and validates the repository list, returning the first problem it
// finds rather than indexing a half-valid list.
func Load(path string) ([]Spec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read repository list %s: %w", path, err)
	}
	// The old shape is looked for FIRST, and leniently, so it is named as what
	// it is. A half-migrated file usually still carries `project:` on the
	// entries that were not moved, and the strict decode below would report
	// those instead — true, but it sends the reader to the wrong line.
	var old struct {
		Repositories []struct{} `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(body, &old); err == nil && len(old.Repositories) > 0 {
		return nil, fmt.Errorf(
			"%s has a top-level `repositories:` list, which is the old flat shape — nest those entries under a `projects:` block, because entries left outside one are not loaded and everything they name would be purged",
			path)
	}

	var f file
	// KnownFields, not Unmarshal: an unknown key is a typo, and every typo in
	// this file is a repository that silently is not what it says. A `project:`
	// left on a nested entry after the migration is exactly that — it reads as
	// declared and does nothing.
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// A list that names nothing is refused rather than returned empty, and this
	// is the floor under the purge: a repository absent from the list loses its
	// index and its checkout, so "the file parsed and mentions no repository"
	// would wipe the whole corpus. The way to reach that state is not exotic: a
	// truncated file after a botched deploy. Refusing here puts the caller on
	// its "list unavailable" path, which leaves everything exactly as it was
	// and says so. The two other shapes that used to land here — a mistyped
	// top-level key and the old flat list — are refused above, by name.
	if len(f.Projects) == 0 {
		return nil, fmt.Errorf(
			"%s names no project: expected a `projects:` list, each with a `repositories:` list of at least one entry", path)
	}

	seen := map[string]bool{}        // repository names, across every project and library
	seenProject := map[string]bool{} // project names, and library names: a library is a project of one
	var specs []Spec

	// Libraries first: their names occupy both namespaces, so a project block
	// named after one is refused by the ordinary duplicate-project rule below.
	for i, r := range f.Libraries {
		if err := validateName(r.Name); err != nil {
			return nil, fmt.Errorf("library %d: %w", i, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate repository name %q", r.Name)
		}
		seen[r.Name] = true
		seenProject[r.Name] = true
		spec, err := loadEntry(r, r.Name, true)
		if err != nil {
			return nil, err
		}
		spec.Library = true
		specs = append(specs, spec)
	}

	for i, p := range f.Projects {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, fmt.Errorf("project %d: name is required", i)
		}
		// Two blocks of one name is the flat shape sneaking back in: the
		// grouping would have to be reassembled by folding, which is the thing
		// nesting exists to make unnecessary. One block per product, whole.
		if seenProject[name] {
			return nil, fmt.Errorf("duplicate project name %q — a project is one block holding all of its repositories", name)
		}
		seenProject[name] = true
		// An empty block is a product rongo cannot search and a name the card
		// could still offer. Almost always a half-finished edit.
		if len(p.Repositories) == 0 {
			return nil, fmt.Errorf("project %q names no repository", name)
		}

		// A parked product is parked WHOLE: the project's false beats a member's
		// true, and the two are ANDed rather than the member overriding. One
		// repository opting back in would keep a product alive that the page no
		// longer shows and the router no longer offers — visible nowhere,
		// answering anyway, which is the state this flag exists to prevent.
		projectEnabled := true
		if p.Enabled != nil {
			projectEnabled = *p.Enabled
		}

		for j, r := range p.Repositories {
			if err := validateName(r.Name); err != nil {
				return nil, fmt.Errorf("project %q, entry %d: %w", name, j, err)
			}
			// Across every project, not just this one: the name is a directory
			// under BACKEND_REPO_ROOT and a citation's repository, both of
			// which are corpus-wide.
			if seen[r.Name] {
				return nil, fmt.Errorf("duplicate repository name %q", r.Name)
			}
			seen[r.Name] = true
			spec, err := loadEntry(r, name, projectEnabled)
			if err != nil {
				return nil, err
			}
			specs = append(specs, spec)
		}
	}

	// Cross-entry checks come after every entry is read: each one needs the
	// whole list, and a uses edge cannot be judged against repositories the
	// loop has not reached yet.
	if err := validateProjects(specs); err != nil {
		return nil, err
	}
	if err := validateStageWords(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

// loadEntry validates one entry — a project member or a library — and
// flattens it. The name has already been checked by the caller, which owns the
// duplicate rules; project is the block the entry sits in, or the entry's own
// name for a library. enabled is the enclosing project's flag, ANDed with the
// entry's own.
func loadEntry(r rawSpec, project string, enabled bool) (Spec, error) {
	if r.Snapshot {
		if err := validateSnapshot(r); err != nil {
			return Spec{}, err
		}
	} else if err := validateCloneURL(r.Name, r.CloneURL); err != nil {
		return Spec{}, err
	} else if err := validateToken(r); err != nil {
		return Spec{}, err
	}
	if r.Enabled != nil && !*r.Enabled {
		enabled = false
	}
	stages, err := loadStages(r.Name, r.Stages)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name:        r.Name,
		CloneURL:    strings.TrimSpace(r.CloneURL),
		Snapshot:    r.Snapshot,
		Branch:      strings.TrimSpace(r.Branch),
		TokenEnv:    strings.TrimSpace(r.TokenEnv),
		TokenUser:   strings.TrimSpace(r.TokenUser),
		TokenAuth:   strings.TrimSpace(r.TokenAuth),
		Enabled:     enabled,
		Project:     project,
		Part:        strings.TrimSpace(r.Part),
		Description: strings.TrimSpace(r.Description),
		Uses:        trimAll(r.Uses),
		Stages:      stages,
	}, nil
}

// stageStopWords are words a stage may not be called or aliased, because a
// question containing one narrows the whole turn to that stage. "How is the
// X integration done" is a question about code, and answering it from the
// intg directory alone — saying the other stages were not looked at — is a
// wrong answer rather than a missed one. A stage called intg is still
// reachable by that name, and the understanding step maps "the integration
// environment" onto it; only the bare word is refused as a trigger.
var stageStopWords = map[string]bool{
	"system": true, "integration": true, "test": true, "testing": true,
	"development": true, "dev": true, "staging": true, "stage": true,
	"local": true, "live": true,
}

// loadStages validates one entry's stage block: every stage named, under a
// relative directory, each name and alias once within the entry.
func loadStages(repo string, raw []rawStage) ([]Stage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []Stage
	for i, r := range raw {
		name := strings.ToLower(strings.TrimSpace(r.Name))
		if name == "" {
			return nil, fmt.Errorf("%s: stage %d is a stage with no name", repo, i)
		}
		if stageStopWords[name] {
			return nil, fmt.Errorf("%s: stage %q is an ordinary word, which would narrow every question containing it to that stage", repo, name)
		}
		prefix := strings.TrimSpace(r.Path)
		prefix = strings.TrimSuffix(prefix, "**")
		prefix = strings.TrimSuffix(prefix, "/") + "/"
		if prefix == "/" {
			return nil, fmt.Errorf("%s: stage %q has no path", repo, name)
		}
		if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
			return nil, fmt.Errorf("%s: stage %q path %q must be relative to the repository root", repo, name, r.Path)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: stage %q is named twice", repo, name)
		}
		seen[name] = true
		var aliases []string
		for _, a := range r.Aliases {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" {
				continue
			}
			if stageStopWords[a] {
				return nil, fmt.Errorf("%s: stage %q alias %q is an ordinary word, which would narrow every question containing it to that stage", repo, name, a)
			}
			if seen[a] {
				return nil, fmt.Errorf("%s: stage %q alias %q names a stage twice", repo, name, a)
			}
			seen[a] = true
			aliases = append(aliases, a)
		}
		out = append(out, Stage{Name: name, Prefix: prefix, Aliases: aliases})
	}
	return out, nil
}

// validateStageWords needs the whole list: a stage name or alias that is also
// a repository or project name would mean two things in one question.
func validateStageWords(specs []Spec) error {
	repo := map[string]bool{}
	project := map[string]bool{}
	for _, s := range specs {
		repo[strings.ToLower(s.Name)] = true
		project[strings.ToLower(s.Project)] = true
	}
	for _, s := range specs {
		for _, st := range s.Stages {
			for _, w := range append([]string{st.Name}, st.Aliases...) {
				if repo[w] {
					return fmt.Errorf("%s: stage word %q is also the name of a repository", s.Name, w)
				}
				if project[w] {
					return fmt.Errorf("%s: stage word %q is also the name of a project", s.Name, w)
				}
			}
		}
	}
	return nil
}

// trimAll copies a YAML string list with each entry trimmed, dropping empties.
// nil in, nil out: an absent `uses` and an empty one mean the same thing.
func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// validateProjects enforces the rules that need the whole list.
func validateProjects(specs []Spec) error {
	project := make(map[string]string, len(specs)) // repository -> its project
	size := make(map[string]int, len(specs))       // project -> how many repositories
	for _, s := range specs {
		project[s.Name] = s.Project
		size[s.Project]++
	}

	// A project name shares one namespace with repository names, because a card
	// button carries one of each. Naming a project after a repository that is
	// NOT its member makes that button mean two things. Naming it after its own
	// only member is the ordinary single-repository setup and is fine.
	//
	// Its own member is NOT enough once the project has a second one. The name
	// then resolves to both the repository and the whole product, so naming the
	// repository expands the search to its siblings: a thread that widens,
	// which is the one thing the funnel forbids. Rejected here rather than
	// disambiguated later, because there is no signal that could disambiguate
	// it — the reader typed one word for two things.
	for _, s := range specs {
		p, ok := project[s.Project]
		if !ok {
			continue
		}
		if p != s.Project {
			return fmt.Errorf(
				"%s: project %q is also the name of a repository that is not in it — project and repository names share one namespace",
				s.Name, s.Project)
		}
		if size[s.Project] > 1 {
			return fmt.Errorf(
				"%s: project %q is also the name of one of its own repositories and has %d of them — that name would mean both, and naming the repository would widen the search to its siblings",
				s.Name, s.Project, size[s.Project])
		}
	}

	// A uses edge stays inside one product or points at a library: the target
	// must exist, not be the entry itself, and either sit in the same project
	// or be a library. A library may only use other libraries — an edge from
	// a shared thing into one product would make it that product's. A cycle
	// between siblings is allowed — two backends calling each other is real,
	// and layoutFlow removes back edges by DFS before it ranks.
	library := make(map[string]bool, len(specs))
	for _, s := range specs {
		if s.Library {
			library[s.Name] = true
		}
	}
	for _, s := range specs {
		for _, u := range s.Uses {
			switch {
			case u == s.Name:
				return fmt.Errorf("%s: uses names itself", s.Name)
			case project[u] == "":
				return fmt.Errorf("%s: uses names %q, which is not a repository in this file", s.Name, u)
			case s.Library && !library[u]:
				return fmt.Errorf(
					"%s: uses names %q, which is in project %q — a library is shared by every product and may only use other libraries",
					s.Name, u, project[u])
			case project[u] != s.Project && !library[u]:
				return fmt.Errorf(
					"%s: uses names %q, which is in project %q not %q — coupling across projects is read from a manifest, never declared here",
					s.Name, u, project[u], s.Project)
			}
		}
	}

	// Two branches of one repository inside one project would search the same
	// file at two commits and answer as one product. AGENTS.md already forbids
	// two cards differing only by branch; this is the same rule one level up.
	//
	// Snapshots are exempt, because they all share the empty URL: their identity
	// is the directory they were extracted into, which Name already keeps unique
	// across the whole corpus. Keying them on "" would refuse a product built
	// from two drops as two branches of one repository.
	type pair struct{ project, cloneURL string }
	first := make(map[pair]string, len(specs))
	for _, s := range specs {
		if s.CloneURL == "" {
			continue
		}
		k := pair{s.Project, s.CloneURL}
		if other, ok := first[k]; ok {
			return fmt.Errorf(
				"%s and %s are the same clone_url in project %q — a project cannot hold two branches of one repository",
				other, s.Name, s.Project)
		}
		first[k] = s.Name
	}

	// A library's remote is declared ONCE, corpus-wide. The same clone_url
	// under a project as well would clone and index the shared code twice
	// and answer about it under two names — the duplication the block exists
	// to remove. Snapshots are exempt as above: their identity is the
	// directory, which Name already keeps unique.
	libraryURL := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Library && s.CloneURL != "" {
			libraryURL[s.CloneURL] = s.Name
		}
	}
	for _, s := range specs {
		if s.Library || s.CloneURL == "" {
			continue
		}
		if lib, ok := libraryURL[s.CloneURL]; ok {
			return fmt.Errorf(
				"%s in project %q is the same clone_url as library %q — a library is declared once and named with uses, never listed in a project too",
				s.Name, s.Project, lib)
		}
	}
	return nil
}

// validateSnapshot refuses the three fields that presuppose a remote.
//
// None of them could be honoured: there is nothing to authenticate to, nothing
// to resolve a default branch from, and nothing to clone. Refusing by name
// rather than ignoring them is the same rule KnownFields(true) enforces one
// level up — a field that reads as declared and does nothing is a repository
// that silently is not what it says.
func validateSnapshot(r rawSpec) error {
	for _, f := range []struct{ name, value string }{
		{"clone_url", r.CloneURL},
		{"branch", r.Branch},
		{"token_env", r.TokenEnv},
		{"token_user", r.TokenUser},
		{"token_auth", r.TokenAuth},
	} {
		if strings.TrimSpace(f.value) != "" {
			return fmt.Errorf(
				"%s: snapshot: true cannot be combined with %s — a snapshot is a directory extracted into the repository root by hand, with no remote to clone, authenticate to or resolve a branch from",
				r.Name, f.name)
		}
	}
	return nil
}

// validateName keeps the name usable as a single directory segment under the
// repository root. A separator or a parent reference would let the YAML write
// outside BACKEND_REPO_ROOT.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || name == "." {
		return fmt.Errorf(
			"name %q must be a single path segment without separators or parent references", name)
	}
	return nil
}

// errCredential is the message used for every rejection: the token belongs in
// an env var named by token_env, never inline in the URL.
func errCredential(name string) error {
	return fmt.Errorf(
		"%s: clone_url must not embed credentials — put the token in an env var and name it with token_env",
		name)
}

// validateToken refuses a token declaration that could not do anything. A
// token_env on an ssh remote never reached git — authURL injects into https
// only — so a private repository over ssh looked configured and fetched with
// whatever key the process had. A token_user without a token_env is the same
// shape: declared, and doing nothing.
func validateToken(r rawSpec) error {
	tokenEnv := strings.TrimSpace(r.TokenEnv)
	tokenUser := strings.TrimSpace(r.TokenUser)
	if tokenEnv != "" && !isHTTPRemote(r.CloneURL) {
		return fmt.Errorf(
			"%s: token_env only applies to an https remote; an ssh remote authenticates with the key named by BACKEND_GIT_SSH_KEY",
			r.Name)
	}
	if tokenUser != "" && tokenEnv == "" {
		return fmt.Errorf("%s: token_user needs a token_env to go with it", r.Name)
	}
	switch tokenAuth := strings.TrimSpace(r.TokenAuth); tokenAuth {
	case "":
	case "bearer":
		if tokenEnv == "" {
			return fmt.Errorf("%s: token_auth needs a token_env to go with it", r.Name)
		}
		// A bearer header carries no username; one written here would be
		// silently ignored, which reads as "configured" while doing nothing.
		if tokenUser != "" {
			return fmt.Errorf("%s: token_auth: bearer sends no username, drop token_user", r.Name)
		}
	default:
		return fmt.Errorf("%s: token_auth %q is not one of: bearer (or omit it for basic auth)", r.Name, tokenAuth)
	}
	// The username is not a secret, but a token pasted where the username
	// goes would be, and it would end up in a diff of this file.
	if looksLikeCredential(tokenUser) {
		return errCredential(r.Name)
	}
	return nil
}

// isHTTPRemote reports whether the clone URL is one authURL can carry a
// token on. Scheme only: a bare host or scp-style remote has none.
func isHTTPRemote(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// validateCloneURL refuses credentials embedded in the URL.
//
// net/url.Parse is deliberately NOT used to classify this: a string like
// "user:pass@host/path" (no "//" after the scheme) parses successfully as an
// OPAQUE URL with scheme="user", Host="" and User=nil, so a check built on
// u.User silently accepts exactly the credential-bearing strings this
// function exists to reject. Instead this walks the raw string structurally.
func validateCloneURL(name, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%s: clone_url is required", name)
	}

	authority := authorityOf(trimmed)
	at := strings.Index(authority, "@")
	if at < 0 {
		return nil
	}
	userinfo := authority[:at]

	if strings.Contains(userinfo, ":") {
		// "user:password@host" — unambiguous userinfo credentials.
		return errCredential(name)
	}

	// No colon: structurally this is scp-style "user@host" (e.g.
	// git@github.com:acme/repo.git) or a full ssh URL
	// (ssh://git@bitbucket.example.com:7999/proj/repo.git), both legitimate
	// ssh remotes that must stay accepted. Still reject it when the "user" looks like a token rather
	// than a username. This is a heuristic backstop, not a guarantee —
	// "token@host" is structurally indistinguishable from "user@host", so the
	// real protection is token_env, not this check.
	if looksLikeCredential(userinfo) {
		return errCredential(name)
	}
	return nil
}

// authorityOf returns the URL's authority segment: everything after a
// "scheme://" up to the next '/', or the whole leading segment up to the
// first '/' when there is no "://" (covers scp-style and bare host:path
// forms, which net/url does not parse as authorities at all).
func authorityOf(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// credentialPrefixes are common forge-issued token shapes. Seeing one as the
// "user" in a scp-style URL means a token was pasted where a username goes.
var credentialPrefixes = []string{"ghp_", "gho_", "ghs_", "github_pat_", "glpat-", "xoxb-"}

// looksLikeCredential flags a userinfo string that reads as a token rather
// than a username: a known forge token prefix, or simply too long for a
// username (git@github.com is 3 chars; a token is typically 40+).
func looksLikeCredential(userinfo string) bool {
	if len(userinfo) > 40 {
		return true
	}
	for _, p := range credentialPrefixes {
		if strings.HasPrefix(userinfo, p) {
			return true
		}
	}
	return false
}
