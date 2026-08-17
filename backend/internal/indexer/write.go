package indexer

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

// Writer stores one file's index entry: its row in files, its symbols, and its
// chunks across the three tables retrieval reads.
//
// Every write here is one transaction, and that is the whole design constraint.
// chunks, chunks_vec and chunks_fts are three views of the same rows bridged by
// rowid == chunks.id, and vec0 can take part in neither a trigger nor an FK
// cascade, so nothing but this code keeps them in step. A half-written file
// leaves the semantic lane and the keyword lane disagreeing about what exists,
// and every later result set is quietly wrong.
type Writer struct {
	db *sql.DB
}

// NewWriter builds a Writer.
func NewWriter(db *sql.DB) *Writer {
	return &Writer{db: db}
}

// ReplaceFile replaces everything stored for one path: its file row, its
// symbols and its chunks. len(vecs) must equal len(chunks).
// size is the file's own byte length. It is passed in rather than derived from
// the chunks, because the chunk windows OVERLAP: summing their text inflates the
// figure by roughly the overlap fraction, and RecordSkipped stores the true
// length — the same column would then mean two different things depending on
// which path wrote it.
func (w *Writer) ReplaceFile(ctx context.Context, repo, path, sha, lang string, size int,
	chunks []Chunk, vecs [][]float32, syms []symbols.Symbol) error {
	if len(chunks) != len(vecs) {
		return fmt.Errorf("index %s/%s: %d chunks but %d vectors", repo, path, len(chunks), len(vecs))
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	fileID, err := upsertFile(ctx, tx, repo, path, sha, lang, size, "")
	if err != nil {
		return err
	}
	if err := clearFileContent(ctx, tx, fileID); err != nil {
		return err
	}
	for i, c := range chunks {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			fileID, c.Ordinal, c.StartLine, c.EndLine, c.Symbol, c.Text, c.RawText, c.TokenCount, c.ContentHash)
		if err != nil {
			return fmt.Errorf("index %s/%s chunk %d: %w", repo, path, c.Ordinal, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		// rowid == chunks.id in both mirrors. Retrieval joins on it, so a drift
		// here does not fail, it answers about the wrong code.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks_vec (rowid, embedding) VALUES (?, ?)`, id, store.VecLiteral(vecs[i])); err != nil {
			return fmt.Errorf("index %s/%s chunk %d vector: %w", repo, path, c.Ordinal, err)
		}
		// The keyword lane indexes SearchText, which differs from RawText only
		// when comments were stripped. RawText stays untouched in `chunks`
		// because that is what a citation quotes.
		search := c.SearchText
		if search == "" {
			search = c.RawText
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks_fts (rowid, raw_text) VALUES (?, ?)`, id, search); err != nil {
			return fmt.Errorf("index %s/%s chunk %d keywords: %w", repo, path, c.Ordinal, err)
		}
	}
	for _, s := range syms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO symbols (file_id, name, kind, line, scope) VALUES (?,?,?,?,?)`,
			fileID, s.Name, s.Kind, s.Line, s.Scope); err != nil {
			return fmt.Errorf("index %s/%s symbol %s: %w", repo, path, s.Name, err)
		}
	}
	return tx.Commit()
}

// RecordSkipped records a file that was deliberately NOT indexed, with the
// reason, and removes anything previously indexed for it.
//
// The row exists so the answer layer can say "that file exists but was not
// indexed" instead of pretending it is absent — the "never invent" invariant
// applied to the index itself. Clearing the old content matters just as much: a
// file that has since grown past the ceiling or gained a secret must stop being
// searchable, not merely stop being updated.
func (w *Writer) RecordSkipped(ctx context.Context, repo, path, sha, lang, reason string, size int) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	fileID, err := upsertFile(ctx, tx, repo, path, sha, lang, size, reason)
	if err != nil {
		return err
	}
	if err := clearFileContent(ctx, tx, fileID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteFile removes a path from the index entirely. A path that was never
// indexed is not an error: a diff legitimately names files that were skipped,
// or added and removed between two polls.
func (w *Writer) DeleteFile(ctx context.Context, repo, path string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var fileID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM files WHERE repo = ? AND path = ?`, repo, path).Scan(&fileID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", repo, path, err)
	}
	// The mirrors go first: foreign_keys is ON, so deleting the files row
	// cascades chunks away, and chunks_vec and chunks_fts are NOT part of that
	// cascade. Letting it fire first would orphan them permanently, and an
	// orphaned vector keeps answering questions about deleted code.
	if err := clearFileContent(ctx, tx, fileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, fileID); err != nil {
		return fmt.Errorf("delete %s/%s: %w", repo, path, err)
	}
	return tx.Commit()
}

// upsertFile inserts or updates the files row and returns its id.
func upsertFile(ctx context.Context, tx *sql.Tx, repo, path, sha, lang string, size int, skipReason string) (int64, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO files (repo, path, sha, lang, size, skip_reason) VALUES (?,?,?,?,?,?)
		ON CONFLICT (repo, path) DO UPDATE SET
			sha = excluded.sha, lang = excluded.lang, size = excluded.size, skip_reason = excluded.skip_reason`,
		repo, path, sha, lang, size, skipReason)
	if err != nil {
		return 0, fmt.Errorf("record %s/%s: %w", repo, path, err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM files WHERE repo = ? AND path = ?`, repo, path).Scan(&id); err != nil {
		return 0, fmt.Errorf("record %s/%s: %w", repo, path, err)
	}
	return id, nil
}

// clearFileContent removes a file's chunks from all three tables and its
// symbols, in the order the mirrors demand: gather the ids, delete the vec0 and
// fts5 rows by rowid, and only then the chunks themselves.
func clearFileContent(ctx context.Context, tx *sql.Tx, fileID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE file_id = ?`, fileID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_vec WHERE rowid = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	return nil
}
