// Package store opens the single SQLite database (pure-Go ncruces driver with
// the sqlite-vec WASM build linked in) and applies the embedded migrations.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	// The sqlite-vec WASM build for ncruces. It provides the SQLite WASM
	// binary AND the vec0 virtual table plus vec_* functions, and replaces
	// ncruces/go-sqlite3/embed. These two modules are one unit: never bump one
	// without the other. See AGENTS.md.
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	// registers the "sqlite3" database/sql driver.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Open opens (creating if needed) the database at path with the pragmas rongo
// relies on. Callers run Migrate separately.
//
// Every writing transaction begins IMMEDIATE. In WAL a deferred transaction
// that reads before its first write holds a snapshot, and once a connection is
// in a read transaction SQLite no longer runs the busy handler for the write
// lock: another connection's commit in between fails the write at once with
// "database is locked". Writing first is not enough to avoid that read, because
// a virtual table (fts5, vec0) connects on a connection's first use by reading
// its shadow tables at prepare time. Taking the write lock at BEGIN puts every
// read of the transaction behind it, and the other writer is the one that
// waits. Read-only transactions pass ReadOnly and keep the plain BEGIN.
//
// busy_timeout comes first so the pragmas after it already wait on a locked
// database instead of failing a connection open under load.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=foreign_keys(on)",
		url.PathEscape(path),
	)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// BuiltDim reports the dimension chunks_vec was actually created with, so a
// boot can refuse a database built for another embedding model instead of
// failing later on every insert.
//
// pragma_table_info reports an empty type for vec0 virtual-table columns on
// this sqlite-vec build (v0.1.7-alpha.2), so the dimension cannot come from
// there. It IS in the table's original DDL, which SQLite keeps verbatim in
// sqlite_master.sql — parse the bracketed dimension out of that.
func BuiltDim(db *sql.DB) (int, error) {
	var ddl string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chunks_vec'`).Scan(&ddl)
	if err != nil {
		return 0, fmt.Errorf("read chunks_vec schema: %w", err)
	}
	open := strings.IndexByte(ddl, '[')
	closing := strings.IndexByte(ddl, ']')
	if open < 0 || closing <= open {
		return 0, fmt.Errorf("could not determine the chunks_vec dimension from its schema %q", ddl)
	}
	return strconv.Atoi(ddl[open+1 : closing])
}

// VecLiteral encodes a float32 vector as the JSON-array text sqlite-vec
// accepts as a bind parameter.
func VecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
