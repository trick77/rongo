// Package symbols extracts the symbol records rongo indexes alongside the
// source, using universal-ctags.
//
// ctags rather than tree-sitter: tree-sitter needs cgo, a compiled grammar and
// per-language node names, while ctags gives one uniform record across ~150
// languages from a single binary. Where it yields nothing, the chunker's line
// window is the normal path, not a failure path.
package symbols

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Symbol is one ctags record: what it is called, what kind of thing it is,
// what encloses it, and where it lives in the file.
//
// End is the symbol's last line, which ctags supplies with --fields=+e for the
// languages that track scope. It is what lets the chunker tell a CONTAINER from
// a leaf — a Java class encloses its methods and is not itself a unit of
// retrieval, while a Go struct with only fields is — by asking whether another
// symbol starts inside this one's range. It is 0 where ctags reported none, and
// the chunker then falls back to matching scope names.
type Symbol struct {
	Name string
	Kind string
	// Scope is the enclosing symbol's name and ScopeKind what kind of thing it
	// is ("class", "struct", "package"). Both are needed to render a breadcrumb
	// like "class AbandonedCartJob > method run", which is what lets the vector
	// lane match a question asked in business language.
	Scope     string
	ScopeKind string
	Line      int
	End       int
}

// Extractor runs universal-ctags over file bodies.
//
// It assumes the binary really is universal-ctags: exttools.Resolve refuses BSD
// ctags at startup, because that one rejects long options and would yield an
// empty symbol index rather than an error. This package still fails loudly on
// output it cannot parse — see Extract.
type Extractor struct {
	ctags string
}

// NewExtractor builds an Extractor around a ctags binary path.
func NewExtractor(ctagsBin string) *Extractor {
	return &Extractor{ctags: ctagsBin}
}

// tagRecord is one line of ctags' JSON output. Pointer fields distinguish
// "absent" from "zero", which is what makes a missing line detectable.
type tagRecord struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Line      *int   `json:"line"`
	End       int    `json:"end"`
}

// Extract returns the symbols ctags finds in body. path is not read; it only
// supplies the file name ctags infers the language from.
//
// A language ctags has no parser for yields an EMPTY slice and NO error: that
// is the normal path into line-window chunking. Output ctags produced but this
// package cannot parse is an ERROR, because zero symbols and broken output look
// identical downstream and mean opposite things — one is a plain-text file, the
// other is a silently empty index.
//
// A failure here concerns one file. The caller logs it and falls back to line
// windows for that file rather than failing the whole repository index.
func (e *Extractor) Extract(ctx context.Context, path string, body []byte) ([]Symbol, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}

	// ctags infers the language from the file NAME, so the temp file keeps the
	// original base name rather than a generated one: a ".java" body written as
	// "tmp123" parses as nothing, and names without an extension (Makefile,
	// CMakeLists.txt) are recognised by the full name.
	dir, err := os.MkdirTemp("", "rongo-ctags-")
	if err != nil {
		return nil, fmt.Errorf("ctags temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, filepath.Base(path))
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, fmt.Errorf("ctags temp file: %w", err)
	}

	// --fields=+neKzS: line numbers, end lines, long kind names, the kind key
	// and the scope. --sort=no keeps the records in file order, which is what
	// the chunker walks.
	cmd := exec.CommandContext(ctx, e.ctags,
		"--output-format=json", "--fields=+neKzS", "--sort=no", "-f", "-", tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ctags %s: %w: %s", filepath.Base(path), err,
			strings.TrimSpace(stderr.String()))
	}

	var out []Symbol
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec tagRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("ctags %s: unparseable output: %w", filepath.Base(path), err)
		}
		// Pseudo-tags describe the ctags run itself, not the code.
		if rec.Type != "tag" {
			continue
		}
		if rec.Name == "" || rec.Line == nil || *rec.Line <= 0 {
			return nil, fmt.Errorf("ctags %s: tag without a name or line: %s", filepath.Base(path), line)
		}
		out = append(out, Symbol{
			Name:      rec.Name,
			Kind:      rec.Kind,
			Scope:     rec.Scope,
			ScopeKind: rec.ScopeKind,
			Line:      *rec.Line,
			End:       rec.End,
		})
	}
	return out, nil
}
