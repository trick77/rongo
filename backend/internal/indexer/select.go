package indexer

import (
	"bytes"
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
)

// SelectOptions tunes the selector.
type SelectOptions struct {
	// MaxBytes is the ceiling above which a file is skipped WHOLE rather than
	// truncated: half a file produces confidently wrong answers about the other
	// half, which is worse than admitting the file was not indexed.
	MaxBytes int
}

// DefaultSelectOptions is 1 MB, overridable via BACKEND_INDEX_MAX_FILE_BYTES.
func DefaultSelectOptions() SelectOptions {
	return SelectOptions{MaxBytes: 1 << 20}
}

// Selector decides which files are worth indexing.
//
// Filtering happens before embedding for two reasons, and the second matters
// more: unfiltered content costs money, and it actively dilutes every result
// list — a vendored dependency's source outranks the real answer for any query
// about a common word.
type Selector struct {
	opts SelectOptions
}

// NewSelector builds a Selector.
func NewSelector(opts SelectOptions) *Selector {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultSelectOptions().MaxBytes
	}
	return &Selector{opts: opts}
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
	// Secrets first. Every other verdict is about usefulness; this one is about
	// not shipping a credential to a third-party embedding endpoint, so it wins
	// regardless of where the file lives.
	if pat := matchSecret(body); pat != "" {
		return SkipSecret, "matches a credential pattern (" + pat + ")"
	}
	if isBinary(body) {
		return SkipBinary, "contains NUL bytes"
	}
	if len(body) > s.opts.MaxBytes {
		return SkipTooLarge, "larger than the configured ceiling; skipped whole rather than truncated"
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
