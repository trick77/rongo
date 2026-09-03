package indexer

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Decision is what the selector concluded about one file.
type Decision string

const (
	Include       Decision = "include"
	SkipVendored  Decision = "vendored"
	SkipGenerated Decision = "generated"
	SkipBinary    Decision = "binary"
	SkipTooLarge  Decision = "too_large"
	SkipSecret    Decision = "secret"
	// SkipExcluded is a path the operator ruled out with BACKEND_INDEX_EXCLUDE:
	// content written for reading, not for the corpus — design documents,
	// plans, mock-ups — that is tracked in the repository but stale or wrong
	// as an answer to how the code works.
	SkipExcluded Decision = "excluded"
	// SkipEmpty is not a selector verdict — the pipeline records it for a file
	// that passed selection but produced no chunk, because it is blank. It
	// shares this vocabulary so the answer layer renders one set of reasons.
	SkipEmpty Decision = "empty"
)

// SelectOptions tunes the selector.
type SelectOptions struct {
	// MaxBytes is the ceiling above which a file is skipped WHOLE rather than
	// truncated: half a file produces confidently wrong answers about the other
	// half, which is worse than admitting the file was not indexed.
	MaxBytes int
	// Exclude lists path globs, relative to the repository root, whose files
	// are skipped as SkipExcluded. Matched segment by segment: "*" and "?"
	// apply within one segment, "**" spans zero or more segments. The
	// patterns are anchored, so "docs/plans/**" excludes docs/plans/x.md but
	// neither services/x/docs/plans/x.md (write "**/docs/plans/**") nor
	// docs/plans-notes.md. Validate with ValidateExclude before use.
	Exclude []string
}

// DefaultSelectOptions is 1 MB, overridable via BACKEND_INDEX_MAX_FILE_BYTES,
// and no exclusions: the default exclusion list is the config package's, so
// a caller that builds a Selector directly gets the plain rule set.
func DefaultSelectOptions() SelectOptions {
	return SelectOptions{MaxBytes: 1 << 20}
}

// ValidateExclude reports the first malformed exclusion pattern. It runs at
// startup so a typo fails the boot rather than silently matching nothing —
// the index would then keep the excluded content while the configuration
// looked right.
func ValidateExclude(patterns []string) error {
	for _, pat := range patterns {
		if pat == "" {
			return fmt.Errorf("exclusion pattern is empty")
		}
		for _, seg := range strings.Split(pat, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("exclusion pattern %q: %w", pat, err)
			}
		}
	}
	return nil
}

// Selector decides which files are worth indexing.
//
// Filtering happens before embedding for two reasons, and the second matters
// more: unfiltered content costs money, and it actively dilutes every result
// list — a vendored dependency's source outranks the real answer for any query
// about a common word.
type Selector struct {
	opts    SelectOptions
	exclude []excludePattern
}

// excludePattern is one exclusion glob, split once so matching a path does
// not re-split the pattern for every file.
type excludePattern struct {
	text string
	segs []string
}

// NewSelector builds a Selector. Exclusion patterns are assumed valid; the
// config package runs ValidateExclude at startup.
func NewSelector(opts SelectOptions) *Selector {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultSelectOptions().MaxBytes
	}
	s := &Selector{opts: opts}
	for _, pat := range opts.Exclude {
		s.exclude = append(s.exclude, excludePattern{text: pat, segs: strings.Split(pat, "/")})
	}
	return s
}

// Excluded reports the exclusion pattern a path matches, if any.
func (s *Selector) Excluded(p string) (string, bool) {
	segs := strings.Split(path.Clean(p), "/")
	for _, pat := range s.exclude {
		if matchSegments(pat.segs, segs) {
			return pat.text, true
		}
	}
	return "", false
}

// matchSegments matches a pattern against a path, both already split on "/".
// "**" consumes zero or more path segments; every other pattern segment must
// match exactly one path segment via path.Match.
func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		// The pattern was validated at startup, so an error here cannot occur.
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// vendoredDirs are directories whose contents belong to someone else. Matched
// as a full path segment so a file called "vendors.go" is not caught.
var vendoredDirs = map[string]bool{
	"node_modules":  true,
	"vendor":        true,
	"third_party":   true,
	"thirdparty":    true,
	".venv":         true,
	"site-packages": true,
}

// generatedDirs hold build output.
var generatedDirs = map[string]bool{
	"dist":   true,
	"build":  true,
	"target": true,
	"out":    true,
	".next":  true,
}

// generatedFiles are lock files and similar: enormous, machine-written, and
// never the answer to a question about how something works.
var generatedFiles = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"go.sum":            true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"composer.lock":     true,
	"Gemfile.lock":      true,
}

// generatedMarker is the convention generators follow. Checked against the head
// of the file because generators put it in the first lines.
var generatedMarker = regexp.MustCompile(`(?i)code generated .{0,60}do not edit`)

