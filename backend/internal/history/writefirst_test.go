package history

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/store"
)

// TestSync_writesFirstSoAConcurrentCommitCannotStaleTheSnapshot: in WAL mode
// a transaction that opens with a read holds a snapshot, and its first write
// after another connection committed fails at once with "database is locked"
// — busy_timeout never runs for a stale snapshot. The trace hook tries that
// commit between the transaction's first statement and its second, the one
// interleaving the poller meets when the HTTP side writes mid-sync. Opened
// with a read, the commit lands and Sync's write fails; opened with a write,
// Sync already holds the lock and it is the other writer that is refused.
func TestSync_writesFirstSoAConcurrentCommitCannotStaleTheSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "h.db")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { seed.Close() })
	if err := store.Migrate(seed, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES ('shop', 'file:///shop', 'main')`); err != nil {
		t.Fatal(err)
	}
	if err := New(seed).Sync(ctx, "shop", []gitrepo.Commit{
		{SHA: "c1", CommittedAt: at(2), Subject: "first"},
	}); err != nil {
		t.Fatalf("seed Sync: %v", err)
	}

	dsn := "file:" + url.PathEscape(path) + "?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)"
	// The other writer gives up at once rather than wait out Sync's lock.
	other, err := driver.Open(dsn + "&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open other: %v", err)
	}
	t.Cleanup(func() { other.Close() })

	// The hook sees the statements database/sql prepares, which is every
	// statement Sync runs: each binds the repository.
	var (
		mu        sync.Mutex
		armed     bool
		first     string
		firstRead bool
		fired     bool
		otherErr  error
	)
	trace := func(_ sqlite3.TraceEvent, arg1 any, _ any) error {
		stmt, ok := arg1.(*sqlite3.Stmt)
		mu.Lock()
		defer mu.Unlock()
		if !ok || !armed || fired {
			return nil
		}
		if first == "" {
			first, firstRead = stmt.ExpandedSQL(), stmt.ReadOnly()
			return nil
		}
		fired = true
		_, otherErr = other.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES ('loom', 'file:///loom', 'main')`)
		return nil
	}
	db, err := driver.Open(dsn+"&_pragma=busy_timeout(10000)", func(c *sqlite3.Conn) error {
		return c.Trace(sqlite3.TRACE_STMT, trace)
	})
	if err != nil {
		t.Fatalf("open traced: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}

	// When
	mu.Lock()
	armed = true
	mu.Unlock()
	err = New(db).Sync(ctx, "shop", []gitrepo.Commit{
		{SHA: "c2", CommittedAt: at(3), Subject: "second"},
		{SHA: "c1", CommittedAt: at(2), Subject: "first"},
	})

	// Then
	mu.Lock()
	defer mu.Unlock()
	if !fired {
		t.Fatalf("the concurrent commit never ran; first statement %q", first)
	}
	if firstRead {
		t.Errorf("the transaction opened with a read: %s", first)
	}
	if err != nil {
		t.Fatalf("Sync() err = %v, want the write lock held from the first statement", err)
	}
	if otherErr == nil {
		t.Error("the other writer committed mid-sync; Sync did not hold the write lock")
	}
	if n := countRows(t, seed, `SELECT COUNT(*) FROM commits WHERE repo = 'shop'`); n != 2 {
		t.Errorf("commits = %d, want 2", n)
	}
}

func countRows(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
