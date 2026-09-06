package retrieve

import "strings"

// docDirs are path segments whose contents are prose written for a reader
// rather than the mechanism a question is about.
var docDirs = map[string]bool{
	"doc":           true,
	"docs":          true,
	"documentation": true,
}

// docExts are the markup extensions a document is written in. A bare ".txt" is
// deliberately NOT here: requirements.txt and CMakeLists.txt are mechanism, and
// the prose ones are already caught by docStems below.
var docExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".rst":      true,
	".adoc":     true,
	".asciidoc": true,
}

// docStems are basenames that are prose when they carry NO extension or a bare
// ".txt" — README, README.txt and README.rst are the same file with three
// spellings, and only the last of them has an extension docExts recognises.
//
// The extension is part of the rule, not decoration. A stem alone would make
// license.go, notice.go, authors.py and Changelog.java documentation, and every
// one of those is the mechanism: a question answered entirely out of
// license.go would be told it had no code in front of it and the reader would
// be told the same.
var docStems = map[string]bool{
	"readme":       true,
	"changelog":    true,
	"license":      true,
	"licence":      true,
	"contributing": true,
	"notice":       true,
	"authors":      true,
}

// IsProseDoc reports whether a repo-relative path is documentation by what
// the FILE is, ignoring the directory it sits in.
//
// The narrower half of IsDocPath, and it exists because the two callers pay
// different prices for a false positive. In fusion a wrong guess costs rank
// and the hit still wins if nothing else matches, so the directory rule is
// worth its mistakes. Dropping a diagram node's citation is not recoverable
// the same way: the node is drawn and the reader cannot open what it was
// drawn from. docs/conf.py and docs/gen.go are the mechanism of the
// documentation build, and a question about how the docs are generated is
// answered from them.
//
// So the bare directory rule is left out here, and only the file's own
// spelling counts.
func IsProseDoc(path string) bool {
	if path == "" {
		return false
	}
	segs := strings.Split(path, "/")
	base := strings.ToLower(segs[len(segs)-1])
	i := strings.LastIndexByte(base, '.')
	if i < 0 {
		return docStems[base]
	}
	if docExts[base[i:]] {
		return true
	}
	return base[i:] == ".txt" && docStems[base[:i]]
}

// IsDocPath reports whether a repo-relative path is documentation.
//
// Path shape only — the same discipline as IsTestPath, and for the same reason:
// it runs over every fused hit, and "is this the mechanism or prose about the
// mechanism?" is a question a path answers correctly often enough.
//
// What is left out matters as much as what is in, exactly as a bare "spec"
// segment is left out of testDirs. Migrations (store/migrations/*.sql) carry
// the schema and the reasoning for it, and .yaml/.json are configuration and
// contracts: demoting one of those is a worse mistake than keeping a document
// at full standing.
func IsDocPath(path string) bool {
	if path == "" {
		return false
	}
	segs := strings.Split(path, "/")
	for _, s := range segs[:len(segs)-1] {
		if docDirs[strings.ToLower(s)] {
			return true
		}
	}
	// The rest is the file's own spelling, which is all IsProseDoc reads:
	// ".txt" only behind one of the stems, since requirements.txt and
	// CMakeLists.txt are mechanism and README.txt is not.
	return IsProseDoc(path)
}
