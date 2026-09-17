// Package edges extracts the named destinations a file talks about — queue and
// topic names, HTTP routes — so that a producer and its consumer in two
// different repositories can be linked.
//
// Why this exists: ctags records definitions, the keyword lane records text and
// the vector lane records meaning. None of the three encodes that
// `shipping/…/ShippingController.java` sends to the queue that
// `queue-master/…/ShippingConsumerConfiguration.java` listens on. The two files
// share no import, no type and no symbol; they share the string
// "shipping-task", and nothing in the index knew that was a link.
//
// The measurement that made this worth building is
// docs/measurements/2026-09-10-flow-loop-diagnostic.md: a tool-using loop with
// twelve rounds and ripgrep in its hands still answered "how is the invoice
// total calculated" out of one repository, because it stopped when it had a
// plausible local answer. Its budget was not the problem. Handing it the far
// side of an edge is.
//
// EXTRACTION ONLY. Every token here is a string literal that stood next to a
// messaging call, a route annotation or a router registration in the file it is
// recorded against. Nothing is inferred, no model runs, and a token cannot age
// independently of its file — it is re-derived whenever the file is re-indexed,
// under the same content hash rule as everything else.
package edges

import (
	"path"
	"regexp"
	"strings"
)

// Kind separates the two namespaces. They are never matched against each
// other: a route "/orders" and a queue named "/orders" would be a coincidence,
// not a link.
type Kind string

const (
	// KindRoute is an HTTP path, as written in a controller annotation, a
	// router registration or a client call.
	KindRoute Kind = "route"
	// KindDestination is a queue, topic or exchange name.
	KindDestination Kind = "destination"
	// KindProperty is a configuration key: "${acme.cron.send-digest}" in
	// the code that reads it, "acme.cron.send-digest=..." in the properties
	// file that sets it. The code's placeholder and default live in one
	// repository, the deployed value per stage in another, and the key is
	// the only thing they share.
	KindProperty Kind = "property"
	// KindLink is a navigation site in a user interface: an href, a
	// window.open, a location assignment, a router call. It is NOT an edge.
	// The value is whatever text stood at the site — an absolute URL when
	// the code writes one, more often `${environment.portalUrl}/claims` or a
	// variable — because a UI repository seldom spells the host it links to
	// as one literal. Two repositories sharing the value are not linked by
	// it, so Neighbours and Holders never match on this kind; the census in
	// internal/ask reads it per repository instead.
	KindLink Kind = "link"
)

// Token is one extracted destination with the line it was found on.
type Token struct {
	Kind  Kind
	Value string
	Line  int
}

// messagingCall matches the APIs whose string argument is a destination. Spring
// AMQP and Spring Kafka first, because that is what the corpus this was built
// against uses, then the shapes other stacks write.
//
// The list is deliberately explicit rather than clever: a rule that guessed
// "any literal that looks like a topic" would fill the table with constants
// that are not destinations, and a wrong edge is worse than a missing one — it
// pulls unrelated code into an answer that then cites it.
// `send` is deliberately NOT here on its own. Express writes res.send("done")
// and the shape rule accepts "done", so a bare rule records it as a queue and
// two services doing it produce a wrong cross-repo edge — the exact failure
// this package says is worse than a missing one. Only the qualified forms.
var messagingCall = regexp.MustCompile(`(?i)\b(?:` +
	`convertAndSend|convertSendAndReceive|sendDefault|sendMessage|` + // Spring AMQP / KafkaTemplate
	`setQueueNames|setQueue|setTopics|setDestinationName|` +
	`RabbitListener|KafkaListener|JmsListener|StreamListener|` +
	`Queue|TopicExchange|DirectExchange|FanoutExchange|NewTopic|` +
	`Subscribe|Publish|QueueDeclare|BasicPublish` +
	`)\b`)

// destinationVar matches a variable or constant whose NAME says the string it
// holds is a destination. This is what catches the corpus's real case: both
// sides write `String queueName = "shipping-task"` and use the constant, so the
// literal never appears inside the call itself.
//
// The keyword must END the identifier, optionally followed by Name or Key.
// A trailing wildcard matched `exchangeRate = "USD"` and recorded USD as a
// destination; a name that merely CONTAINS "queue" is not a queue name.
var destinationVar = regexp.MustCompile(`(?i)\b\w*(?:queue|topic|exchange|channel|routingkey)(?:name|key)?\s*(?:=|:=|:)\s*"([^"]{2,120})"`)

