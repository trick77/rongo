// Package redact removes credential VALUES from configuration files before
// anything downstream sees them: the chunker, the embedding endpoint, the
// full-text index, the reranker's excerpt, the edge extractor and the source
// viewer. One function, called at every one of those places, so the chunk a
// citation points at and the file the viewer opens are the same bytes.
//
// It is line-level and keeps the key: "db.password=<redacted>" still answers
// "is the database password configured, and where", which is a real question.
// What it never does is let the value through. A missed exotic shape is
// acceptable; an obvious one leaving the machine is not, which is why the rules
// lean towards redacting a harmless value (a keystore path under a key that
// says "password") rather than keeping a harmful one.
package redact

import (
	"path"
	"regexp"
	"strings"
)

// Marker is what a redacted value reads as. It is visible text on purpose:
// the reader and the model are told a credential was here, and the answer
// prompt says never to guess what it was.
const Marker = "<redacted>"

// configExt is where line redaction runs at all. Source files are not in it:
// a `key = value` rule over Go or Java would redact `password := os.Getenv(…)`
// out of the corpus, and those files are covered by the whole-file credential
// shapes in the indexer instead.
var configExt = map[string]bool{
	".properties": true, ".yaml": true, ".yml": true, ".conf": true,
	".env": true, ".toml": true, ".ini": true, ".json": true,
	".tfvars": true, ".cfg": true, ".xml": true, ".config": true,
}

// IsConfigPath reports whether a path is a configuration file this package
// redacts. ".env" has no extension in path.Ext's eyes, so it is matched on
// the base name.
func IsConfigPath(p string) bool {
	if configExt[strings.ToLower(path.Ext(p))] {
		return true
	}
	// .env, .env.prod, .env.local: path.Ext reads the stage suffix as the
	// extension, and a stage-suffixed env file is exactly the file at stake.
	base := strings.ToLower(path.Base(p))
	return base == ".env" || strings.HasPrefix(base, ".env.") || base == ".npmrc"
}

// keyLine is the one shape every supported format shares: optional indent
// and yaml list dash, an optionally quoted key, a separator, the rest. The
// key's character class is deliberately narrow so "https://host/x" is never
// read as key "https" with value "//host/x" — the URL sits in the VALUE of a
// line like "acme.url=https://…", where the regex has already stopped at "=".
var keyLine = regexp.MustCompile(`^(\s*(?:-\s+)?(?:export\s+)?)(["']?)([\w.\-\[\]]+)(["']?)(\s*[=:]\s*)(.*)$`)

// scopedLine is an .npmrc key: a registry URL, a colon, then the key —
// "//registry.npmjs.org/:_authToken=…". The URL part is carried as the
// indent so the key is judged on its own.
var scopedLine = regexp.MustCompile(`^(\s*//[^=\s]*:)()([\w.\-]+)()(\s*=\s*)(.*)$`)

// propsSpaceLine is the Java properties separator nobody uses on purpose: a
// run of whitespace with no "=" or ":" at all. Only read in .properties
// files, where it is the format; anywhere else two words are prose.
var propsSpaceLine = regexp.MustCompile(`^(\s*)()([\w.\-\[\]]+)()(\s+)(\S.*)$`)

// secretNames are the key names an inline rule looks for where the line has
// no key of its own: inside minified JSON, a yaml flow mapping, an XML
// attribute list. Narrower than isSecretKey on purpose — mid-line, "pass"
// and "key" are everywhere.
const secretNames = `\w*(?:password|passwd|passphrase|passwort|secret|token|apikey|api_key|api-key|credential|accesskey|access_key)\w*`

// inlineKeyed is `password: x`, `"password":"x"`, `password="x"` wherever it
// stands in a line that has no key of its own. The value stops at a quote,
// a comma, a brace or whitespace, and never starts with "$": a placeholder
// is a pointer, not a value.
var inlineKeyed = regexp.MustCompile(`(?i)(["']?)(` + secretNames + `)(["']?)(\s*[:=]\s*)(["']?)([^"'\s,;}{$][^"'\s,;}]*)`)

