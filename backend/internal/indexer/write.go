package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

// dataLanguages are the file types where ctags reports the CONTENT as symbols:
// every key, every value and every array index. None of it defines a name, and
// a fixture that lists sixty-five questions ends up defining "candidates"
// sixty-five times — which the reference walk then follows, spending a token
// budget it stops on rather than trims, so every name after them goes
// unfollowed.
var dataLanguages = map[string]bool{"json": true, "yaml": true, "xml": true, "properties": true}

// nonDefinitionKinds are the ctags kinds to skip, per language.
//
// A denylist rather than a list of kinds worth keeping, because a ctags kind
// name means different things in different parsers, and a global set of
// "definition kinds" is therefore not expressible:
//
//	member    a struct field in Go, a METHOD in Python
//	constant  a const value in Go, an arrow function in TypeScript
//
// Getting that wrong does not fail, it silently removes every method of a
// language from the walk. So a language nobody has looked at keeps every kind
// and behaves exactly as it did before, and each entry below is a parser whose
// output was read.
//
// Note what is NOT here: Go const and var. They name a real definition, and
// following one reaches the prompt text or the table it holds. Only the kinds
// that define nothing followable are listed.
var nonDefinitionKinds = map[string]map[string]bool{
	// A struct field named `named` is not a definition of anything, but the
	// walk resolved it and cited the enclosing struct for behaviour the struct
	// does not perform. `package` matches every file that declares it.
	"go": {"member": true, "package": true},
	// A column and an index are parts of a table, and the table is already
	// followable under its own kind.
	"sql": {"field": true, "index": true},
}

// isDefinition reports whether a ctags record names something the reference
// walk should be able to reach.
func isDefinition(lang, kind string) bool {
	if dataLanguages[lang] {
		return false
	}
	return !nonDefinitionKinds[lang][kind]
}

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
	chunks []Chunk, vecs [][]float32, syms []symbols.Symbol, toks []edges.Token) error {
	if len(chunks) != len(vecs) {
		return fmt.Errorf("index %s/%s: %d chunks but %d vectors", repo, path, len(chunks), len(vecs))
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

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
		//
		// No fallback to RawText: it could not tell "the producer never set
		// SearchText" from "empty because stripping removed everything", and
		// under the second reading it would put the comments straight back into
		// the lane they were just removed from — silently, on exactly the
		// chunks that are pure prose. ChunkFile drops those windows instead, so
		// an empty SearchText here is a programming error and says so.
		if c.SearchText == "" && c.RawText != "" {
			return fmt.Errorf("index %s/%s chunk %d: SearchText is empty while RawText is not; "+
				"the keyword lane would silently index the unstripped source", repo, path, c.Ordinal)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks_fts (rowid, raw_text) VALUES (?, ?)`, id, c.SearchText); err != nil {
			return fmt.Errorf("index %s/%s chunk %d keywords: %w", repo, path, c.Ordinal, err)
		}
	}
	// Only definitions are recorded. The reference walk in internal/ask reads
	// this table as "where is this name DEFINED", and storing every ctags
	// record made that false. See isDefinition.
	for _, s := range syms {
		if !isDefinition(lang, s.Kind) {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO symbols (file_id, name, kind, line, scope) VALUES (?,?,?,?,?)`,
			fileID, s.Name, s.Kind, s.Line, s.Scope); err != nil {
			return fmt.Errorf("index %s/%s symbol %s: %w", repo, path, s.Name, err)
		}
	}
	// Integration tokens ride along with the file for the same reason symbols
	// do: they are derived from its content, so they must be replaced with it
	// or an edge outlives the line that declared it.
	for _, tok := range toks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO integration_tokens (file_id, kind, value, line) VALUES (?,?,?,?)`,
			fileID, string(tok.Kind), tok.Value, tok.Line); err != nil {
			return fmt.Errorf("index %s/%s token %s: %w", repo, path, tok.Value, err)
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
	defer func() { _ = tx.Rollback() }()
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
	defer func() { _ = tx.Rollback() }()

	// The transaction WRITES first. In WAL mode a transaction that opens with
	// a read holds a snapshot, and its first write after another connection
	// has committed fails at once with "database is locked" — the busy timeout
	// never runs for a stale snapshot. The HTTP side writes titles, usage and
	// steps all the time, so the old "SELECT id, then delete" shape failed
	// whole incremental runs on a race every test missed.
	//
	// The mirrors go first: foreign_keys is ON, so deleting the files row
	// cascades chunks away, and chunks_vec and chunks_fts are NOT part of that
	// cascade. Letting it fire first would orphan them permanently, and an
	// orphaned vector keeps answering questions about deleted code.
	const owned = `SELECT id FROM chunks WHERE file_id IN (SELECT id FROM files WHERE repo = ?1 AND path = ?2)`
	for _, q := range []string{
		`DELETE FROM chunks_vec WHERE rowid IN (` + owned + `)`,
		`DELETE FROM chunks_fts WHERE rowid IN (` + owned + `)`,
		`DELETE FROM chunks WHERE id IN (` + owned + `)`,
		`DELETE FROM symbols WHERE file_id IN (SELECT id FROM files WHERE repo = ?1 AND path = ?2)`,
		`DELETE FROM integration_tokens WHERE file_id IN (SELECT id FROM files WHERE repo = ?1 AND path = ?2)`,
	} {
		if _, err := tx.ExecContext(ctx, q, repo, path); err != nil {
			return fmt.Errorf("delete %s/%s: %w", repo, path, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE repo = ?1 AND path = ?2`, repo, path); err != nil {
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
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM integration_tokens WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	return nil
}

