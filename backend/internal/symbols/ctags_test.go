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
