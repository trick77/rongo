package edges

// Neighbour is a file on the far side of an integration edge: it names the same
// queue or route as the file asked about, and lives in a DIFFERENT repository.
//
// Table: integration_tokens
type Neighbour struct {
	Repo  string
	Path  string
	Kind  Kind
	Value string
	Line  int
}
