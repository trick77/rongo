package stages

// Stage is one declared stage of one repository.
//
// Table: repo_stages
type Stage struct {
	Repo    string
	Name    string
	Prefix  string
	Aliases []string
}