// xmlNamedValue is a name/value attribute pair whose NAME says secret:
// Spring's <property name="password" value="…"/>, web.config's <add
// key="ApiToken" value="…"/>. The key is in one attribute and the value in
// the next, the XML shape of the Kubernetes env pair.
var xmlNamedValue = regexp.MustCompile(`(?i)\b(name|key)=(["'])(` + secretNames + `)(["'])(\s+value=)(["'])([^"']*)(["'])`)

// xmlElement is <password>…</password>: Maven's settings.xml, and every
// hand-written XML config with a credential element.
var xmlElement = regexp.MustCompile(`(?i)<(` + secretNames + `)>([^<]+)</`)

// secretKeyWords are matched inside the key with "-" and "_" removed and the
// case folded, so consumer-key, consumerKey and CONSUMER_KEY are one word.
// Bare "key" is not here: key-serializer and keystorePath are not secrets,
// and a keystore PASSWORD is caught by "password".
var secretKeyWords = []string{
	"password", "passwd", "pwd", "secret", "token", "credential",
	"apikey", "accesskey", "authkey", "signingkey", "encryptionkey",
	"private", "consumerkey", "masterkey", "jaas", "passphrase",
	"passwort", "kennwort", "sessionid",
}

// secretKeySegments are matched as a whole segment of the key — between
// dots, dashes, underscores or camel-case humps — because as substrings they
// are everywhere: "pass" in bypass and passthrough. "acme.api.pass" is a
// credential.
var secretKeySegments = map[string]bool{"pass": true, "pw": true}

// secretLastSegments are matched as the key's LAST segment only: "key" ends
// acme.maps.key and acme.mapsKey, which are credentials, and begins
// key-serializer, which is not. A key whose only segment is "key" (a yaml
// configMapKeyRef) is a name, not a value, and stays.
var secretLastSegments = map[string]bool{"key": true}

// segmentSplit finds the boundaries inside a key: separators and the start
// of a camel-case hump.
var segmentSplit = regexp.MustCompile(`[.\-_\[\]]+|(?:[a-z0-9])(?:[A-Z])`)

// secretValueShapes are values that are credentials under ANY key. Each is
// anchored to the whole value: unanchored, the base64 rule would eat a
// registry image name or a URL path, which are exactly the values an infra
// answer is made of.
var secretValueShapes = []*regexp.Regexp{
	regexp.MustCompile(`^ENC\(.*\)$`),                                                   // jasypt
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`),       // JWT
	regexp.MustCompile(`^[A-Za-z0-9+/]{40,}={0,2}$`),                                    // base64 blob
	regexp.MustCompile(`^[0-9a-fA-F]{32,}$`),                                            // hex digest or key
	regexp.MustCompile(`(?i)(password|secret)\s*=`),                                     // a JAAS line carries its password inline
	regexp.MustCompile(`^(?i)(basic|bearer)\s+\S+$`),                                    // an Authorization header value
	regexp.MustCompile(`://[^/\s@:]+:[^/\s@]+@`),                                        // credentials inside a URL
	regexp.MustCompile(`^(?i)(AKIA|ASIA)[0-9A-Z]{16}$`),                                 // AWS access key id
	regexp.MustCompile(`^(?i)(gh[pousr]_|github_pat_|glpat-|xox[baprs]-|sk-|npm_)\S+$`), // vendor token prefixes
}

