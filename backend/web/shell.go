package web

import (
	"bytes"
	"html"
	"net/http"
	"strings"
)

// The four placeholders ui/index.html carries in its og:/twitter: tags. They
// are substituted here rather than written into the file because two of the
// four cannot be known at build time — the deployment's own origin — and one
// of them changes per request.
//
// Substitution is a plain byte replace, not a template: the shell is a built
// artefact this package must not reparse, and every value that goes in is
// escaped first.
const (
	phTitle = "__OG_TITLE__"
	phDesc  = "__OG_DESC__"
	phURL   = "__OG_URL__"
	phImage = "__OG_IMAGE__"
)

// The site-wide card, and what a share link falls back to. Kept short: Slack
// truncates a description at roughly 200 characters and X sooner than that.
const (
	siteTitle = "Rongo"
	siteDesc  = "Ask a codebase in plain language. Answers in domain terms, with the diagram and the places the answer was read from."
	shareDesc = "A shared thread on Rongo. Read-only, frozen where it was shared."
)

// origin is the absolute scheme://host this request arrived at, which every
// crawler needs because none of them resolve a relative og:image.
//
// Read off the request rather than configured: rongo is deployed under whatever
// host its operator picked, and the process itself never learns it — behind a
// TLS-terminating proxy it only ever sees plain HTTP on some internal address,
// which is the same reason share_store hands the SPA a path and never a URL.
// X-Forwarded-* is what the proxy sets; a caller reaching the binary directly
// supplies neither and gets its own Host back.
func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		// A proxy chain sets a comma-separated list; the first entry is the
		// hop nearest the client, which is the one the reader actually used.
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}

// renderShell fills the shell's link-preview placeholders in. title is the
// thread's own for a share link and empty everywhere else.
//
// Every value is HTML-escaped: a thread's title is whatever its owner asked,
// and it goes into an attribute.
func renderShell(shell []byte, r *http.Request, title, desc string) []byte {
	if title == "" {
		title = siteTitle
	}
	if desc == "" {
		desc = siteDesc
	}
	o := origin(r)
	out := bytes.ReplaceAll(shell, []byte(phTitle), []byte(html.EscapeString(title)))
	out = bytes.ReplaceAll(out, []byte(phDesc), []byte(html.EscapeString(desc)))
	out = bytes.ReplaceAll(out, []byte(phURL), []byte(html.EscapeString(o+r.URL.Path)))
	out = bytes.ReplaceAll(out, []byte(phImage), []byte(html.EscapeString(o+"/og.png")))
	return out
}
