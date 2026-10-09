package store

import (
	"context"
	"testing"
	"time"
)

// TestOpen_aTransactionThatReadsFirstStillGetsItsWrite: in WAL a deferred
// transaction that opens with a read holds a snapshot, and its first write
// after another connection has committed fails at once with "database is
// locked" — the busy timeout never runs once the connection is in a read
// transaction (btreeBeginTrans retries only from TRANS_NONE). Reads a
// transaction never asked for happen too: a virtual table connects on its
// first use by reading its shadow tables, at prepare time, before the
// statement's own write. Open therefore begins every writing transaction
// IMMEDIATE, taking the write lock up front, so the other writer waits on the
// busy timeout instead of our write failing.
func TestOpen_aTransactionThatReadsFirstStillGetsItsWrite(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}

	// Another connection commits. Under a deferred BEGIN it does so at once
	// and our snapshot is stale; under BEGIN IMMEDIATE it waits for us.
	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, `INSERT INTO t (x) VALUES (1)`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("other writer: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO t (x) VALUES (2)`); err != nil {
		t.Fatalf("write after a read in the same transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("other writer after our commit: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d, %v; want both writes", n, err)
	}
}
