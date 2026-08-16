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
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(on)",
		url.PathEscape(path),
	)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
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
