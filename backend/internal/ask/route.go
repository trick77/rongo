package ask

import (
	"sort"
	"strings"

	"github.com/trick77/rongo/internal/retrieve"
)

// Candidate is one place an answer could come from: a module in a repository,
// with the hits that put it on the list.
type Candidate struct {
	Repo      string
	Branch    string
	ModuleKey string
	// Title and Summary are written per turn by the naming call, and only when
	// the reader will actually see them. Bare module keys do not work as
	// titles: peeq and loom both have httpapi, and a card offering "httpapi"
	// against "httpapi" is a question without content.
	Title   string
	Summary string
	// Score is the candidate's BEST hit, never the sum. Summing rewards size,
	// and phase 3 measured that doing so makes the ranking worse.
	Score float64
	Hits  []retrieve.Hit
}

// candidates groups hits into the units routing reasons about, best first.
func candidates(hits []retrieve.Hit, moduleOf func(repo, path string) string) []Candidate {
	index := map[string]int{}
	var out []Candidate
	for _, h := range hits {
		key := moduleOf(h.Repo, h.Path)
		id := h.Repo + "\x00" + key
		i, ok := index[id]
		if !ok {
			out = append(out, Candidate{Repo: h.Repo, Branch: h.Branch, ModuleKey: key})
			i = len(out) - 1
			index[id] = i
		}
		out[i].Hits = append(out[i].Hits, h)
		if h.Score > out[i].Score {
			out[i].Score = h.Score
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// dominates reports whether the leading candidate is far enough ahead to answer
// without asking. Relative, not absolute: fused scores have no fixed range, so
// a constant gap would mean something different for every question.
func dominates(cs []Candidate, margin float64) bool {
	if len(cs) < 2 {
		return true
	}
	if cs[0].Score <= 0 {
		return false
	}
	return (cs[0].Score-cs[1].Score)/cs[0].Score > margin
}

// lastSlash finds the final path separator, so a directory can be taken as a
// module key without pulling in path/filepath's OS-specific behaviour.
func lastSlash(p string) int {
	return strings.LastIndexByte(p, '/')
}
