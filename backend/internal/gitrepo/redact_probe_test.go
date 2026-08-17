package gitrepo

import "testing"

// TestRedact_realisticGitErrorShapes feeds redact the way git actually reports a
// failed authenticated fetch: the remote appears inside single quotes, often
// followed by a colon. redact splits on whitespace and relies on url.Parse
// returning a non-nil User, so surrounding punctuation is exactly what could
// defeat it — and the token is live at that moment.
func TestRedact_realisticGitErrorShapes(t *testing.T) {
	cases := []string{
		"fatal: could not read Username for 'https://x-access-token:ghp_realsecret@example.invalid': terminal prompts disabled",
		"fatal: Authentication failed for 'https://x-access-token:ghp_realsecret@example.invalid/a.git/'",
		"fatal: unable to access 'https://x-access-token:ghp_realsecret@example.invalid/a.git/': The requested URL returned error: 403",
	}
	for _, in := range cases {
		out := redact(in)
		if contains(out, "ghp_realsecret") {
			t.Errorf("token leaked through redact\n  in:  %q\n  out: %q", in, out)
		}
	}
}
