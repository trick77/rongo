package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// VecCompaction is what one CompactVectors call found and did. Chunks are
// vec0's storage chunks (chunks_vec_chunks rows), not rongo's chunks.
type VecCompaction struct {
	Compacted    bool
	Rows         int64
	ChunksBefore int64
	ChunksAfter  int64
}

// CompactVectors rebuilds chunks_vec when deleted vectors have left it bloated.
//
// vec0 never gives space back. A delete clears a validity bit, the storage chunk
// stays, and an insert only appends to the newest chunk, so every re-embed —
// incremental run, full re-index, replaced snapshot — adds chunks nothing
// reclaims. A KNN query reads every chunk's whole vector blob whether or not a
// slot in it is live: a production database holding 46k vectors in 380 chunks
// (46 needed) spent minutes per search reading 2.3 GB, and turns died on the
// reader's timeout before the search returned.
//
// Bloated means more than twice the chunks the live rows need. The rebuild
// keeps every rowid and every vector byte for byte, so nothing is re-embedded
// and retrieval's rowid join is unaffected. One transaction: a failure leaves
// the table as it was.
//
// Boot only. The transaction holds the write lock for the whole copy — 9.6 s
// for 14k vectors on a laptop — which is past the 10 s busy_timeout at
// production size, so run beside live turns it fails their writes with
// "database is locked". An index run warns instead (WarnVectorBloat).
func CompactVectors(ctx context.Context, db *sql.DB) (VecCompaction, error) {
	return compactVectors(ctx, db, nil)
}

// compactBatch is how many vectors one copy statement moves. Progress is
// counted in these, so a rewrite of minutes reports rows as it goes. A var so
// a test can walk many batches over a small table.
var compactBatch = 2000

// compactHeartbeat is how often a running compaction or vacuum says it is
// still running. A var so a test need not wait for it.
var compactHeartbeat = 15 * time.Second

// compactProgress is what a running compaction has got to, read by the
// heartbeat. A nil one records nothing.
type compactProgress struct {
	log   *slog.Logger
	args  []any
	mu    sync.Mutex
	step  string
	done  int64
	total int64
}

func (p *compactProgress) begin(step string, total int64, attrs ...any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.step, p.done, p.total = step, 0, total
	p.mu.Unlock()
	p.log.Info(step, append(slices.Clone(p.args), attrs...)...)
}

func (p *compactProgress) add(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.done += n
	p.mu.Unlock()
}

func (p *compactProgress) state() (step string, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.step, p.done, p.total
}

// heartbeat logs msg every compactHeartbeat until the returned stop is
// called, with whatever attrs returns at that moment and the time elapsed.
func heartbeat(log *slog.Logger, msg string, attrs func() []any) (stop func()) {
	start := time.Now()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(compactHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				log.Info(msg, append(attrs(), "elapsed", took(start))...)
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

func compactVectors(ctx context.Context, db *sql.DB, p *compactProgress) (VecCompaction, error) {
	c, needed, err := measureVectors(ctx, db)
	if err != nil || c.ChunksBefore <= 2*needed {
		c.ChunksAfter = c.ChunksBefore
		return c, err
	}

	var ddl string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chunks_vec'`).Scan(&ddl); err != nil {
		return c, fmt.Errorf("read chunks_vec schema: %w", err)
	}
	p.begin("compacting the vector index", c.Rows,
		"rows", c.Rows, "chunks", c.ChunksBefore, "chunks_needed", needed)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback() }()
	exec := func(q string, args ...any) (int64, error) {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, fmt.Errorf("compact chunks_vec: %w", err)
		}
		return res.RowsAffected()
	}
	// Opens with a write to main, for the reason Writer.DeleteFile gives: a
	// transaction that reads first holds a snapshot, and its first write
	// after another connection committed fails at once as "database is
	// locked". The copy table is in main for that reason, not in temp.
	if _, err := exec(`CREATE TABLE vec_compact_keep (id INTEGER PRIMARY KEY, embedding BLOB NOT NULL)`); err != nil {
		return c, err
	}
	// Both copies go in rowid batches, walked on chunks_vec_rowids (a plain
	// table, so the range is an index seek), each vector read by rowid = ?.
	// Never rowid IN (…): outside a KNN query vec0 answers that with a full
	// scan, so every batch would walk the whole table.
	p.begin("copying vectors out", c.Rows)
	if err := copyBatches(ctx, tx, p, exec, `INSERT INTO vec_compact_keep (id, embedding)
		SELECT r.rowid, (SELECT v.embedding FROM chunks_vec v WHERE v.rowid = r.rowid)
		FROM chunks_vec_rowids r WHERE r.rowid > ? ORDER BY r.rowid LIMIT ?`,
		`SELECT MAX(id) FROM vec_compact_keep`); err != nil {
		return c, err
	}
	if _, err := exec(`DROP TABLE chunks_vec`); err != nil {
		return c, err
	}
	if _, err := exec(ddl); err != nil {
		return c, err
	}
	p.begin("writing vectors back", c.Rows)
	if err := copyBatches(ctx, tx, p, exec, `INSERT INTO chunks_vec (rowid, embedding)
		SELECT id, embedding FROM vec_compact_keep WHERE id > ? ORDER BY id LIMIT ?`,
		`SELECT MAX(rowid) FROM chunks_vec_rowids`); err != nil {
		return c, err
	}
	if _, err := exec(`DROP TABLE vec_compact_keep`); err != nil {
		return c, err
	}
	var rows int64
	if err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM chunks_vec_rowids), (SELECT COUNT(*) FROM chunks_vec_chunks)`).
		Scan(&rows, &c.ChunksAfter); err != nil {
		return c, fmt.Errorf("count compacted chunks_vec: %w", err)
	}
	if rows != c.Rows {
		return c, fmt.Errorf("compact chunks_vec: %d rows after, %d before", rows, c.Rows)
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	c.Compacted = true
	return c, nil
}

// copyBatches runs insert, which copies the next compactBatch rows after a
// rowid, until it copies none; last reads the highest rowid copied so far.
func copyBatches(ctx context.Context, tx *sql.Tx, p *compactProgress,
	exec func(string, ...any) (int64, error), insert, last string) error {
	var after int64
	for {
		n, err := exec(insert, after, compactBatch)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		p.add(n)
		var hi sql.NullInt64
		if err := tx.QueryRowContext(ctx, last).Scan(&hi); err != nil {
			return fmt.Errorf("compact chunks_vec: %w", err)
		}
		after = hi.Int64
	}
}

// measureVectors counts chunks_vec's live rows and storage chunks, and the
// chunks those rows need. An empty table needs none and has none.
func measureVectors(ctx context.Context, db *sql.DB) (c VecCompaction, needed int64, err error) {
	var size sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM chunks_vec_rowids),
		       (SELECT COUNT(*) FROM chunks_vec_chunks),
		       (SELECT length(validity) * 8 FROM chunks_vec_chunks LIMIT 1)`).
		Scan(&c.Rows, &c.ChunksBefore, &size); err != nil {
		return c, 0, fmt.Errorf("measure chunks_vec: %w", err)
	}
	if !size.Valid || size.Int64 <= 0 {
		return c, 0, nil
	}
	return c, max((c.Rows+size.Int64-1)/size.Int64, 1), nil
}

