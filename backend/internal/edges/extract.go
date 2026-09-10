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
var messagingCall = regexp.MustCompile(`(?i)\b(?:` +
	`convertAndSend|convertSendAndReceive|send|sendDefault|` + // Spring AMQP / KafkaTemplate
	`setQueueNames|setQueue|setTopics|setDestinationName|` +
	`RabbitListener|KafkaListener|JmsListener|StreamListener|` +
	`Queue|TopicExchange|DirectExchange|FanoutExchange|NewTopic|` +
	`Subscribe|Publish|QueueDeclare|BasicPublish` +
	`)\b`)

// destinationVar matches a variable or constant whose NAME says the string it
// holds is a destination. This is what catches the corpus's real case: both
// sides write `String queueName = "shipping-task"` and use the constant, so the
// literal never appears inside the call itself.
var destinationVar = regexp.MustCompile(`(?i)\b\w*(?:queue|topic|exchange|channel|routingKey)\w*\s*(?:=|:=|:)\s*"([^"]{2,120})"`)

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

// Extract reads one file and returns the destinations it names.
//
// It works line by line rather than by parsing. A parser per language would be
// more precise and is the obvious later step; the line rules are enough to find
// the edges that exist, and their failure mode is a missing token rather than a
// wrong one.
func Extract(filePath string, body []byte) []Token {
	if !codeExt[strings.ToLower(path.Ext(filePath))] {
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

	for i, line := range strings.Split(string(body), "\n") {
		lineNo := i + 1

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
		for _, m := range stringLiteral.FindAllStringSubmatch(line, -1) {
			lit := firstNonEmpty(m[1], m[2], m[3])
			if lit == "" {
				continue
			}
			switch {
			case routeShape.MatchString(lit) && !uninteresting[lit]:
				// A route is a route whichever side declared it. Which side
				// this is does not need recording: the edge is the shared
				// value, and both ends want to find each other.
				if routing || client {
					add(KindRoute, lit, lineNo)
				}
			case messaging && isDestinationShape(lit):
				add(KindDestination, lit, lineNo)
			}
		}
	}
	return out
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
