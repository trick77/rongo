package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// embedDimPlaceholder is substituted in every migration before it is applied.
// Only the vec0 table uses it: vec0 needs its dimension in the DDL, and the
// dimension is a configuration choice (1536 for text-embedding-3-small, 3072
// for -large), not a schema constant.
const embedDimPlaceholder = "{{EMBED_DIM}}"

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, each in its own transaction, in lexicographic filename
// order. embedDim is the vector width the vec0 table is built with.
//
// The recorded version is the FULL FILENAME. Never edit a migration that has
// run anywhere real: the runner skips a recorded version, so the edit silently
// never applies. Write the next numbered file instead.
func Migrate(db *sql.DB, embedDim int) error {
	if embedDim <= 0 {
		return fmt.Errorf("migrate: embed dimension must be positive, got %d", embedDim)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var dummy int
		err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, name).Scan(&dummy)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return err
		}

		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sqlText := strings.ReplaceAll(string(body), embedDimPlaceholder, strconv.Itoa(embedDim))
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(sqlText); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