// secretPatterns are shapes that are credentials wherever they appear. This is
// a filter, not a scanner: it exists so an accidentally committed credential
// does not leave the network when the file is embedded. Missing an exotic
// format is acceptable; letting an obvious one through is not.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                   // AWS access key id
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), // any PEM private key
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),         // GitHub tokens
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),       // GitHub fine-grained PAT
	regexp.MustCompile(`glpat-[A-Za-z0-9\-_]{20,}`),          // GitLab PAT
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`),      // Slack tokens
	regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`),                // OpenAI-style secret key
	regexp.MustCompile(`(?i)aws_secret_access_key\s*[=:]\s*\S{20,}`),
}

// Select decides what to do with one file and returns a human-readable reason
// for anything it skips. The reason is stored on the file row so the answer
// layer can say "that file exists but was not indexed" — the "never invent"
// invariant applied to the index itself.
func (s *Selector) Select(p string, body []byte) (Decision, string) {
	// The two cheap, certain verdicts go first. Both are decided in one pass or
	// none, while the secret scan runs eight regexes over the WHOLE body — so
	// putting it first meant fully scanning a 500 MB blob only to skip it as
	// too_large anyway, and labelling a binary that happened to match a pattern
	// "secret" when "binary" is the true reason.
	if len(body) > s.opts.MaxBytes {
		return SkipTooLarge, "larger than the configured ceiling; skipped whole rather than truncated"
	}
	if isBinary(body) {
		return SkipBinary, "contains NUL bytes"
	}
	// Secrets next, and ahead of every remaining verdict: those are about
	// usefulness, this one is about not shipping a credential to a third-party
	// embedding endpoint, so it wins regardless of where the file lives.
	if pat := matchSecret(body); pat != "" {
		return SkipSecret, "matches a credential pattern (" + pat + ")"
	}
	// The operator's list before the built-in ones: a document under an
	// excluded directory is reported as excluded, whichever other rule would
	// also have caught it.
	if pat, ok := s.Excluded(p); ok {
		return SkipExcluded, "matches exclusion pattern " + pat
	}
	if seg := vendoredSegment(p); seg != "" {
		return SkipVendored, "lives under " + seg + "/"
	}
	if reason := generatedReason(p, body); reason != "" {
		return SkipGenerated, reason
	}
	return Include, ""
}

func matchSecret(body []byte) string {
	for _, re := range secretPatterns {
		if re.Match(body) {
			return re.String()
		}
	}
	return ""
}

// isBinary uses the NUL byte, the same heuristic git uses. Checking only the
// head would miss a file that is text for a megabyte and then embeds a blob.
func isBinary(body []byte) bool {
	return bytes.IndexByte(body, 0) >= 0
}

// vendoredSegment reports the vendored directory a path lives under, matching
// full segments so "vendors.go" or "my-node_modules-notes.md" are not caught.
func vendoredSegment(p string) string {
	for _, seg := range strings.Split(path.Clean(p), "/") {
		if vendoredDirs[seg] {
			return seg
		}
	}
	return ""
}

// generatedReason checks the path and then the content, because generators
// differ: some announce themselves in a marker, others only by where they live.
func generatedReason(p string, body []byte) string {
	base := path.Base(p)
	if generatedFiles[base] {
		return "is a lock file"
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return "is minified"
	}
	for _, seg := range strings.Split(path.Clean(p), "/") {
		if generatedDirs[seg] {
			return "lives under " + seg + "/, which holds build output"
		}
	}
	// Only the head: generators put the marker in the first lines, and scanning
	// a megabyte for it on every file would cost more than it saves.
	head := body
	if len(head) > 2048 {
		head = head[:2048]
	}
	if generatedMarker.Match(head) {
		return "carries a generated-code marker"
	}
	return ""
}

// extLang maps a file extension to the language name used for ctags selection
// and for the chunker's comment syntax. A wrong guess degrades chunking; it
// does not break it.
var extLang = map[string]string{
	".java": "java", ".go": "go", ".ts": "ts", ".tsx": "tsx",
	".js": "js", ".jsx": "jsx", ".py": "py", ".rb": "rb",
	".cs": "cs", ".kt": "kt", ".scala": "scala", ".rs": "rs",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
	".php": "php", ".sh": "sh", ".sql": "sql", ".md": "md",
	".yaml": "yaml", ".yml": "yaml", ".json": "json", ".xml": "xml",
	".html": "html", ".css": "css", ".scss": "scss",
}

// LanguageOf maps a path to a language by extension, returning "" when it does
// not recognise one. An unknown language is not an error: the chunker falls
// back to line windows, which is the normal path for a mixed corpus.
func LanguageOf(p string) string {
	return extLang[strings.ToLower(path.Ext(p))]
}
