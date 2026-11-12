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

// Landing is the chunk an M/TEXT include lands on: the head chunk of the file
// it names, where the stem is defined, with what a citation needs.
//
// Table: chunks
type Landing struct {
	ChunkID            int64
	Repo, Branch, Path string
	SHA, Symbol        string
	StartLine, EndLine int
	Text               string
	// Stem is the file's stem as its name spells it, which a language
	// variable or a case-blind match may spell differently from the include.
	Stem string
}
