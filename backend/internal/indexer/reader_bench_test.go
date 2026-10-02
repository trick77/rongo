package indexer

import (
	"fmt"
	"testing"
)

// BenchmarkIndexRepo_reads is one full index of a 300-file repository through
// real git and real ctags, read through the batch reader and read a process
// per file. Embedding is a fake, so the figure is the pipeline's own cost and
// the difference between the arms is the read.
func BenchmarkIndexRepo_reads(b *testing.B) {
	files := map[string]string{}
	for i := range 300 {
		files[fmt.Sprintf("src/shop/cart/Job%03d.java", i)] = cartJava
	}
	for _, arm := range []struct {
		name   string
		refuse bool
	}{{"one process per run", false}, {"one process per file", true}} {
		b.Run(arm.name, func(b *testing.B) {
			h := newHarnessFiles(b, files, nil)
			h.ix.git = &countingGit{Client: h.gitc, refuse: arm.refuse}
			st, sha := h.stateOf(b), h.head(b)
			for b.Loop() {
				if _, err := h.ix.IndexRepo(b.Context(), st, sha, nil); err != nil {
					b.Fatalf("IndexRepo: %v", err)
				}
			}
		})
	}
}