// routeAnnotation matches the server side of an HTTP route in the frameworks
// that declare it: Spring's mapping annotations, gorilla/mux and net/http in
// Go, Express in Node.
// The two halves are matched differently on purpose. A bare name takes word
// boundaries; a method call takes the DOT, because `\b` before `\.` is not a
// boundary when the character before the dot is also non-word — which is
// exactly the shape of a chained builder, `r.Methods("POST").Path("/x")`, and
// cost this rule its first route until the test caught it.
var routeAnnotation = regexp.MustCompile(`(?i)(?:` +
	`\b(?:RequestMapping|GetMapping|PostMapping|PutMapping|DeleteMapping|PatchMapping|HandleFunc|PathPrefix)\b` +
	`|\.(?:Path|PathPrefix|Handle|HandleFunc|route|get|post|put|delete|patch|all)\s*\(` +
	`)`)

// httpClientCall matches the client side: the code that CALLS a route someone
// else serves. Without it only servers are recorded and an edge has one end.
var httpClientCall = regexp.MustCompile(`(?i)\b(?:` +
	`RestTemplate|WebClient|FeignClient|HttpClient|ServiceUri|` +
	`http\.(?:Get|Post|Put|Delete|NewRequest)|request|fetch|axios|exchange|getForObject|postForObject` +
	`)\b`)

// stringLiteral pulls double-quoted literals out of a line. Backticks and
// single quotes are deliberately included for Go and JavaScript, where a route
// is as likely to be written with either.
var stringLiteral = regexp.MustCompile("\"([^\"\\n]{1,200})\"|`([^`\\n]{1,200})`|'([^'\\n]{1,200})'")

// routeShape is what may be recorded as a route: a leading slash, then path
// characters. Spring path variables ({id}), Express wildcards (*) and gorilla
// patterns are all allowed through, because they are how the same route is
// written on the two sides of the same edge.
var routeShape = regexp.MustCompile(`^/[A-Za-z0-9_\-./{}*:$]*$`)

// uninteresting are routes every service has. They are not links: matching on
// them would join every repository to every other through /health, and an edge
// that connects everything says nothing.
var uninteresting = map[string]bool{
	"/": true, "/*": true, "/**": true, "/health": true, "/healthz": true,
	"/metrics": true, "/ready": true, "/readiness": true, "/liveness": true,
	"/ping": true, "/status": true, "/info": true, "/version": true,
	"/favicon.ico": true, "/index.html": true, "/static": true, "/api": true,
}

// codeExt is where extraction runs at all. Documentation and configuration are
// excluded on purpose: a route in a README is a claim about the code, and this
// table is for what the code does.
var codeExt = map[string]bool{
	".java": true, ".kt": true, ".scala": true, ".go": true, ".js": true,
	".jsx": true, ".ts": true, ".tsx": true, ".py": true, ".rb": true,
	".cs": true, ".php": true,
}

// placeholderExt is where a "${a.b}" inside a double-quoted string is a
// Spring property placeholder and nothing else. JavaScript, TypeScript,
// Kotlin and Scala are deliberately absent: there "${order.id}" is string
// interpolation, and recording it would join a file to whatever repository
// has a key of that name. Java has no string templates.
var placeholderExt = map[string]bool{".java": true}

// propertiesExt is where every key is a property token. Only the properties
// format: flattening arbitrary yaml would record spec.template.spec.containers
// from every manifest in every repository, and a Spring application.yaml is
// a follow-up with its own rule.
var propertiesExt = map[string]bool{".properties": true}

// propertyPlaceholder is "${key}" or "${key:default}" inside a double-quoted
// literal. The key needs a dot or a dash: "${id}" in a Java string is a
// message template far more often than a property, and a property key has
// at least one segment.
var propertyPlaceholder = regexp.MustCompile(`"[^"\n]*?\$\{([A-Za-z][\w]*[.\-][\w.\-]*)(?::[^}"]*)?\}`)

