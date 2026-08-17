package ask

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/store"
)

func gatherDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO repo_state (name, clone_url, branch) VALUES ('peeq', 'file:///x', 'master')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return db
}

// seedChunk inserts one chunk and returns its id, so a test can hand it to the
// gatherer as if the search had returned it.
func seedChunk(t *testing.T, db *sql.DB, path string, ordinal, start, end int, symbol, body string) int64 {
	t.Helper()
	var fileID int64
	err := db.QueryRow(`SELECT id FROM files WHERE repo='peeq' AND path=?`, path).Scan(&fileID)
	if err == sql.ErrNoRows {
		res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('peeq', ?, 'sha')`, path)
		if err != nil {
			t.Fatalf("seed file: %v", err)
		}
		fileID, _ = res.LastInsertId()
	} else if err != nil {
		t.Fatalf("look up file: %v", err)
	}
	res, err := db.Exec(`
		INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, ordinal, start, end, symbol, "enriched "+body, body, path+string(rune('a'+ordinal)))
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO chunks_fts (rowid, raw_text) VALUES (?, ?)`, id, body); err != nil {
		t.Fatalf("seed fts: %v", err)
	}
	return id
}

func seedSymbol(t *testing.T, db *sql.DB, path, name string, line int) {
	t.Helper()
	var fileID int64
	if err := db.QueryRow(`SELECT id FROM files WHERE repo='peeq' AND path=?`, path).Scan(&fileID); err != nil {
		t.Fatalf("look up file for symbol: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO symbols (file_id, name, kind, line) VALUES (?, ?, 'func', ?)`, fileID, name, line); err != nil {
		t.Fatalf("seed symbol: %v", err)
	}
}

func hitFor(t *testing.T, db *sql.DB, id int64) retrieve.Hit {
	t.Helper()
	var h retrieve.Hit
	err := db.QueryRow(`
		SELECT c.id, f.repo, r.branch, f.path, c.symbol, c.raw_text, c.start_line, c.end_line
		FROM chunks c JOIN files f ON f.id=c.file_id JOIN repo_state r ON r.name=f.repo
		WHERE c.id = ?`, id).Scan(
		&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol, &h.RawText, &h.StartLine, &h.EndLine)
	if err != nil {
		t.Fatalf("load hit: %v", err)
	}
	return h
}

func paths(sources []Source) []string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = s.Path
	}
	return out
}

func has(sources []Source, path string) bool {
	for _, s := range sources {
		if s.Path == path {
			return true
		}
	}
	return false
}

func TestGather_followsASymbolIntoAFileTheSearchNeverReturned(t *testing.T) {
	// The point of gathering. A mechanism runs over a handler, a service and a
	// template; plain top-k returns the handler and the answer needs the rest.
	// If this fixture's second file were also a search hit, the test would pass
	// with the reference walk deleted.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "backend/internal/httpapi/grant.go", 0, 1, 20, "issueGrant",
		"func issueGrant(w http.ResponseWriter) { store.NewGrant(ctx) }")
	seedChunk(t, db, "backend/internal/playbackgrant/store.go", 0, 1, 30, "NewGrant",
		"func NewGrant(ctx context.Context) (Grant, error) { return Grant{}, nil }")
	seedSymbol(t, db, "backend/internal/playbackgrant/store.go", "NewGrant", 1)

	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000})

	got, err := g.Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !has(got, "backend/internal/playbackgrant/store.go") {
		t.Fatalf("sources = %v, want the referenced file pulled in", paths(got))
	}
	for _, s := range got {
		if s.Path == "backend/internal/playbackgrant/store.go" && !strings.Contains(s.Reason, "NewGrant") {
			t.Errorf("reason = %q, want it to name the symbol that led here", s.Reason)
		}
	}
}

func TestGather_doesNotFollowASymbolThatIsOnlyDeclaredNotReferenced(t *testing.T) {
	// The manifest says which repo a symbol belongs to; the reference says
	// whether it counts. Pulling in every file that merely DEFINES a name would
	// drag the whole corpus into the context.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "backend/internal/httpapi/grant.go", 0, 1, 20, "issueGrant",
		"func issueGrant(w http.ResponseWriter) { return }")
	seedChunk(t, db, "backend/internal/unrelated/store.go", 0, 1, 30, "NewGrant",
		"func NewGrant() {}")
	seedSymbol(t, db, "backend/internal/unrelated/store.go", "NewGrant", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if has(got, "backend/internal/unrelated/store.go") {
		t.Errorf("sources = %v, want the unreferenced definition left out", paths(got))
	}
}

func TestGather_respectsTheHopBudget(t *testing.T) {
	// A chain of three files with one hop allowed reaches the second, not the
	// third. Without a cap a single question walks the corpus.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "one", "func one() { two() }")
	seedChunk(t, db, "b.go", 0, 1, 10, "two", "func two() { three() }")
	seedSymbol(t, db, "b.go", "two", 1)
	seedChunk(t, db, "c.go", 0, 1, 10, "three", "func three() {}")
	seedSymbol(t, db, "c.go", "three", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !has(got, "b.go") {
		t.Errorf("sources = %v, want the first hop", paths(got))
	}
	if has(got, "c.go") {
		t.Errorf("sources = %v, want the second hop refused", paths(got))
	}
}

func TestGather_theBudgetNeverEvictsASearchHit(t *testing.T) {
	// Capping context is normal; dropping the chunk the answer was built on is
	// not. A citation pointing at evicted material is a citation rongo cannot
	// stand behind.
	db := gatherDB(t)
	big := strings.Repeat("x ", 4000)
	hitID := seedChunk(t, db, "hit.go", 0, 1, 10, "target", "func target() { helper() }")
	seedChunk(t, db, "helper.go", 0, 1, 10, "helper", "func helper() { "+big+" }")
	seedSymbol(t, db, "helper.go", "helper", 1)

	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 100}).
		Gather(context.Background(), []retrieve.Hit{hitFor(t, db, hitID)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !has(got, "hit.go") {
		t.Fatalf("sources = %v, want the search hit kept whatever the budget", paths(got))
	}
	if has(got, "helper.go") {
		t.Errorf("sources = %v, want the oversized follow-up dropped by the budget", paths(got))
	}
}

func TestGather_returnsNothingForNoHits(t *testing.T) {
	// "No hit means no hit." Gathering must not invent a starting point.
	got, err := NewGatherer(gatherDB(t), GatherOptions{MaxHops: 2, TokenBudget: 1000}).
		Gather(context.Background(), nil)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("sources = %v, want none", paths(got))
	}
}
