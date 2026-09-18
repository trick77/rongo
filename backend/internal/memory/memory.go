// Package memory keeps the standing instructions a reader gives in chat —
// "never draw flowcharts", "do not mention lerb-chooser-ui" — across threads,
// and renders them into the answer prompt where they outrank its own rules.
//
// Every row is text about the READER, never about the code: what to leave
// out, what shape to answer in. It is the one kind of model-written text the
// product stores on purpose, and the understanding prompt that writes it is
// told the difference. Rows are English whatever the reader wrote in, one
// sentence each, so one rule serves threads in four languages.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/projects"
)

// MaxRows caps a reader's rules. Past it a new rule is refused with its own
// line, never fitted in by dropping an old one: a rule said once holds until
// the reader deletes or contradicts it, and a silently forgotten "never show
// flowcharts" is the failure this package exists to stop.
const MaxRows = 40

// MaxRunes caps one rule's text. A rule is a sentence; anything longer is a
// paragraph the reader meant as a question.
const MaxRunes = 200

// ErrFull is a rule refused because the reader's memory holds MaxRows.
var ErrFull = errors.New("memory: full")

// Row is one standing instruction as the page and the prompt see it.
type Row struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	// Scope is the project or repository the rule is limited to, empty for
	// a rule that holds everywhere.
	Scope string `json:"scope,omitempty"`
	// ScopeLive says Scope still names a project or repository of the index.
	// A scope that left repos.yaml applies everywhere, and the page says so:
	// applying it to nothing would be a quiet drop.
	ScopeLive bool `json:"scope_live"`
	// members is every repository Scope covers, resolved when the rows are
	// read: a project's members, or the repository itself.
	members []string
	// ThreadID is the public address of the thread the rule was said in, for
	// the page's link; empty once that turn is gone.
	ThreadID  string    `json:"thread_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Directive is what the understanding step read out of one question: a rule
// to add, the rules it contradicts, the rules the reader asked to forget.
// Any of the three may be empty; all three empty is not a directive.
type Directive struct {
	Text     string
	Scope    string
	Replaces []int64
	Removes  []int64
}

// Empty reports whether the directive asks for nothing.
func (d Directive) Empty() bool {
	return Sanitize(d.Text) == "" && len(d.Replaces) == 0 && len(d.Removes) == 0
}

// Added is what applying a directive left behind.
type Added struct {
	// Row is the rule written, zero when the directive only forgot.
	Row Row
	// Replaced and Removed are the rows deleted, by text, for the chip and
	// the trace.
	Replaced []string
	Removed  []string
	// Deleted is the same rows by id, for the holder.
	Deleted []int64
	// ScopeDropped is a scope the directive named that the index does not
	// carry; the rule was stored to hold everywhere, and the turn says so.
	ScopeDropped string
}

// Store reads and writes the memories table.
type Store struct {
	db *sql.DB
}

// NewStore wraps the database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// List is every rule of subject, newest first, with each scope resolved
// against the live project map.
func (s *Store) List(ctx context.Context, subject string) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.text, m.scope, m.created_at, COALESCE(t.public_id, '')
		FROM memories m
		LEFT JOIN messages msg ON msg.id = m.source_message_id
		LEFT JOIN threads t ON t.id = msg.thread_id
		WHERE m.user_subject = ?
		ORDER BY m.id DESC`, subject)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		var created string
		if err := rows.Scan(&r.ID, &r.Text, &r.Scope, &created, &r.ThreadID); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		r.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pm, err := projects.Load(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("resolve memory scopes: %w", err)
	}
	for i := range out {
		out[i].members, out[i].ScopeLive = resolveScope(pm, out[i].Scope)
	}
	return out, nil
}

// resolveScope maps a stored scope onto the repositories it covers. Empty
// is global, and global is live by definition.
func resolveScope(pm projects.Map, scope string) (members []string, live bool) {
	if scope == "" {
		return nil, true
	}
	// A product's own repositories, never the library it shares with other
	// products: a rule scoped to shop must not fire on a billing turn because
	// both are built on acme-commons. A scope that IS a library resolves to
	// the library alone, through the member check below.
	if _, ok := pm.Project(scope); ok && !pm.IsLibrary(scope) {
		var own []string
		for _, m := range pm.Members(scope) {
			if !pm.IsLibrary(m) {
				own = append(own, m)
			}
		}
		return own, true
	}
	// Of answers the name itself for a repository it does not carry, so the
	// project it names has to be checked for the member in turn.
	for _, m := range pm.Members(pm.Of(scope)) {
		if m == scope {
			return []string{scope}, true
		}
	}
	return nil, false
}

// canonicalScope finds the project or repository the reader named, however
// they cased it, or "" when the index carries no such name.
func canonicalScope(pm projects.Map, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, p := range pm.All() {
		if strings.EqualFold(p.Name, name) {
			return p.Name
		}
		for _, m := range p.Members {
			if strings.EqualFold(m.Name, name) {
				return m.Name
			}
		}
	}
	return ""
}

