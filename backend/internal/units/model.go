package units

// Unit is one declared piece of a repository.
//
// Table: units
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
//
// Table: unit_deps
type Dep struct {
	Repo       string
	From       string
	To         string
	Coordinate string
}
