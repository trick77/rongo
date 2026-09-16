package symbols

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realCtags returns the universal-ctags on this machine. It fails rather than
// skips: AGENTS.md requires universal-ctags to be present, and a skipped test
// would let a broken extractor look green on a machine that cannot run it.
func realCtags(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ctags")
	if err != nil {
		t.Fatalf("ctags not found in PATH: %v — install universal-ctags", err)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || !strings.Contains(string(out), "Universal Ctags") {
		t.Fatalf("ctags at %s is not universal-ctags — install it (brew install universal-ctags)", bin)
	}
	return bin
}

// fakeCtags writes a stub executable so the failure paths can be driven without
// a real ctags that would refuse to produce broken output.
func fakeCtags(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ctags")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake ctags: %v", err)
	}
	return path
}

// find returns the symbol with this name, or fails naming what was extracted.
func find(t *testing.T, syms []Symbol, name string) Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			return s
		}
	}
	var got []string
	for _, s := range syms {
		got = append(got, s.Name)
	}
	t.Fatalf("symbol %q not extracted; got %v", name, got)
	return Symbol{}
}

const goFixture = `package fixture

// Greeter greets a person by name.
type Greeter struct {
	Name string
}

// Hello returns a greeting.
func (g *Greeter) Hello() string {
	return "hi " + g.Name
}

func Plain() {}
`

const javaFixture = `package shop.cart;

/** Sends the teaser mail for an abandoned cart. */
public class AbandonedCartJob {

    private final MailSender sender;

    public AbandonedCartJob(MailSender sender) {
        this.sender = sender;
    }

    /** Runs one pass over the abandoned carts. */
    public void run() {
        sender.send();
    }
}
`

func TestExtract_goSource(t *testing.T) {
	// Given
	testee := NewExtractor(realCtags(t))

	// When
	syms, err := testee.Extract(context.Background(), "internal/fixture/greeter.go", []byte(goFixture))

	// Then
	if err != nil {
		t.Fatalf("Extract() err = %v, want nil", err)
	}
	greeter := find(t, syms, "Greeter")
	if greeter.Kind != "struct" || greeter.Line != 4 || greeter.End != 6 {
		t.Errorf("Greeter = %+v, want kind struct, line 4, end 6", greeter)
	}
	hello := find(t, syms, "Hello")
	if hello.Kind != "func" || hello.Line != 9 || hello.Scope != "fixture.Greeter" {
		t.Errorf("Hello = %+v, want kind func, line 9, scope fixture.Greeter", hello)
	}
	plain := find(t, syms, "Plain")
	if plain.Line != 13 {
		t.Errorf("Plain.Line = %d, want 13", plain.Line)
	}
}

func TestExtract_javaSource(t *testing.T) {
	// Given: the extension drives ctags' language inference, so the temp file it
	// writes must keep it — a .java body under a generic name yields nothing.
	testee := NewExtractor(realCtags(t))

	// When
	syms, err := testee.Extract(context.Background(), "src/shop/cart/AbandonedCartJob.java", []byte(javaFixture))

	// Then
	if err != nil {
		t.Fatalf("Extract() err = %v, want nil", err)
	}
	class := find(t, syms, "AbandonedCartJob")
	if class.Kind != "class" || class.Line != 4 {
		t.Errorf("AbandonedCartJob = %+v, want kind class, line 4", class)
	}
	run := find(t, syms, "run")
	if run.Kind != "method" || run.Line != 13 || run.Scope != "AbandonedCartJob" {
		t.Errorf("run = %+v, want kind method, line 13, scope AbandonedCartJob", run)
	}
	// ScopeKind is what makes the chunker's breadcrumb read "class
	// AbandonedCartJob > method run" rather than just naming the scope.
	if run.ScopeKind != "class" {
		t.Errorf("run.ScopeKind = %q, want class", run.ScopeKind)
	}
}

func TestExtract_unknownLanguageIsEmptyAndNoError(t *testing.T) {
	// Given: a language ctags has no parser for.
	testee := NewExtractor(realCtags(t))

	// When
	syms, err := testee.Extract(context.Background(), "docs/notes.zzz", []byte("just some prose\nover two lines\n"))

	// Then: this is the NORMAL path into line-window chunking, not a failure.
	// Reporting it as an error would make every unsupported file look broken.
	if err != nil {
		t.Fatalf("Extract() err = %v, want nil for an unknown language", err)
	}
	if len(syms) != 0 {
		t.Errorf("Extract() = %d symbols, want 0", len(syms))
	}
}

func TestExtract_unparseableOutputIsAnError(t *testing.T) {
	// Given: ctags printing something that is not JSON. Zero symbols and broken
	// output look identical downstream and mean opposite things, so this must
	// never be reported as "this file has no symbols".
	testee := NewExtractor(fakeCtags(t, `printf '{not json\n'`))

	// When
	_, err := testee.Extract(context.Background(), "a.go", []byte("package a\n"))

	// Then
	if err == nil {
		t.Fatal("Extract() err = nil, want an error for unparseable output")
	}
}