// PruneEmbedCache deletes every cached vector no chunk uses any more: the old
// text of an edited file, a deleted file, a purged repository, whatever a
// reset's re-index no longer produced. Matched on content hash alone, whatever
// the model: embed.Model is a constant of the build, a different one is a new
// database. The cache is keyed on content, not on a
// repository, so a vector another repository's chunk still carries stays.
// It reports how many rows went and how many are left.
//
// It runs after a run completes, so a reset's re-index still finds unchanged
// content in the cache. A purged repository re-embeds if it returns, which is
// accepted: a repository rongo was told to forget must not live on as vectors.
//
// The eval harness's question vectors, keyed "query:<sha>", are no chunk's and
// stay: pruning them would re-embed every question after an index run, and
// the endpoint's drift would then move measurements that compare within one
// database. The product itself never caches a query.
func PruneEmbedCache(ctx context.Context, db *sql.DB) (removed, kept int64, err error) {
	res, err := db.ExecContext(ctx, `
		DELETE FROM embed_cache
		WHERE content_hash NOT LIKE 'query:%'
		  AND NOT EXISTS (SELECT 1 FROM chunks c WHERE c.content_hash = embed_cache.content_hash)`)
	if err != nil {
		return 0, 0, fmt.Errorf("prune embedding cache: %w", err)
	}
	// SQLite always reports the rows a DELETE touched.
	removed, _ = res.RowsAffected()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embed_cache`).Scan(&kept); err != nil {
		return 0, 0, fmt.Errorf("count embedding cache: %w", err)
	}
	return removed, kept, nil
}

// PruneEmbedCacheAndLog prunes and says so: rows removed and kept when any
// went, a warning when the prune failed, nothing on a run that orphaned
// nothing. args name the occasion (the repository, the purge). A failure is
// never the caller's: the index is complete, a stale vector only costs space.
func PruneEmbedCacheAndLog(ctx context.Context, db *sql.DB, log *slog.Logger, args ...any) {
	removed, kept, err := PruneEmbedCache(ctx, db)
	if err != nil {
		log.Warn("pruning the embedding cache failed", append(args, "err", err)...)
		return
	}
	if removed > 0 {
		log.Info("embedding cache pruned", append(args, "removed", removed, "kept", kept)...)
	}
}