// propertyLine is one key of a properties file, "=" or ":" separated. A
// leading "#" or "!" is a comment in that format.
var propertyLine = regexp.MustCompile(`^\s*([A-Za-z][\w.\-\[\]]*)\s*[=:]`)

// linkExt is where navigation sites are read. Markup gets ONLY this branch:
// running the route rules over .html would take a prose "fetch" and every
// "/path" on that line for a client call. Script files run both.
var linkExt = map[string]bool{
	".html": true, ".htm": true, ".vue": true, ".svelte": true,
	".js": true, ".jsx": true, ".ts": true, ".tsx": true,
}

// navigationSite matches the places a user interface navigates from. As
// explicit as messagingCall, for the same reason: a rule that took every
// string with a slash for a link would record every asset path. Each
// alternative ends on the "=" or "(" the value follows, so linkValue reads
// from there. A comparison ("href ===", "location.href == x") is not a
// site; Extract checks the character after the match, RE2 having no
// lookahead.
//
// The href and location forms are the ones that need care. "data-href" is
// not href, so href takes no hyphen before it. A bare "location =" is any
// variable of that name — React's useLocation() in every routed component —
// so only the browser's own is a site: window.location, document.location,
// or location.href.
var navigationSite = regexp.MustCompile(`(?i)(?:` +
	`(?:^|[^\w-])(?:href|routerLink)\s*\]?\s*=` + // href=, [href]=, [attr.href]=, routerLink=, [routerLink]=
	`|<Link\b[^>]*?\bto\s*=` + // React Router
	`|\bwindow\.open\s*\(` +
	`|\blocation\.(?:assign|replace)\s*\(` +
	`|\b(?:window|document)\.location(?:\.href)?\s*=` +
	`|\blocation\.href\s*=` +
	`|\.navigate(?:ByUrl)?\s*\(` + // Angular Router
	`)`)

// linkObject is the text before an href that names a stylesheet element:
// "link.href =", the injected stylesheet. A "portalLink.href" is not it.
var linkObject = regexp.MustCompile(`(?i)(?:^|[^\w])link\s*\.?\s*$`)

// stylesheetSite says whether the href at line[at:] belongs to a stylesheet
// or base tag rather than a navigation: the tag opened last before it is
// <link or <base, or the script assigns link.href. Case matters on the tag:
// "<Link" is the React Router component.
func stylesheetSite(line string, at int) bool {
	before := line[:at]
	if linkObject.MatchString(before) {
		return true
	}
	open := strings.LastIndexByte(before, '<')
	if open < 0 {
		return false
	}
	tag := before[open:]
	return strings.HasPrefix(tag, "<link") || strings.HasPrefix(tag, "<base")
}

// linkSkip is a value that leads to no application: an in-page anchor,
// mail, phone, script, nothing, the bare root.
func linkSkip(v string) bool {
	if v == "" || v == "/" {
		return true
	}
	l := strings.ToLower(v)
	return strings.HasPrefix(l, "#") || strings.HasPrefix(l, "mailto:") ||
		strings.HasPrefix(l, "tel:") || strings.HasPrefix(l, "javascript:")
}

// linkMaxRunes bounds a value: past it the text is a statement, not a target.
const linkMaxRunes = 120