func TestExtract_missingLineIsAnError(t *testing.T) {
	// Given: a well-formed JSON tag with no line number. Accepting it would put
	// a citation at line 0 into the index, which is worse than failing loudly.
	testee := NewExtractor(fakeCtags(t, `printf '{"_type": "tag", "name": "Foo", "kind": "func"}\n'`))

	// When
	_, err := testee.Extract(context.Background(), "a.go", []byte("package a\n"))

	// Then
	if err == nil {
		t.Fatal("Extract() err = nil, want an error for a tag without a line")
	}
}

func TestExtract_ctagsFailureIsAnError(t *testing.T) {
	// Given
	testee := NewExtractor(fakeCtags(t, `echo "ctags: broken" >&2; exit 1`))

	// When
	_, err := testee.Extract(context.Background(), "a.go", []byte("package a\n"))

	// Then
	if err == nil {
		t.Fatal("Extract() err = nil, want the ctags failure surfaced")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %q, want it to carry ctags' own message", err)
	}
}

// ctags is handed the bare file name, which it also parses as its own command
// line: a file called "-x.js" reads as an option and the whole extraction
// fails ("Unknown option"), losing the file's symbols to the line-window
// fallback for a reason that has nothing to do with the code.
func TestExtract_aNameStartingWithADashIsAFileNotAnOption(t *testing.T) {
	// Given
	testee := NewExtractor(realCtags(t))

	// When
	syms, err := testee.Extract(context.Background(), "scripts/-x.js",
		[]byte("function parse(s) { return s; }\n"))

	// Then
	if err != nil {
		t.Fatalf("Extract() err = %v, want the dash treated as part of the name", err)
	}
	if len(syms) == 0 {
		t.Errorf("Extract() found nothing in a file whose name begins with a dash")
	}
}

func TestExtract_ignoresPseudoTags(t *testing.T) {
	// Given: ctags emits pseudo-tags as _type "ptag". They describe the run, not
	// the code, and must never reach the symbol index.
	testee := NewExtractor(fakeCtags(t,
		`printf '{"_type": "ptag", "name": "!_TAG_PROGRAM_NAME", "parserName": "Go"}\n'
printf '{"_type": "tag", "name": "Real", "line": 2, "kind": "func"}\n'`))

	// When
	syms, err := testee.Extract(context.Background(), "a.go", []byte("package a\n\nfunc Real() {}\n"))

	// Then
	if err != nil {
		t.Fatalf("Extract() err = %v, want nil", err)
	}
	if len(syms) != 1 || syms[0].Name != "Real" {
		t.Errorf("Extract() = %+v, want only the real tag", syms)
	}
}

// ctags names an anonymous function by hashing the file name it was HANDED,
// so a temporary directory in that name makes the symbol, the enriched text
// and therefore the chunk's embedding different on every index of unchanged
// code. Measured on the flow corpus: two indexes disagreed on 286 chunks, and
// the hits moved with them.
func TestExtract_anonymousNamesAreTheSameOnASecondExtraction(t *testing.T) {
	// Given: a file whose callbacks ctags has to invent names for, one of them
	// nested inside another.
	body := []byte(`hooks.before("/orders > POST", function (transaction, done) {
  request.get(url, function (err, res) {
    done();
  });
});
hooks.after("/orders > GET", function (transaction, done) {
  done();
});
`)
	testee := NewExtractor(realCtags(t))

	// When: the same body is extracted twice, as two index runs would.
	first, err := testee.Extract(context.Background(), "api-spec/hooks.js", body)
	if err != nil {
		t.Fatalf("first Extract() err = %v", err)
	}
	second, err := testee.Extract(context.Background(), "api-spec/hooks.js", body)
	if err != nil {
		t.Fatalf("second Extract() err = %v", err)
	}

	// Then: the whole record, not the name alone. Chunking reads a nested
	// symbol's scope back against its parent's name, so a fix that settled the
	// name and left the scope moving would break nesting instead.
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("Extract() returned %d then %d symbols", len(first), len(second))
	}
	anonymous := 0
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("symbol %d = %+v then %+v; ctags named the same code twice", i, first[i], second[i])
		}
		if strings.Contains(first[i].Name, "anonymous") || strings.Contains(first[i].Name, "__anon") {
			anonymous++
		}
	}
	if anonymous == 0 {
		t.Fatalf("no anonymous symbol in %+v; the fixture stopped exercising the naming", first)
	}

	// And: the invented names still tell the file's callbacks apart.
	seen := map[string]bool{}
	for _, s := range first {
		if seen[s.Name] && (strings.Contains(s.Name, "anonymous") || strings.Contains(s.Name, "__anon")) {
			t.Errorf("anonymous name %q used twice in one file: %+v", s.Name, first)
		}
		seen[s.Name] = true
	}
}

func TestExtract_emptyBodyIsEmptyAndNoError(t *testing.T) {
	// Given
	testee := NewExtractor(realCtags(t))

	// When
	syms, err := testee.Extract(context.Background(), "a.go", nil)

	// Then
	if err != nil {
		t.Fatalf("Extract() err = %v, want nil", err)
	}
	if len(syms) != 0 {
		t.Errorf("Extract() = %d symbols, want 0", len(syms))
	}
}
