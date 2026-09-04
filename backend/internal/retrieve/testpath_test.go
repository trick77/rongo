package retrieve

import "testing"

func TestIsTestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"backend/internal/llm/client_test.go", true},
		{"src/main/java/com/example/SessionHeaderTest.java", true},
		{"api/Handlers/OrderTests.cs", true},
		{"app/models/user_spec.rb", true},
		{"ui/src/Clarify.test.tsx", true},
		{"ui/src/Clarify.spec.ts", true},
		{"svc/handler_test.py", true},
		{"svc/test_handler.py", true},
		{"backend/internal/store/testdata/seed.sql", true},
		{"backend/internal/httpapi/testutil/fake.go", true},
		{"src/test/java/com/example/Thing.java", true},
		{"ui/__tests__/render.js", true},
		{"tests/e2e/login.ts", true},

		// Near misses: none of these is test code.
		{"backend/internal/retrieve/latest.go", false},
		{"backend/internal/ask/contest.go", false},
		{"spec/openapi.yaml", false},
		{"backend/internal/llm/client.go", false},
		{"ui/src/Clarify.tsx", false},
		{"protest/manifesto.md", false},
		{"api/openapi.spec.yaml", false},
		{"deploy/values.test.yaml", false},
		{"src/service.spec.md", false},
	}
	for _, c := range cases {
		if got := IsTestPath(c.path); got != c.want {
			t.Errorf("IsTestPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
