package retrieve

import "testing"

func TestIsDocPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"README.md", true},
		{"AGENTS.md", true},
		{"docs/manual-verification.md", true},
		{"docs/measurements/2026-08-19-candidates.md", true},
		{"doc/architecture.adoc", true},
		{"documentation/index.rst", true},
		{"README", true},
		{"README.txt", true},
		{"CHANGELOG", true},
		{"LICENSE", true},
		{"contributing.markdown", true},
		{"ui/docs/examples.go", true},

		// Near misses: none of these is prose about the mechanism.
		{"backend/internal/store/migrations/0001_init.sql", false},
		{"backend/internal/llm/client.go", false},
		{"repos.example.yaml", false},
		{"requirements.txt", false},
		{"CMakeLists.txt", false},
		// A prose stem on a source file is source. Without the extension in
		// the rule these read as documentation, and a question answered
		// entirely out of one of them would be told it had no code.
		{"pkg/license/license.go", false},
		{"backend/internal/notice.go", false},
		{"scripts/authors.py", false},
		{"src/main/java/com/example/Changelog.java", false},
		{"ui/src/readme.tsx", false},
		{"spec/openapi.yaml", false},
		{"ui/src/Ask.tsx", false},
		{"backend/internal/ask/document.go", false},
		{"compose.yaml", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsDocPath(c.path); got != c.want {
			t.Errorf("IsDocPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
