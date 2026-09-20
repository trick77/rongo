package indexer

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/trick77/rongo/internal/redact"
)

// Decision is what the selector concluded about one file.
type Decision string

// The verdicts the selector can reach about a file.
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
	// SkipData is rows, not code: a csv, a source map, a snapshot, a
	// translation catalogue, or a json/xml file above the data ceiling. Such
	// a file is machine output or reference data and never the answer to how
	// something works, while its bulk outranks the real answer for every
	// value it happens to contain.
	SkipData Decision = "data"
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
	// MaxDataBytes is the ceiling for json and xml only: above it a file is
	// data, not configuration. Manifests are exempt, whatever their size,
	// because the repository's structure is read from them.
	MaxDataBytes int
	// Exclude lists path globs, relative to the repository root, whose files
	// are skipped as SkipExcluded. Matched segment by segment: "*" and "?"
	// apply within one segment, "**" spans zero or more segments. The
	// patterns are anchored, so "docs/plans/**" excludes docs/plans/x.md but
	// neither services/x/docs/plans/x.md (write "**/docs/plans/**") nor
	// docs/plans-notes.md. Validate with ValidateExclude before use.
	Exclude []string
}

// DefaultSelectOptions is 1 MB, overridable via BACKEND_INDEX_MAX_FILE_BYTES,
// 8 KiB for data (BACKEND_INDEX_MAX_DATA_FILE_BYTES), and no exclusions: the
// default exclusion list is the config package's, so a caller that builds a
// Selector directly gets the plain rule set.
func DefaultSelectOptions() SelectOptions {
	return SelectOptions{MaxBytes: 1 << 20, MaxDataBytes: 8 << 10}
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
			// Paths are cleaned before matching and never contain these, so a
			// pattern carrying one (a gitignore-style trailing slash, a leading
			// "/" or "./", a doubled slash) could never match anything.
			switch seg {
			case "", ".", "..":
				return fmt.Errorf("exclusion pattern %q: segments are matched against a cleaned path, so a leading, trailing or doubled slash and \".\" or \"..\" never match", pat)
			}
			// "**" spans directories only as a whole segment; glued to other
			// text path.Match would quietly read it as "*".
			if strings.Contains(seg, "**") && seg != "**" {
				return fmt.Errorf("exclusion pattern %q: \"**\" must be a whole segment, as in docs/**/*.html", pat)
			}
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

// NewSelector builds a Selector. Exclusion patterns are assumed valid; main
// runs ValidateExclude at startup.
func NewSelector(opts SelectOptions) *Selector {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultSelectOptions().MaxBytes
	}
	if opts.MaxDataBytes <= 0 {
		opts.MaxDataBytes = DefaultSelectOptions().MaxDataBytes
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

// generatedDirs are directories nobody writes into by hand, each with what it
// holds, for the reason on the file row. Matched as a full path segment.
var generatedDirs = map[string]string{
	"dist":          "build output",
	"build":         "build output",
	"target":        "build output",
	"out":           "build output",
	".next":         "build output",
	"generated":     "generated code",
	"__generated__": "generated code",
	"__snapshots__": "test snapshots",
	"coverage":      "coverage reports",
	".idea":         "IDE state",
	".gradle":       "build state",
	".nx":           "build state",
	".angular":      "build state",
}

// catalogueDirs hold translation catalogues. Only a catalogue format under
// them is generated: a react-i18next app keeps its hand-written setup at
// src/i18n/index.ts, and that is the answer to "how does language switching
// work".
var catalogueDirs = map[string]bool{
	"i18n": true, "locales": true, "locale": true, "translations": true,
}

// catalogueExts are the formats a translation catalogue comes in.
var catalogueExts = map[string]bool{
	".json": true, ".xml": true, ".yaml": true, ".yml": true, ".properties": true,
	".xlf": true, ".xliff": true, ".po": true, ".pot": true, ".resx": true,
	".strings": true, ".csv": true, ".arb": true,
}

// generatedName matches "generated" as a word of the basename, so
// generated-de-CH.json, schema.generated.ts and generated_client.go are
// caught while GeneratedIdEntity.java and RegeneratedTokenEvent.kt, both
// hand-written, are not.
var generatedName = regexp.MustCompile(`(?i)(^|[-_.])generated([-_.]|$)`)

// generatedFiles are the lock files whose name does not end in .lock or .sum:
// enormous, machine-written, and never the answer to a question about how
// something works.
var generatedFiles = map[string]bool{
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
}

// generatedSuffixes are the endings generators give their output, for the
// ones that skip the marker: protobuf, Dart codegen, .NET designers.
var generatedSuffixes = []string{
	".pb.go", ".pb.ts", ".pb.js", "_pb2.py", "_pb2_grpc.py",
	".g.dart", ".freezed.dart", ".gen.go", ".gen.ts", ".designer.cs",
}

// generatedMarker is the convention generators follow: Go's "Code generated
// ... DO NOT EDIT", Facebook's "@generated", .NET's "<auto-generated>". The
// tag forms only: a README saying "the client is auto-generated from the
// spec" is prose about a generator, not its output. Checked against the head
// of the file because generators put it in the first lines.
//
// A real tag opens a comment or an annotation, so "@generated" counts only
// after start-of-text, whitespace, / , * or #. Bare, it also matched the name
// of the thing rather than the tag, and dropped hand-written code whole:
// `from '@generated'`, the TypeScript alias schadenmeldung-ui gives its
// generated client, took 278 files there, and without \b "@GeneratedValue"
// on a JPA @Id took all 43 entities of schadenmeldung-service. Both times
// the code answering the question was never indexed.
//
// An allowlist, not a quote denylist: the same workspace spells the alias
// `'^@generated/(.*)$'` in jest and /^@generated\// in vite, where the
// character before @ is ^ or /, not a quote. Those are build and test
// configuration, which is what "how is the client wired up" needs.
var generatedMarker = regexp.MustCompile(`(?i)code generated .{0,60}do not edit|(?:^|[\s/*#])@generated\b|<auto-generated`)

// dataExts are formats that hold rows, not code, whatever their size: tables,
// event logs, source maps, snapshots, translation catalogues, drawings.
var dataExts = map[string]bool{
	".csv": true, ".tsv": true, ".jsonl": true, ".ndjson": true, ".geojson": true,
	".parquet": true, ".avro": true, ".map": true, ".snap": true, ".har": true,
	".log": true, ".xlf": true, ".xliff": true, ".po": true, ".pot": true, ".mo": true,
	".resx": true, ".strings": true, ".svg": true, ".pdf": true, ".ipynb": true,
}

// sizedDataExts are the formats the data ceiling applies to. YAML is
// deliberately absent: a compose file or a Helm values file runs to 10-20 KB
// and is exactly the hand-written configuration that answers questions,
// whereas json and xml of that size are exports, fixtures or translations.
var sizedDataExts = map[string]bool{
	".json": true, ".json5": true, ".xml": true, ".xsd": true, ".wsdl": true, ".plist": true,
}

// manifestFiles are exempt from the data ceiling: internal/units reads the
// repository's structure from the manifests, and a parent pom or a workspace
// angular.json outgrows any sensible ceiling. The rest is hand-written
// configuration that routinely runs past 8 KiB and is exactly what "how is X
// configured" needs: Spring contexts, JPA, .NET appsettings, logging.
var manifestFiles = map[string]bool{
	"project.json": true, "package.json": true, "nx.json": true, "angular.json": true,
	"pom.xml": true, "composer.json": true, "deno.json": true,
	".eslintrc.json": true, "renovate.json": true,
	"applicationContext.xml": true, "persistence.xml": true, "web.xml": true,
	"logback.xml": true, "logback-spring.xml": true, "log4j2.xml": true,
	"appsettings.json": true, "launchSettings.json": true,
}

// exemptSuffixes are the families the ceiling never applies to: appsettings
// per environment, and Camunda process models, which the BPMN extractor only
// sees under .bpmn and which carry diagram geometry past any ceiling.
var exemptSuffixes = []string{".bpmn20.xml", ".bpmn.xml"}

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
	d, reason, _ := s.SelectBody(p, body)
	return d, reason
}

// SelectBody is Select returning the body the pipeline must index: for a
// configuration file that is the REDACTED body, with credential values
// replaced by redact.Marker. Every consumer downstream — ctags, the chunker,
// the embedding, the edge extractor, the content hash — reads this one, so
// what is stored, embedded and later shown is the same bytes; the source
// viewer applies the same function on its own read.
func (s *Selector) SelectBody(p string, body []byte) (Decision, string, []byte) {
	// The two cheap, certain verdicts go first. Both are decided in one pass or
	// none, while the secret scan runs eight regexes over the WHOLE body — so
	// putting it first meant fully scanning a 500 MB blob only to skip it as
	// too_large anyway, and labelling a binary that happened to match a pattern
	// "secret" when "binary" is the true reason.
	if len(body) > s.opts.MaxBytes {
		return SkipTooLarge, "larger than the configured ceiling; skipped whole rather than truncated", body
	}
	if isBinary(body) {
		return SkipBinary, "contains NUL bytes", body
	}
	// A Kubernetes Secret is nothing but its data; redacted it would be a
	// shell of metadata around a marker, so it is skipped whole.
	if redact.SecretManifest(p, body) {
		return SkipSecret, "is a Secret or SealedSecret manifest", body
	}
	// Redaction before the credential scan, so a configuration file whose
	// values are all placeholders and markers is INDEXED rather than dropped
	// for the shape of one value, and after the size check, so the line pass
	// never runs over a blob that is about to be refused anyway.
	body = redact.Redact(p, body)
	// Secrets next, and ahead of every remaining verdict: those are about
	// usefulness, this one is about not shipping a credential to a third-party
	// embedding endpoint, so it wins regardless of where the file lives.
	if pat := matchSecret(body); pat != "" {
		return SkipSecret, "matches a credential pattern (" + pat + ")", body
	}
	if d, reason := s.selectByPath(p, len(body)); d != Include {
		return d, reason, body
	}
	// Only the head: generators put the marker in the first lines, and scanning
	// a megabyte for it on every file would cost more than it saves.
	head := body
	if len(head) > 2048 {
		head = head[:2048]
	}
	if generatedMarker.Match(head) {
		return SkipGenerated, "carries a generated-code marker", body
	}
	return Include, "", body
}

// selectByPath applies every verdict that needs only the path and the size:
// the operator's list, vendored and build directories, generated names, data
// formats and the data ceiling. SelectBody runs it once the body-only checks
// are through; Sweep runs it alone over rows already indexed, whose bodies it
// never reads, so a rule that ships with a newer build retires what an older
// one embedded.
func (s *Selector) selectByPath(p string, size int) (Decision, string) {
	// The operator's list before the built-in ones: a document under an
	// excluded directory is reported as excluded, whichever other rule would
	// also have caught it.
	if pat, ok := s.Excluded(p); ok {
		return SkipExcluded, "matches exclusion pattern " + pat
	}
	if seg := vendoredSegment(p); seg != "" {
		return SkipVendored, "lives under " + seg + "/"
	}
	// The name before the size: a generated translation bundle is reported as
	// generated whether it is 3 KB or 300 KB, and a manifest under a
	// generated directory is generated like ownPaths says.
	if reason := generatedByName(p); reason != "" {
		return SkipGenerated, reason
	}
	if reason := dataReason(p, size, s.opts.MaxDataBytes); reason != "" {
		return SkipData, reason
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

// generatedSegment reports the generated directory a path lives under and
// what that directory holds.
func generatedSegment(p string) (string, string) {
	for _, seg := range strings.Split(path.Clean(p), "/") {
		if holds, ok := generatedDirs[seg]; ok {
			return seg, holds
		}
	}
	return "", ""
}

// ownPaths drops the paths that live under a vendored or build-output
// directory: the same segments Select skips, decided on the path alone. A
// manifest there describes somebody else's build and must not become a unit.
func ownPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if seg, _ := generatedSegment(p); vendoredSegment(p) == "" && seg == "" {
			out = append(out, p)
		}
	}
	return out
}

// generatedByName reports why a path is generated, from the name alone:
// generators differ, and the ones without a marker announce themselves by
// where they live or what they are called.
func generatedByName(p string) string {
	base := path.Base(p)
	lower := strings.ToLower(base)
	if generatedFiles[base] || strings.HasSuffix(lower, ".lock") || strings.HasSuffix(lower, ".sum") {
		return "is a lock file"
	}
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") {
		return "is minified"
	}
	if seg, holds := generatedSegment(p); seg != "" {
		return "lives under " + seg + "/, which holds " + holds
	}
	if seg := catalogueSegment(p); seg != "" && catalogueExts[strings.ToLower(path.Ext(base))] {
		return "is a translation catalogue under " + seg + "/"
	}
	if generatedName.MatchString(base) {
		return "is named generated"
	}
	for _, suf := range generatedSuffixes {
		if strings.HasSuffix(lower, suf) {
			return "carries the generator suffix " + suf
		}
	}
	return ""
}

