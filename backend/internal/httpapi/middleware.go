package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/httpx"
)

// trustedHops is the number of proxies in front of rongo: one Traefik, so the
// X-Forwarded-For entry it wrote is the client. Reached directly, the header
// is the caller's to forge; a forged one buys a fresh throttle turn per value,
// which is the unthrottled form it replaced and no worse.
const trustedHops = 1

// csp is kit's default policy plus what the diagrams need: PlantUML renders
// through graphviz compiled to WebAssembly (@plantuml/core's viz-global.js),
// which 'self' alone does not let instantiate, and an exported diagram is
// handed to the browser as a blob.
const csp = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// chain is the family's middleware order around the router: the access log
// outermost, so a panicked request still produces a line (as a 500); recovery
// inside it; the security headers and the cross-site refusal inside that, so
// the headers reach a 500 too. Both log lines carry maskPath's view of the
// path, never a share token.
func chain(h http.Handler) http.Handler {
	log := slog.Default()
	return httpx.Chain(h,
		httpx.AccessLog(log, httpx.AccessLogOptions{ClientIP: clientIP, Path: maskedPath}),
		httpx.RecoverWith(log, httpx.RecoverOptions{Path: maskedPath}),
		httpx.SecurityHeaders(csp),
		httpx.CrossOrigin,
	)
}

// clientIP is the address the access log and the login throttle see.
func clientIP(r *http.Request) string {
	return httpx.ClientIP(r, trustedHops)
}

func maskedPath(r *http.Request) string {
	return maskPath(r.URL.Path)
}

// sharePrefixes are the routes whose next path segment is a share token:
// the public API and the page a link opens.
var sharePrefixes = []string{"/api/shares/", "/share/"}

// maskPath replaces a share token in path with the literal "{token}", keeping
// what follows it, so a log line still says which route was hit without
// handing whoever reads the log every link that was opened.
func maskPath(path string) string {
	for _, prefix := range sharePrefixes {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok || rest == "" {
			continue
		}
		suffix := ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			suffix = rest[i:]
		}
		return prefix + "{token}" + suffix
	}
	return path
}
