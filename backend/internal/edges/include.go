package edges

import (
	"context"
	"database/sql"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/sqlutil"
	"github.com/trick77/rongo/internal/symbols"
)

// mtextExt is where the include rule runs, and the only rule M/TEXT gets.
var mtextExt = map[string]bool{".mtxm": true, ".mtxt": true}

// IsMTextPath reports whether Extract reads includes from p: the check the
// gather walk shares with it, so the walk asks Included only where an
// include can have been recorded.
func IsMTextPath(p string) bool {
	return mtextExt[strings.ToLower(path.Ext(p))]
}

// includeLanguages are the language codes a language variable stands for, in
// the order Included prefers them: a template including
// "Form_T1000_*HV_SPRACHE" includes one of these four files at run time, and
// which one is the letter's business.
var includeLanguages = []string{"de", "fr", "it", "en"}

// languageVariable is the system variable holding the letter's language or
// the user interface's. Variable names are not case-stable in real files.
// The name must end there: *HV_SPRACHEKZ is another variable, which
// languageAt checks, RE2 having no lookahead.
var languageVariable = regexp.MustCompile(`(?i)\*HV_(?:GUI)?SPRACHE`)

// languageAt is the span of the first language variable in s that is not
// the start of a longer variable name, or nil.
func languageAt(s string) []int {
	for _, loc := range languageVariable.FindAllStringIndex(s, -1) {
		if loc[1] == len(s) || !isIdentByte(s[loc[1]]) {
			return loc
		}
	}
	return nil
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= 0x80
}

// includeRefs reads the places an M/TEXT file names another one:
//
//   - a line starting with "#", up to the first ";" or "," (its arguments);
//   - an include stored in a .SET value or opened by .NEW: an argument
//     holding "#" and a path. It must name a folder, or ".SET *x,#" — a
//     literal "#" assigned to a variable — would read as an include;
//   - a template included by .INC: a quoted path, or the path up to the
//     options after it ("resolve=true").
//
// A name may hold spaces, and a value may be quoted; quotes are dropped. A
// .DSC line is a comment and names nothing. Each value is the stem of the
// last path segment, case folded as files.stem is (symbols.FoldStem), the key
// Included resolves. One token per value and line, so a walk can follow just
// the includes on the lines it holds.
func includeRefs(body []byte) []Token {
	type at struct {
		value string
		line  int
	}
	var out []Token
	seen := map[at]bool{}
	add := func(raw string, line int, needFolder bool) {
		if needFolder && !strings.ContainsAny(raw, `\/`) {
			return
		}
		v, ok := includeValue(raw)
		key := at{v, line}
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Token{Kind: KindInclude, Value: v, Line: line})
	}
	for i, line := range symbols.MTextLines(body) {
		n := i + 1
		if strings.HasPrefix(line, "#") {
			if args := includeArgs(line[1:]); len(args) > 0 {
				add(args[0], n, false)
			}
			continue
		}
		name, rest, ok := symbols.MTextDirective(line)
		if !ok {
			continue
		}
		switch name {
		case "SET", "NEW":
			for _, arg := range includeArgs(rest) {
				if _, target, ok := strings.Cut(arg, "#"); ok {
					add(unquote(target), n, true)
				}
			}
		case "INC":
			if target := incTarget(rest); target != "" {
				add(target, n, false)
			}
		}
	}
	return out
}

// incTarget is the path an .INC names: the quoted string when the arguments
// open with a quote, else the first word, options after it left off.
func incTarget(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest != "" && (rest[0] == '"' || rest[0] == '\'') {
		if end := strings.IndexByte(rest[1:], rest[0]); end >= 0 {
			return strings.TrimSpace(rest[1 : end+1])
		}
	}
	if f := strings.Fields(rest); len(f) > 0 {
		return f[0]
	}
	return ""
}