// inlineShapes are credentials recognisable wherever they stand in a line
// that has no key at all — an nginx `proxy_set_header Authorization "Basic
// …";`, a shell export in a .conf. Only the shapes that are unmistakable
// mid-line, each fenced by a word boundary and a minimum length: the first
// draft matched "sk-" inside task-scheduler and "Basic settings" in a
// comment, and rewrote resource lists a kustomization is made of. The
// base64 and hex rules stay anchored to a whole value, where a key says
// what the value is.
var inlineShapes = regexp.MustCompile(`\bENC\([^)]*\)` +
	`|\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*` +
	`|\b(?i:basic)\s+[A-Za-z0-9+/]{12,}={0,2}` +
	`|\b(?i:bearer)\s+[A-Za-z0-9._~+/-]{20,}` +
	`|\b(?:gh[pousr]_|github_pat_|glpat-|xox[baprs]-|sk-|npm_)[A-Za-z0-9_-]{16,}` +
	`|\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)

// urlCredentials is user:password inside a URL, on ANY line: a key line's
// whole-value rules never see a bare "https://u:p@host" list item, whose
// "https" reads as the key. Only the credential is replaced, so the host
// and the path — the part an answer needs — stay.
var urlCredentials = regexp.MustCompile(`://[^/\s@:'"]+:[^/\s@'"]+@`)

// versionSegments are keys whose value is an identifier that happens to be
// hex — a git sha under newTag, a digest, a checksum. "Which version runs
// in prod" is answered from these, so the value shapes do not apply under
// them; a key that NAMES a secret still is one.
var versionSegments = map[string]bool{
	"tag": true, "newtag": true, "sha": true, "commit": true, "rev": true,
	"revision": true, "version": true, "digest": true, "checksum": true,
	"hash": true, "image": true, "id": true,
}

// placeholder is "${ENV_VAR}" or "${a.key:default}": a pointer to where the
// value lives, not the value. Kept even under a secret key.
var placeholder = regexp.MustCompile(`^\$\{[^}\s]*\}$`)

// blockScalar is a yaml value that begins on the next lines.
var blockScalar = regexp.MustCompile(`^[|>][-+]?\d*$`)

// Redact returns body with credential values replaced by Marker. A path
// outside IsConfigPath, or a body with nothing to redact, comes back as the
// same slice.
func Redact(p string, body []byte) []byte {
	if !IsConfigPath(p) {
		return body
	}
	yaml := isYAML(p)
	properties := strings.ToLower(path.Ext(p)) == ".properties"
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	// envName is the indent of a yaml `- name: DB_PASSWORD` list item whose
	// `value:` is still to come, or -1. The Kubernetes env shape puts the key
	// in one line and the value in the next, under a key ("value") that
	// says nothing by itself.
	envName := -1
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		crlf := len(line) != len(lines[i])
		if urlCredentials.MatchString(line) {
			line = urlCredentials.ReplaceAllString(line, "://"+Marker+"@")
			lines[i] = line + eol(crlf)
			changed = true
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			m = scopedLine.FindStringSubmatch(line)
		}
		if m == nil && properties {
			m = propsSpaceLine.FindStringSubmatch(line)
		}
		if m == nil || isComment(m[1], line) {
			// No key to judge by: the shapes that carry their own key
			// mid-line, then the unmistakable values — never in a comment,
			// where "password=notreally" is what it says.
			redacted := line
			if !isComment(leadingSpace(line), line) {
				redacted = xmlNamedValue.ReplaceAllString(redacted, "${1}=${2}${3}${4}${5}${6}"+Marker+"${8}")
				redacted = xmlElement.ReplaceAllString(redacted, "<${1}>"+Marker+"</")
				redacted = inlineKeyed.ReplaceAllString(redacted, "${1}${2}${3}${4}${5}"+Marker)
			}
			redacted = inlineShapes.ReplaceAllString(redacted, Marker)
			if redacted != line {
				out = append(out, redacted+eol(crlf))
				changed = true
				continue
			}
			out = append(out, lines[i])
			continue
		}
		indent, key, sep, rest := m[1], m[3], m[5], m[6]
		if envName >= 0 && indentWidth(indent) <= envName {
			// The next list item, or a dedent: the name no longer applies.
			envName = -1
		}
		secretKey := isSecretKey(key) || (envName >= 0 && key == "value")
		value, trail := splitTrail(rest)
		unquoted, quote := unquote(value)
		if yaml && key == "name" && isSecretKey(unquoted) {
			envName = indentWidth(indent)
		}

		if yaml && secretKey && blockScalar.MatchString(unquoted) {
			// The value is the indented block below. Drop it whole; the
			// marker on the key line stands for it.
			out = append(out, m[1]+m[2]+key+m[4]+sep+Marker+eol(crlf))
			depth := indentWidth(indent)
			for i+1 < len(lines) {
				next := strings.TrimSuffix(lines[i+1], "\r")
				if strings.TrimSpace(next) != "" && indentWidth(next) <= depth {
					break
				}
				i++
			}
			changed = true
			continue
		}
		if unquoted == "" || placeholder.MatchString(unquoted) {
			out = append(out, lines[i])
			continue
		}
		if !secretKey && (strings.HasPrefix(unquoted, "{") || strings.HasPrefix(unquoted, "[")) {
			// A flow mapping or an inline object: the keys are inside the
			// value, and each is judged where it stands.
			if inner := inlineKeyed.ReplaceAllString(rest, "${1}${2}${3}${4}${5}"+Marker); inner != rest {
				out = append(out, m[1]+m[2]+key+m[4]+sep+inner+eol(crlf))
				changed = true
				continue
			}
		}
		if !secretKey && (isVersionKey(key) || !isSecretValue(unquoted)) {
			out = append(out, lines[i])
			continue
		}
		out = append(out, m[1]+m[2]+key+m[4]+sep+quote+Marker+quote+trail+eol(crlf))
		changed = true
	}
	if !changed {
		return body
	}
	return []byte(strings.Join(out, "\n"))
}