// Extract reads one file and returns the destinations it names.
//
// It works line by line rather than by parsing. A parser per language would be
// more precise and is the obvious later step; the line rules are enough to find
// the edges that exist, and their failure mode is a missing token rather than a
// wrong one.
func Extract(filePath string, body []byte) []Token {
	ext := strings.ToLower(path.Ext(filePath))
	if propertiesExt[ext] {
		return propertyKeys(body)
	}
	if !codeExt[ext] && !linkExt[ext] {
		return nil
	}
	var out []Token
	seen := map[string]bool{}
	add := func(kind Kind, value string, line int) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := string(kind) + "\x00" + value
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Token{Kind: kind, Value: value, Line: line})
	}

	lines := strings.Split(string(body), "\n")
	if linkExt[ext] {
		for i, line := range lines {
			for _, v := range linkSites(line) {
				add(KindLink, v, i+1)
			}
		}
	}
	if !codeExt[ext] {
		return out
	}
	if placeholderExt[ext] {
		for i, line := range lines {
			for _, m := range propertyPlaceholder.FindAllStringSubmatch(line, -1) {
				add(KindProperty, m[1], i+1)
			}
		}
	}
	// prefix is the class-level @RequestMapping in force: Spring serves the
	// method path UNDER it, and a client spelling the whole path matches
	// nothing else. Both spellings are recorded — the bare method path is
	// what the flow corpus's clients write.
	prefix := ""
	// pending is the class-level path read off the annotation, promoted to
	// prefix on the top-level class line that follows it. Every top-level
	// class line promotes, so a second controller in the same file starts
	// with no prefix unless it declares one. A nested class, indented, is
	// still inside the controller and leaves the prefix alone.
	pending := ""
	for i, line := range lines {
		lineNo := i + 1
		if isTopLevelClassLine(line) {
			prefix, pending = pending, ""
		}

		// A destination named by the variable it is assigned to. Checked first
		// and independently of the call rules, because the literal and the call
		// that uses it are usually on different lines and often in different
		// files of the same repository.
		for _, m := range destinationVar.FindAllStringSubmatch(line, -1) {
			if isDestinationShape(m[1]) {
				add(KindDestination, m[1], lineNo)
			}
		}

		messaging := messagingCall.MatchString(line)
		routing := routeAnnotation.MatchString(line)
		client := httpClientCall.MatchString(line)
		if !messaging && !routing && !client {
			continue
		}
		classLevel := routing && strings.Contains(line, "RequestMapping") && classFollows(lines, i)
		for _, m := range stringLiteral.FindAllStringSubmatch(line, -1) {
			lit := firstNonEmpty(m[1], m[2], m[3])
			if lit == "" {
				continue
			}
			if m[2] != "" && strings.HasPrefix(lit, "${") {
				// A template literal whose base is configuration:
				// `${this.configuration.basePath}/beruf`. The base says
				// nothing; the constant part after it is the route the
				// server declares, cut at the next interpolation.
				lit = templateRoute(lit)
				if lit == "" {
					continue
				}
			}
			switch {
			case routeShape.MatchString(lit) && !uninteresting[lit]:
				// A route is a route whichever side declared it. Which side
				// this is does not need recording: the edge is the shared
				// value, and both ends want to find each other.
				if classLevel {
					// The class-level path is a route in its own right —
					// the front-end calls "/carts" — AND the prefix every
					// method path below is served under.
					pending = strings.TrimSuffix(lit, "/")
					add(KindRoute, lit, lineNo)
					continue
				}
				if routing || client {
					add(KindRoute, lit, lineNo)
					if routing && prefix != "" {
						add(KindRoute, prefix+lit, lineNo)
					}
				}
			case classLevel && uninteresting[lit] && routeShape.MatchString(lit):
				// "/api" as a class-level prefix is not a token, but it is
				// still the prefix the method paths are served under.
				pending = strings.TrimSuffix(lit, "/")
			case messaging && isDestinationShape(lit):
				add(KindDestination, lit, lineNo)
			}
		}
	}
	return out
}

// classFollows reports whether the annotation on line i sits on a class or
// interface rather than on a method: the next line that is neither blank nor
// another annotation declares one.
func classFollows(lines []string, i int) bool {
	for j := i + 1; j < len(lines) && j <= i+8; j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" || strings.HasPrefix(t, "@") || isCommentLine(t) {
			continue
		}
		return isClassLine(t)
	}
	return false
}

// classDecl is a class or interface declaration: the keyword at a token
// boundary followed by a name, so a comment saying "the class " or a string
// holding the word does not count.
var classDecl = regexp.MustCompile(`(?:^|[\s(])(?:class|interface)\s+[A-Za-z_]`)

// isClassLine reports whether line declares a class or interface.
func isClassLine(line string) bool {
	t := strings.TrimSpace(line)
	return !isCommentLine(t) && classDecl.MatchString(t)
}

