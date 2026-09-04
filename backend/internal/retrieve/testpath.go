package retrieve

import "strings"

// testDirs are path segments whose contents are test material rather than the
// mechanism a reader asked about. A bare "spec" is deliberately NOT here: API
// and OpenAPI specifications live in spec/, and demoting a contract is a worse
// mistake than keeping a spec file at full standing.
var testDirs = map[string]bool{
	"test":      true,
	"tests":     true,
	"__tests__": true,
	"testdata":  true,
	"testutil":  true,
}

// testSuffixes are file-name endings that mark a test across the languages in
// the corpus: Go, Python, TypeScript, Java, C# and Ruby.
var testSuffixes = []string{
	"_test.go",
	"_test.py",
	"_test.ts", "_test.js",
	"_spec.rb",
	"Test.java", "Tests.java",
	"Test.cs", "Tests.cs",
}

// testInfixes are markers written between dots, the JavaScript and TypeScript
// convention: Clarify.test.tsx, Clarify.spec.ts.
//
// They are matched ONLY on the script extensions below, because the same
// spelling means something else elsewhere: openapi.spec.yaml is a contract and
// values.test.yaml is a Helm environment, and demoting either would repeat the
// mistake a bare "spec" directory segment is left out to avoid.
var testInfixes = []string{".test", ".spec"}

// scriptExts are the extensions on which a .test. or .spec. infix is read as a
// test marker.
var scriptExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".mts": true, ".cts": true,
}

// IsTestPath reports whether a repo-relative path is test material.
//
// Path shape only — no parsing, no language server. It runs over every fused
// hit, and the question it answers ("is this the mechanism or the harness
// around it?") is one a path answers correctly often enough. It is used twice:
// to demote test hits in fusion, and to keep a module that is only test code
// off the clarification card. An analyst asking how something works is never
// choosing between a test and the code it exercises.
func IsTestPath(path string) bool {
	if path == "" {
		return false
	}
	segs := strings.Split(path, "/")
	for _, s := range segs[:len(segs)-1] {
		if testDirs[strings.ToLower(s)] {
			return true
		}
	}

	base := segs[len(segs)-1]
	for _, suf := range testSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	// A "test_" prefix is Python's convention and needs the separator: "test_"
	// marks a test, "testing.go" does not.
	if strings.HasPrefix(base, "test_") {
		return true
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 && scriptExts[base[i:]] {
		for _, in := range testInfixes {
			if strings.HasSuffix(base[:i], in) {
				return true
			}
		}
	}
	return false
}