// SecretManifest reports a Kubernetes Secret or SealedSecret document: a
// yaml file whose top-level kind is one of the two. Nothing in such a file is
// useful once its data is redacted, so the indexer skips it whole rather than
// indexing a shell of metadata around a marker.
func SecretManifest(p string, body []byte) bool {
	if !isYAML(p) {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "kind:") {
			continue
		}
		kind, _ := unquote(strings.TrimSpace(strings.TrimPrefix(line, "kind:")))
		switch kind {
		case "Secret", "SealedSecret":
			return true
		}
	}
	return false
}

func isYAML(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

func isComment(indent, line string) bool {
	rest := line[len(indent):]
	return strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, "!") || strings.HasPrefix(rest, "//")
}

// leadingSpace is the indent of a line that matched no key rule.
func leadingSpace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func isSecretKey(key string) bool {
	folded := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	for _, w := range secretKeyWords {
		if strings.Contains(folded, w) {
			return true
		}
	}
	segs := segments(key)
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if secretKeySegments[seg] {
			return true
		}
	}
	return secretLastSegments[segs[len(segs)-1]]
}

// segments splits a key at separators and camel-case humps, lower-cased:
// "acme.jwtKeystorePassword" is acme, jwt, keystore, password.
func segments(key string) []string {
	var out []string
	start := 0
	for _, loc := range segmentSplit.FindAllStringIndex(key, -1) {
		end := loc[0]
		if key[loc[0]] != '.' && key[loc[0]] != '-' && key[loc[0]] != '_' && key[loc[0]] != '[' && key[loc[0]] != ']' {
			// A hump match covers the last lower-case letter and the first
			// upper-case one; the boundary is between them.
			end = loc[0] + 1
			loc[1] = end
		}
		if end > start {
			out = append(out, strings.ToLower(key[start:end]))
		}
		start = loc[1]
	}
	if start < len(key) {
		out = append(out, strings.ToLower(key[start:]))
	}
	return out
}

// isVersionKey reports a key whose last segment says the value is an
// identifier rather than a credential.
func isVersionKey(key string) bool {
	segs := segments(key)
	return len(segs) > 0 && versionSegments[segs[len(segs)-1]]
}

func isSecretValue(v string) bool {
	for _, re := range secretValueShapes {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

// splitTrail separates a JSON trailing comma from the value so it survives.
func splitTrail(rest string) (value, trail string) {
	rest = strings.TrimRight(rest, " \t")
	if strings.HasSuffix(rest, ",") {
		return strings.TrimRight(rest[:len(rest)-1], " \t"), ","
	}
	return rest, ""
}

// unquote strips one pair of matching quotes and reports which, so the
// marker can be written back inside them.
func unquote(v string) (string, string) {
	if len(v) >= 2 {
		if q := v[0]; (q == '"' || q == '\'') && v[len(v)-1] == q {
			return v[1 : len(v)-1], string(q)
		}
	}
	return v, ""
}

func indentWidth(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 8
		default:
			return n
		}
	}
	return n
}

func eol(crlf bool) string {
	if crlf {
		return "\r"
	}
	return ""
}
