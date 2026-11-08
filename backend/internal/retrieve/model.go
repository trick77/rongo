package retrieve

// Hit is one retrieved chunk, carrying everything a citation needs: repo,
// branch, path and line range. They are joined in the retrieval query rather
// than looked up afterwards, because every claim rongo makes must be citable
// and a second lookup is a second chance to lose the branch.
//
// ChunkID is the fusion key. Without it the two lanes cannot tell that they
// found the same chunk, and every hit would be counted once per lane.
//
// Table: chunks
type Hit struct {
	ChunkID   int64
	Repo      string
	Branch    string
	Path      string
	Symbol    string
	RawText   string
	StartLine int
	EndLine   int
	// Ordinal is the chunk's position in its file. It completes the address:
	// an overlong line is split into sibling chunks that START on the same
	// line, so the repository, path and start line do not tell those apart and
	// only the ordinal keeps the file in file order. It is a file position and
	// not an id, so it says the same thing after a re-index.
	Ordinal int
	// SHA is the commit the file was indexed at. A citation carries it so the
	// cited lines can be shown as they were when the answer was written, not
	// as the branch has moved on since.
	SHA string
	// Distance is the L2 distance from the query vector, set by the semantic
	// lane only. The keyword lane leaves it 0: FTS rank is positional and the
	// fusion works on rank, not on score.
	Distance float64
	// Score and Lanes are filled by FuseWeightedDecayed.
	Score float64
	Lanes []string
}
