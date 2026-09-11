// Package units reads the structure a repository's own build declares: which
// applications, libraries and modules it is made of, and which uses which.
//
// It exists because a project of two repositories — an nx workspace with
// three Angular apps and a Maven parent with four Spring Boot services — is
// not two things to the people who ask about it, and rongo saw only two:
// repos.yaml names the repositories, the directory cut in internal/modules
// offers directories, and repodeps reads go.mod alone. Every fact here is
// written down by a build tool: project.json, tsconfig.base.json, pom.xml,
// settings.gradle and build.gradle, go.mod. Nothing is inferred from names,
// nothing is asked of a model, and nothing is read from documentation, which
// goes stale without saying so. A unit is replaced with its repository on
// every index run, so it cannot outlive the manifest that declared it.
package units

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// Kind says which manifest declared a unit and what it is.
type Kind string

const (
	KindNxApp         Kind = "nx-app"
	KindNxLib         Kind = "nx-lib"
	KindMavenService  Kind = "maven-service"
	KindMavenLibrary  Kind = "maven-library"
	KindGradleService Kind = "gradle-service"
	KindGradleLibrary Kind = "gradle-library"
	KindGoModule      Kind = "go-module"
)

// Unit is one declared piece of a repository.
type Unit struct {
	Repo string
	// Key is the unit's directory, repo-relative. "." is the repository
	// itself — a single-module Maven or Gradle build at the root — which is
	// not a part of anything: Scan returns it so its coordinates reach
	// repo_deps, and Sync, Of and Describe leave it out.
	Key  string
	Kind Kind
	// Name is what the manifest calls it.
	Name string
	// Tags are nx's, empty elsewhere.
	Tags []string
	// Publishes is the coordinate other builds pull this unit by: the Maven
	// groupId:artifactId, the npm package name, the Go module path. Empty for
	// an nx project, which is not published.
	Publishes string
}

// Dep is one edge: From uses To (a sibling unit's Key), or, with To empty,
// uses Coordinate — something outside the repository.
type Dep struct {
	Repo       string
	From       string
	To         string
	Coordinate string
}

// Read is how Scan gets at a file. It is a function rather than a file system
// because the indexer reads a commit out of a bare mirror, not a checkout.
type Read func(path string) ([]byte, error)

// Scan reads the manifests among paths and returns the units they declare and
// the dependencies written in them. Import-level dependencies (an nx app
// importing "@lib") are not here; they need the source and are linked from
// the index by LinkImports.
//
// A manifest that fails to parse is skipped: the structure is a hint for
// naming and composition, and losing a whole repository's units over one
// broken pom would be the worse trade. What was skipped is returned by name.
func Scan(repo string, paths []string, read Read) (units []Unit, deps []Dep, skipped []string) {
	sort.Strings(paths)
	has := map[string]bool{}
	for _, p := range paths {
		has[p] = true
	}
	units, deps, skipped = scanNx(repo, paths, has, read)
	u, d, s := scanMaven(repo, paths, has, read)
	units, deps, skipped = append(units, u...), append(deps, d...), append(skipped, s...)
	u, d, s = scanGradle(repo, paths, has, read)
	units, deps, skipped = append(units, u...), append(deps, d...), append(skipped, s...)
	u, d, s = scanGo(repo, paths, read)
	units, deps, skipped = append(units, u...), append(deps, d...), append(skipped, s...)
	sort.Slice(units, func(i, j int) bool { return units[i].Key < units[j].Key })
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].From != deps[j].From {
			return deps[i].From < deps[j].From
		}
		if deps[i].To != deps[j].To {
			return deps[i].To < deps[j].To
		}
		return deps[i].Coordinate < deps[j].Coordinate
	})
	return units, deps, skipped
}

// ignoredDir is a path segment under which no manifest counts: a dependency's
// own project.json or pom.xml describes somebody else's build.
func ignoredDir(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "node_modules", "vendor", "target", "build", "dist", ".git":
			return true
		}
	}
	return false
}

// --- nx ---------------------------------------------------------------------

type nxProject struct {
	Name                 string   `json:"name"`
	ProjectType          string   `json:"projectType"`
	SourceRoot           string   `json:"sourceRoot"`
	Tags                 []string `json:"tags"`
	ImplicitDependencies []string `json:"implicitDependencies"`
}

func scanNx(repo string, paths []string, has map[string]bool, read Read) (units []Unit, deps []Dep, skipped []string) {
	byName := map[string]string{}
	for _, p := range paths {
		if path.Base(p) != "project.json" || ignoredDir(p) || path.Dir(p) == "." {
			continue
		}
		body, err := read(p)
		if err != nil {
			skipped = append(skipped, p+": "+err.Error())
			continue
		}
		var pj nxProject
		if err := json.Unmarshal(body, &pj); err != nil {
			skipped = append(skipped, p+": "+err.Error())
			continue
		}
		key := path.Dir(p)
		name := pj.Name
		if name == "" {
			name = path.Base(key)
		}
		kind := KindNxLib
		if pj.ProjectType == "application" {
			kind = KindNxApp
		}
		units = append(units, Unit{Repo: repo, Key: key, Kind: kind, Name: name, Tags: pj.Tags})
		byName[name] = key
		for _, d := range pj.ImplicitDependencies {
			deps = append(deps, Dep{Repo: repo, From: key, To: d}) // resolved by name below
		}
	}
	// implicitDependencies name projects, not directories.
	for i := range deps {
		if k, ok := byName[deps[i].To]; ok {
			deps[i].To = k
		} else {
			deps[i] = Dep{}
		}
	}
	deps = compact(deps)
	return units, deps, skipped
}