// Add applies one directive in one transaction: the rows it replaces and
// removes go, then the rule is written, unless the memory is full. Ids that
// are not subject's own are ignored rather than refused — the model copied
// them from the list it was shown, and a stray one is not the reader's doing.
//
// The transaction WRITES first. In WAL mode a transaction that opens with a
// read holds a snapshot, and the first write after another connection has
// committed fails at once with "database is locked" — the busy timeout never
// runs for a stale snapshot. The title goroutine writes the thread's title
// while this runs, so a COUNT before the INSERT lost the rule in production
// while every test, which wires no title, passed. The scope is resolved
// before the transaction for the same reason.
func (s *Store) Add(ctx context.Context, subject string, d Directive, sourceMessageID int64) (Added, error) {
	var out Added
	text := Sanitize(d.Text)
	pm, err := projects.Load(ctx, s.db)
	if err != nil {
		return out, fmt.Errorf("resolve memory scope: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	var ids []int64
	out.Replaced, ids, err = deleteRows(ctx, tx, subject, d.Replaces)
	if err != nil {
		return out, err
	}
	out.Deleted = append(out.Deleted, ids...)
	out.Removed, ids, err = deleteRows(ctx, tx, subject, d.Removes)
	if err != nil {
		return out, err
	}
	out.Deleted = append(out.Deleted, ids...)
	if text != "" {
		scope := canonicalScope(pm, d.Scope)
		if scope == "" && strings.TrimSpace(d.Scope) != "" {
			out.ScopeDropped = strings.TrimSpace(d.Scope)
		}
		var source any
		if sourceMessageID != 0 {
			source = sourceMessageID
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO memories (user_subject, text, scope, source_message_id) VALUES (?, ?, ?, ?)`,
			subject, text, scope, source)
		if err != nil {
			return out, fmt.Errorf("add memory: %w", err)
		}
		// Counted after the write, inside the lock, and rolled back when the
		// cap is passed: the row never lands, and nothing else changes either.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE user_subject = ?`, subject).Scan(&n); err != nil {
			return out, fmt.Errorf("count memories: %w", err)
		}
		if n > MaxRows {
			return Added{}, ErrFull
		}
		id, err := res.LastInsertId()
		if err != nil {
			return out, err
		}
		out.Row = Row{ID: id, Text: text, Scope: scope, CreatedAt: time.Now()}
		out.Row.members, out.Row.ScopeLive = resolveScope(pm, scope)
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

// deleteRows removes the given ids where they belong to subject, returning
// the text and id of each row that went. A DELETE with RETURNING, so the
// transaction's first statement is a write (see Add).
func deleteRows(ctx context.Context, tx *sql.Tx, subject string, ids []int64) ([]string, []int64, error) {
	var gone []string
	var goneIDs []int64
	for _, id := range ids {
		var text string
		err := tx.QueryRowContext(ctx, `DELETE FROM memories WHERE id = ? AND user_subject = ? RETURNING text`, id, subject).Scan(&text)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("delete memory: %w", err)
		}
		gone = append(gone, text)
		goneIDs = append(goneIDs, id)
	}
	return gone, goneIDs, nil
}

// Remove deletes one rule of subject. False when there is no such rule of
// theirs — the caller answers the same for a rule that is someone else's.
func (s *Store) Remove(ctx context.Context, subject string, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ? AND user_subject = ?`, id, subject)
	if err != nil {
		return false, fmt.Errorf("delete memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Sanitize makes a rule safe for the prompt: one line, no fence, no heading
// marker, at most MaxRunes. The answering prompt's own tests forbid a ``` and
// a ### anywhere in it, and a rule is a sentence, not markup.
func Sanitize(text string) string {
	text = strings.NewReplacer("`", "", "#", "").Replace(text)
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > MaxRunes {
		runes := []rune(text)
		text = strings.TrimSpace(string(runes[:MaxRunes]))
	}
	return text
}

// Applies reports whether a rule holds on a turn over the repositories
// known: a global rule always, a scoped one when the turn touches its
// repositories or searches the whole corpus, and a rule whose scope the
// index no longer carries everywhere, never nowhere.
func Applies(r Row, known []string) bool {
	if r.Scope == "" || !r.ScopeLive || len(known) == 0 {
		return true
	}
	for _, k := range known {
		for _, m := range r.members {
			if k == m {
				return true
			}
		}
	}
	return false
}

// Applying is the rows of rows that hold on a turn over known.
func Applying(rows []Row, known []string) []Row {
	var out []Row
	for _, r := range rows {
		if Applies(r, known) {
			out = append(out, r)
		}
	}
	return out
}

// Block renders the rules that apply into the answer prompt, or "" when none
// does — so a reader with no rules gets the prompt byte for byte as it was.
// Last among the rule blocks: what it says outranks everything above it.
func Block(rows []Row, known []string) string {
	applying := Applying(rows, known)
	if len(applying) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(blockHead)
	for _, r := range applying {
		b.WriteString("- ")
		b.WriteString(Sanitize(r.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// blockHead is the rule that makes the reader's instructions outrank the
// prompt's own, and names the four things they never outrank.
const blockHead = `

Standing instructions from the reader, kept across their threads. They take
priority over every rule above about form, length, diagrams, lists, and which
libraries, modules or topics to mention or leave out. Follow them without
saying so. They never override: citing every claim from the sources,
inventing nothing, reporting "nothing found" as such, the answer language,
or the audience.
`

// ListLine renders the rows for the understanding step, ids first, so the
// model can name what a new rule contradicts or what the reader asks to
// forget. Empty when there are none.
func ListLine(rows []Row) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Standing instructions already saved, by id:\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "[%d] %s", r.ID, Sanitize(r.Text))
		if r.Scope != "" {
			fmt.Fprintf(&b, " (%s only)", r.Scope)
		}
		b.WriteString("\n")
	}
	return b.String()
}