// includeArgs splits a directive's arguments at "," and ";" outside quotes,
// each trimmed and unquoted, empty ones dropped. A quote quotes only when it
// opens an argument: the apostrophe in "O'Neil_Form" is part of the name.
func includeArgs(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
	)
	flush := func() {
		if a := unquote(cur.String()); a != "" {
			out = append(out, a)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case (r == '"' || r == '\'') && strings.TrimSpace(cur.String()) == "":
			quote = r
		case r == ',' || r == ';':
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// unquote trims s and drops one pair of surrounding quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// LanguagePlaceholder stands where a language variable stood in an include
// value: "#Blocks\Form_T1000_*HV_SPRACHE" is recorded as "Form_T1000_{lang}",
// one include whose language Included picks. Braces never occur in a stem.
const LanguagePlaceholder = "{lang}"

// includeValue turns an include path into the value recorded for it: the
// last segment without "#" and extension, case folded, with
// LanguagePlaceholder where a language variable stands. A path built from any
// other variable names nothing the index could resolve and yields none.
func includeValue(raw string) (string, bool) {
	segs := strings.FieldsFunc(raw, func(r rune) bool { return r == '\\' || r == '/' })
	if len(segs) == 0 {
		return "", false
	}
	v := symbols.MTextStem(strings.TrimSpace(segs[len(segs)-1]))
	if loc := languageAt(v); loc != nil {
		v = v[:loc[0]] + LanguagePlaceholder + v[loc[1]:]
	}
	if v == "" || strings.ContainsAny(v, "*$%") {
		return "", false
	}
	return symbols.FoldStem(v), true
}

// Included resolves the includes on lines from..to of repo/filePath to the
// head chunks of the files they name, in the order the includes first occur.
//
// A stem is matched by files.stem, folded as the include values are, so case
// does not matter, as it does not to M/TEXT on Windows; the (stem, repo)
// index serves the lookup. No M/TEXT file stores a symbol (indexer
// isDefinition), so the name walks never reach one and this is the only way
// in. The match runs in the M/TEXT files of repo first, and only for an
// include repo has no file of in the libraries among within. Home first, because a template's own
// project is where M/TEXT looks first, and a library's copy of a block the
// project also has is not the one the template prints. Libraries only,
// because a shared block lives in a shared project and another product's copy
// is that product's. within is the turn's ceiling; without one the lookup
// stays at home, never the whole corpus.
//
// A plain include lands every file with its stem: a stem is a file name, not
// a word five unrelated files define, so there is no definer ceiling. An
// include through a language variable prints one language: it lands ONE
// variant, in the one repository chosen for the include as a whole, de before
// fr, it and en. Landing the home de beside a library's fr would mix two
// projects' copies of one block.
//
// One query, chunks included. Enabled repositories only, like every other
// lookup.
func Included(ctx context.Context, db *sql.DB, repo, filePath string, from, to int, within []string) ([]Landing, error) {
	langs := make([]string, len(includeLanguages))
	args := []any{repo, filePath, from, to}
	for i, l := range includeLanguages {
		langs[i] = "(?, " + strconv.Itoa(i) + ")"
		args = append(args, l)
	}
	args = append(args, repo, filePath, repo)
	library := `0`
	if len(within) > 0 {
		library = `(r.library = 1 AND f.repo IN (` + sqlutil.Placeholders(len(within)) + `))`
		args = append(args, sqlutil.Args(within)...)
	}
	args = append(args, repo)
	//nolint:gosec // only fixed SQL structure is interpolated (a ?-placeholder list or a literal table name); every value is a bound ? parameter
	rows, err := db.QueryContext(ctx, `
		WITH wanted AS (
			SELECT t.value AS site, MIN(t.line) AS line
			FROM integration_tokens t
			JOIN files me ON me.id = t.file_id
			WHERE me.repo = ? AND me.path = ? AND t.kind = 'include'
			  AND t.line BETWEEN ? AND ?
			GROUP BY t.value
		),
		langs(code, rank) AS (VALUES `+strings.Join(langs, ", ")+`),
		expanded AS (
			-- A plain include is its own one candidate; a variable one is
			-- one candidate per language, ranked. Values and codes are
			-- folded already.
			SELECT w.site, w.line, l.rank, replace(w.site, '`+LanguagePlaceholder+`', l.code) AS stem
			FROM wanted w
			JOIN langs l ON instr(w.site, '`+LanguagePlaceholder+`') > 0 OR l.rank = 0
		)
		SELECT e.site, c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
		FROM expanded e
		-- stem <> '' lets the partial index on (stem, repo) serve the join.
		JOIN files f ON f.stem = e.stem AND f.stem <> ''
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		-- The head chunk: the one covering line 1, the first of them when
		-- windows overlap, as chunkAt picks.
		JOIN chunks c ON c.file_id = f.id AND c.start_line <= 1 AND c.end_line >= 1
		WHERE NOT (f.repo = ? AND f.path = ?)
		  AND (f.repo = ? OR `+library+`)
		ORDER BY e.line, e.site, f.repo <> ?, f.repo, e.rank, f.path, c.ordinal`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type candidate struct {
		site string
		Landing
	}
	var all []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.site, &c.ChunkID, &c.Repo, &c.Branch, &c.Path, &c.SHA,
			&c.Symbol, &c.StartLine, &c.EndLine, &c.Text); err != nil {
			return nil, err
		}
		c.Stem = symbols.MTextStem(path.Base(c.Path))
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One landing per file: overlapping windows give a file more than one
	// chunk covering line 1, and the first, by ordinal, is the head.
	var out []Landing
	files := map[string]bool{}
	take := func(l Landing) {
		key := l.Repo + "\x00" + l.Path
		if files[key] {
			return
		}
		files[key] = true
		out = append(out, l)
	}
	for i := 0; i < len(all); {
		j := i
		home := false
		for j < len(all) && all[j].site == all[i].site {
			home = home || all[j].Repo == repo
			j++
		}
		group := all[i:j]
		i = j
		if home {
			// Home rows sort first: the group's home part ends at the
			// first row of another repository.
			k := 0
			for k < len(group) && group[k].Repo == repo {
				k++
			}
			group = group[:k]
		}
		if strings.Contains(group[0].site, LanguagePlaceholder) {
			// Ordered home first, then repository, language rank, path
			// and ordinal: the first row is the one variant.
			take(group[0].Landing)
			continue
		}
		for _, c := range group {
			take(c.Landing)
		}
	}
	return out, nil
}