// catalogueSegment reports the translation directory a path lives under.
func catalogueSegment(p string) string {
	for _, seg := range strings.Split(path.Clean(p), "/") {
		if catalogueDirs[seg] {
			return seg
		}
	}
	return ""
}

// dataReason reports why a path is data: a format that only ever holds rows,
// or json/xml above the ceiling. The detail names the ceiling for the index
// log; the row keeps the decision alone, like every other skip.
func dataReason(p string, size, maxDataBytes int) string {
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(base))
	if dataExts[ext] {
		return "is a data file (" + ext + ")"
	}
	if !sizedDataExts[ext] || size <= maxDataBytes || isManifest(base) {
		return ""
	}
	return fmt.Sprintf("%d bytes of %s, above the data ceiling of %d", size, ext[1:], maxDataBytes)
}

// isManifest reports a file the data ceiling leaves alone: a manifest, the
// tsconfig and appsettings families, a process model.
func isManifest(base string) bool {
	if manifestFiles[base] {
		return true
	}
	lower := strings.ToLower(base)
	if (strings.HasPrefix(lower, "tsconfig") || strings.HasPrefix(lower, "appsettings.")) && strings.HasSuffix(lower, ".json") {
		return true
	}
	for _, suf := range exemptSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
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
	".yaml": "yaml", ".yml": "yaml", ".json": "json", ".xml": "xml", ".properties": "properties",
	".html": "html", ".css": "css", ".scss": "scss",
	// A BPMN process model. Not a ctags language: the indexer hands it to
	// symbols.ExtractBPMN and the chunker anchors on its flow nodes.
	".bpmn": "bpmn",
}

// LanguageOf maps a path to a language by extension, returning "" when it does
// not recognise one. An unknown language is not an error: the chunker falls
// back to line windows, which is the normal path for a mixed corpus.
func LanguageOf(p string) string {
	return extLang[strings.ToLower(path.Ext(p))]
}