// Aliases reads tsconfig.base.json's paths: an import alias and the unit
// directory it points into. Exported because LinkImports needs it at a
// different time than Scan runs — after the files are indexed.
func Aliases(paths []string, read Read) map[string]string {
	out := map[string]string{}
	for _, p := range paths {
		if path.Base(p) != "tsconfig.base.json" || ignoredDir(p) {
			continue
		}
		body, err := read(p)
		if err != nil {
			continue
		}
		var ts struct {
			CompilerOptions struct {
				Paths map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}
		if err := json.Unmarshal(stripJSONComments(body), &ts); err != nil {
			continue
		}
		base := path.Dir(p)
		for alias, targets := range ts.CompilerOptions.Paths {
			if len(targets) == 0 {
				continue
			}
			target := strings.TrimSuffix(targets[0], "*")
			if base != "." {
				target = path.Join(base, target)
			}
			out[strings.TrimSuffix(alias, "/*")] = path.Clean(target)
		}
	}
	return out
}

// stripJSONComments removes // and /* */ comments, which tsconfig files carry
// and encoding/json refuses. String contents are respected.
func stripJSONComments(b []byte) []byte {
	var out []byte
	inStr, lineC, blockC := false, false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case lineC:
			if c == '\n' {
				lineC = false
				out = append(out, c)
			}
		case blockC:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				blockC = false
				i++
			}
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			lineC = true
			i++
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			blockC = true
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// --- Maven ------------------------------------------------------------------

var (
	xmlTag = func(tag string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)<` + tag + `>\s*([^<]+?)\s*</` + tag + `>`)
	}
	pomModules  = regexp.MustCompile(`(?s)<modules>(.*?)</modules>`)
	pomModule   = xmlTag("module")
	pomDeps     = regexp.MustCompile(`(?s)<dependency>(.*?)</dependency>`)
	pomParent   = regexp.MustCompile(`(?s)<parent>(.*?)</parent>`)
	pomArtifact = xmlTag("artifactId")
	pomGroup    = xmlTag("groupId")
	pomPackage  = xmlTag("packaging")
	pomPlugins  = regexp.MustCompile(`(?s)<build>.*?</build>`)
)

func scanMaven(repo string, paths []string, has map[string]bool, read Read) (units []Unit, deps []Dep, skipped []string) {
	type pom struct {
		key, artifact, group string
		requires             []string // groupId:artifactId
		service              bool
	}
	var poms []pom
	byArtifact := map[string]string{}
	for _, p := range paths {
		if path.Base(p) != "pom.xml" || ignoredDir(p) {
			continue
		}
		body, err := read(p)
		if err != nil {
			skipped = append(skipped, p+": "+err.Error())
			continue
		}
		s := string(body)
		// The project's own coordinates sit outside <parent> and outside
		// <dependencies>; cutting those blocks out first is what keeps the
		// parent's artifactId from being read as this module's.
		own := pomDeps.ReplaceAllString(pomParent.ReplaceAllString(pomPlugins.ReplaceAllString(s, ""), ""), "")
		artifact := first(pomArtifact, own)
		if artifact == "" {
			skipped = append(skipped, p+": no artifactId")
			continue
		}
		group := first(pomGroup, own)
		if group == "" {
			if parent := pomParent.FindStringSubmatch(s); parent != nil {
				group = first(pomGroup, parent[1])
			}
		}
		if first(pomPackage, own) == "pom" {
			// An aggregator or a parent: a build of builds, not a unit.
			continue
		}
		pm := pom{key: path.Dir(p), artifact: artifact, group: group,
			service: strings.Contains(s, "spring-boot-maven-plugin")}
		for _, d := range pomDeps.FindAllStringSubmatch(s, -1) {
			a := first(pomArtifact, d[1])
			g := first(pomGroup, d[1])
			if a == "" {
				continue
			}
			if g == "" || strings.Contains(g, "${") {
				g = group
			}
			pm.requires = append(pm.requires, g+":"+a)
		}
		poms = append(poms, pm)
		byArtifact[artifact] = pm.key
	}
	for _, pm := range poms {
		kind := KindMavenLibrary
		if pm.service {
			kind = KindMavenService
		}
		units = append(units, Unit{Repo: repo, Key: pm.key, Kind: kind, Name: pm.artifact, Publishes: pm.group + ":" + pm.artifact})
		for _, req := range pm.requires {
			a := req[strings.LastIndex(req, ":")+1:]
			if k, ok := byArtifact[a]; ok && k != pm.key {
				deps = append(deps, Dep{Repo: repo, From: pm.key, To: k})
			} else if !ok {
				deps = append(deps, Dep{Repo: repo, From: pm.key, Coordinate: req})
			}
		}
	}
	return units, deps, skipped
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// --- Gradle -----------------------------------------------------------------

var (
	gradleInclude   = regexp.MustCompile(`(?m)^\s*include\s*\(?\s*((?:["'][^"']+["']\s*,?\s*)+)\)?`)
	gradleQuoted    = regexp.MustCompile(`["']([^"']+)["']`)
	gradleProject   = regexp.MustCompile(`project\s*\(\s*["']([^"']+)["']\s*\)`)
	gradleCoord     = regexp.MustCompile(`["']([A-Za-z0-9_.\-]+):([A-Za-z0-9_.\-]+)(?::[^"']*)?["']`)
	gradleDepsBlock = regexp.MustCompile(`(?s)dependencies\s*\{(.*)`)
	gradleGroup     = regexp.MustCompile(`(?m)^\s*group\s*=?\s*["']([^"']+)["']`)
)

func scanGradle(repo string, paths []string, has map[string]bool, read Read) (units []Unit, deps []Dep, skipped []string) {
	var settings string
	for _, p := range paths {
		if (p == "settings.gradle" || p == "settings.gradle.kts") && !ignoredDir(p) {
			settings = p
			break
		}
	}
	if settings == "" {
		return nil, nil, nil
	}
	body, err := read(settings)
	if err != nil {
		return nil, nil, []string{settings + ": " + err.Error()}
	}
	var keys []string
	for _, m := range gradleInclude.FindAllStringSubmatch(string(body), -1) {
		for _, q := range gradleQuoted.FindAllStringSubmatch(m[1], -1) {
			keys = append(keys, strings.ReplaceAll(strings.TrimPrefix(q[1], ":"), ":", "/"))
		}
	}
	byPath := map[string]string{} // gradle project path (":a:b") -> key
	for _, k := range keys {
		byPath[":"+strings.ReplaceAll(k, "/", ":")] = k
	}
	for _, k := range keys {
		build := ""
		for _, candidate := range []string{path.Join(k, "build.gradle.kts"), path.Join(k, "build.gradle")} {
			if has[candidate] {
				build = candidate
				break
			}
		}
		kind := KindGradleLibrary
		var group string
		var requires []Dep
		if build != "" {
			b, err := read(build)
			if err != nil {
				skipped = append(skipped, build+": "+err.Error())
			} else {
				s := string(b)
				if strings.Contains(s, "org.springframework.boot") {
					kind = KindGradleService
				}
				group = first(gradleGroup, s)
				if dm := gradleDepsBlock.FindStringSubmatch(s); dm != nil {
					for _, m := range gradleProject.FindAllStringSubmatch(dm[1], -1) {
						if to, ok := byPath[m[1]]; ok && to != k {
							requires = append(requires, Dep{Repo: repo, From: k, To: to})
						}
					}
					for _, m := range gradleCoord.FindAllStringSubmatch(dm[1], -1) {
						requires = append(requires, Dep{Repo: repo, From: k, Coordinate: m[1] + ":" + m[2]})
					}
				}
			}
		}
		publishes := ""
		if group != "" {
			publishes = group + ":" + path.Base(k)
		}
		units = append(units, Unit{Repo: repo, Key: k, Kind: kind, Name: path.Base(k), Publishes: publishes})
		deps = append(deps, requires...)
	}
	return units, deps, skipped
}

// --- Go ---------------------------------------------------------------------

func scanGo(repo string, paths []string, read Read) (units []Unit, deps []Dep, skipped []string) {
	for _, p := range paths {
		if path.Base(p) != "go.mod" || ignoredDir(p) || path.Dir(p) == "." {
			// A go.mod at the root is the repository, not a part of it.
			continue
		}
		body, err := read(p)
		if err != nil {
			skipped = append(skipped, p+": "+err.Error())
			continue
		}
		f, err := modfile.Parse(p, body, nil)
		if err != nil || f.Module == nil {
			skipped = append(skipped, p+": not a go.mod")
			continue
		}
		mp := f.Module.Mod.Path
		units = append(units, Unit{Repo: repo, Key: path.Dir(p), Kind: KindGoModule, Name: path.Base(mp), Publishes: mp})
	}
	return units, deps, skipped
}

// --- helpers ----------------------------------------------------------------

func compact(deps []Dep) []Dep {
	out := deps[:0]
	seen := map[Dep]bool{}
	for _, d := range deps {
		if d.From == "" || (d.To == "" && d.Coordinate == "") || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Of returns the unit a repo-relative path belongs to, by longest key prefix,
// or nil. Units are the repository's, in any order.
func Of(us []Unit, filePath string) *Unit {
	var best *Unit
	for i := range us {
		if us[i].Key == "." {
			continue
		}
		k := us[i].Key + "/"
		if strings.HasPrefix(filePath, k) && (best == nil || len(us[i].Key) > len(best.Key)) {
			best = &us[i]
		}
	}
	return best
}
