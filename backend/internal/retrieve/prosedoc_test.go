package retrieve

import "testing"

// IsProseDoc is the predicate the diagram citation filter reads, and it is
// deliberately narrower than IsDocPath: dropping a chip is not recoverable
// the way a demotion is.
func TestIsProseDoc_readsTheFileNotTheDirectory(t *testing.T) {
	for path, want := range map[string]bool{
		"README.md":                      true,
		"docs/architecture.md":           true,
		"docs/notes/README":              true,
		"CHANGELOG":                      true,
		"README.txt":                     true,
		"backend/internal/ask/answer.go": false,
		// The reason the predicate exists: the mechanism of the
		// documentation build is code, and a node drawn from it keeps its
		// chip even though IsDocPath demotes the file in fusion.
		"docs/conf.py":              false,
		"docs/gen.go":               false,
		"documentation/build.sh":    false,
		"requirements.txt":          false,
		"CMakeLists.txt":            false,
		"store/migrations/0001.sql": false,
	} {
		if got := IsProseDoc(path); got != want {
			t.Errorf("IsProseDoc(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsDocPath_stillDemotesEverythingUnderADocDirectory(t *testing.T) {
	// The fusion predicate is unchanged: there a false positive costs rank,
	// which is worth the directory rule's mistakes.
	for _, path := range []string{"docs/conf.py", "docs/gen.go", "documentation/build.sh"} {
		if !IsDocPath(path) {
			t.Errorf("IsDocPath(%q) = false, want true", path)
		}
	}
}