// WarnVectorBloat is what an index run does instead of compacting: it says
// the table is bloated, and that the next boot compacts it. Quiet when healthy.
func WarnVectorBloat(ctx context.Context, db *sql.DB, log *slog.Logger, args ...any) {
	c, needed, err := measureVectors(ctx, db)
	if err != nil {
		log.Warn("measuring the vector index failed", append(args, "err", err)...)
		return
	}
	if c.ChunksBefore > 2*needed {
		log.Warn("vector index bloated, compacted at next boot", append(args, "rows", c.Rows,
			"chunks", c.ChunksBefore, "chunks_needed", needed)...)
	}
}

// CompactVectorsAndLog compacts and says so: a line before the rewrite and one
// after, a warning when it failed, nothing when the table was healthy. The
// rewrite takes minutes on a production table, so it announces itself rather
// than leave the boot silent until it is done. A failure is never the
// caller's: the index is complete, bloat only costs time. It reports what it
// found; ok is false when the check itself failed.
func CompactVectorsAndLog(ctx context.Context, db *sql.DB, log *slog.Logger, args ...any) (c VecCompaction, ok bool) {
	start := time.Now()
	p := &compactProgress{log: log, args: args}
	stop := heartbeat(log, "compacting the vector index, still running", func() []any {
		step, done, total := p.state()
		return append(slices.Clone(args), "step", step, "done", done, "total", total)
	})
	c, err := compactVectors(ctx, db, p)
	stop()
	if err != nil {
		log.Warn("compacting the vector index failed", append(args, "err", err)...)
		return c, false
	}
	if c.Compacted {
		log.Info("vector index compacted", append(args, "rows", c.Rows,
			"chunks_before", c.ChunksBefore, "chunks_after", c.ChunksAfter, "took", took(start))...)
	}
	return c, true
}

// LogVectorIndexAtBoot compacts when bloated, vacuums after a compaction, and
// otherwise states the table's shape: rows against storage chunks is the one
// number that shows vec0 bloat before searches slow down.
func LogVectorIndexAtBoot(ctx context.Context, db *sql.DB, log *slog.Logger) {
	c, ok := CompactVectorsAndLog(ctx, db, log, "reason", "boot")
	switch {
	case !ok:
	case c.Compacted:
		VacuumAndLog(ctx, db, log)
	default:
		log.Info("vector index", "rows", c.Rows, "chunks", c.ChunksBefore)
	}
}

// VacuumAndLog rewrites the database file so the pages a compaction freed go
// back to the disk, and logs the size either side. Compaction alone fixes the
// search time; freed pages are reused, but the file keeps its size until this.
// It rewrites the whole file and blocks writers while it runs, so it belongs
// at boot, before anything else opens a transaction, and says so first.
func VacuumAndLog(ctx context.Context, db *sql.DB, log *slog.Logger) {
	start := time.Now()
	before, err := dbBytes(ctx, db)
	if err == nil {
		log.Info("vacuuming the database", "bytes_before", before)
		stop := heartbeat(log, "vacuuming the database, still running", func() []any { return nil })
		_, err = db.ExecContext(ctx, `VACUUM`)
		stop()
	}
	if err == nil {
		// The rewrite lands in the WAL; the checkpoint is what shrinks the file.
		_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	var after int64
	if err == nil {
		after, err = dbBytes(ctx, db)
	}
	if err != nil {
		log.Warn("vacuuming the database failed", "err", err)
		return
	}
	log.Info("database vacuumed", "bytes_before", before, "bytes_after", after, "took", took(start))
}

func dbBytes(ctx context.Context, db *sql.DB) (int64, error) {
	var pages, size int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, fmt.Errorf("read page_count: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
		return 0, fmt.Errorf("read page_size: %w", err)
	}
	return pages * size, nil
}