// isTopLevelClassLine is isClassLine for a declaration at column zero: the
// controller itself, not a nested class or a code line mentioning one.
func isTopLevelClassLine(line string) bool {
	return len(line) > 0 && line[0] != ' ' && line[0] != '\t' && isClassLine(line)
}

// isCommentLine reports whether a trimmed line is a comment line.
func isCommentLine(t string) bool {
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// templateRoute cuts the constant route out of a template literal that opens
// with an interpolated base: the text between the first "}" and the next
// "${", without a trailing slash. Empty when nothing constant is there.
func templateRoute(lit string) string {
	end := strings.IndexByte(lit, '}')
	if end < 0 {
		return ""
	}
	rest := lit[end+1:]
	if i := strings.Index(rest, "${"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	if len(rest) < 2 || rest[0] != '/' {
		return ""
	}
	return rest
}

// linkSites returns the navigation targets on one line, as the text that
// stood at each site. A stylesheet or base tag carries an href that is not a
// navigation, and is skipped.
func linkSites(line string) []string {
	var out []string
	for _, m := range navigationSite.FindAllStringIndex(line, -1) {
		rest := line[m[1]:]
		// "href ===" and "location.href == x" compare; only "=" assigns.
		if line[m[1]-1] == '=' && strings.HasPrefix(rest, "=") {
			continue
		}
		if stylesheetSite(line, m[0]) {
			continue
		}
		v := linkValue(rest)
		if linkSkip(v) {
			continue
		}
		// Markup or a second statement leaked into the value: not a target.
		if strings.ContainsAny(v, "<>\t") || strings.Contains(v, "  ") {
			continue
		}
		if r := []rune(v); len(r) > linkMaxRunes {
			v = string(r[:linkMaxRunes])
		}
		out = append(out, v)
	}
	return out
}

// linkValue reads the target from the text after a site: a quoted literal
// whole (a template literal included, its interpolation with it — that is
// the variable the census hands to the symbol walk), else the expression up
// to the first comma, closing bracket or semicolon at depth zero. Quotes
// inside the expression are stepped over, so a concatenated '/path' does not
// end it early.
func linkValue(rest string) string {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return ""
	}
	// A JSX attribute wraps its expression in braces: to={"/x"}, to={to},
	// to={{ pathname: "/x" }}. One layer off, then the rules below.
	if rest[0] == '{' {
		inner := strings.TrimSpace(balanced(rest))
		if inner == "" {
			return ""
		}
		if inner[0] == '{' {
			// An object literal: the expression itself, kept whole.
			return inner
		}
		return linkValue(inner)
	}
	if q := rest[0]; q == '"' || q == '\'' || q == '`' {
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			return rest[1 : end+1]
		}
		return ""
	}
	depth := 0
	var quote byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return strings.TrimSpace(rest[:i])
			}
			depth--
		case (c == ',' || c == ';') && depth == 0:
			return strings.TrimSpace(rest[:i])
		}
	}
	return strings.TrimSpace(rest)
}

// balanced returns the text inside the brace that opens s, up to its
// matching close, stepping over quoted strings. Empty when it never closes.
func balanced(s string) string {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[1:i]
			}
		}
	}
	return ""
}

// isDestinationShape keeps prose out of the destination namespace. A queue name
// is an identifier: no spaces, no punctuation beyond the separators queue names
// actually use, and long enough not to be a flag or a single letter.
func isDestinationShape(s string) bool {
	if len(s) < 3 || len(s) > 120 {
		return false
	}
	if strings.ContainsAny(s, " \t/\\{}()<>\"'") {
		return false
	}
	hasLetter := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == ':':
		default:
			return false
		}
	}
	return hasLetter
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// propertyKeys is the configuration side of a property edge: every key of a
// properties file, at the line it is set on. Values are not read — the
// deployed value is what the answer will quote from the chunk, and a
// credential value has been redacted before this runs.
func propertyKeys(body []byte) []Token {
	var out []Token
	seen := map[string]bool{}
	for i, line := range strings.Split(string(body), "\n") {
		m := propertyLine.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, Token{Kind: KindProperty, Value: m[1], Line: i + 1})
	}
	return out
}
