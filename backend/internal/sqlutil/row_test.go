package sqlutil_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/sqlutil"
	"github.com/trick77/rongo/internal/store/storetest"
)

func TestScanOne_tellsNoRowFromARowAndFromAFailure(t *testing.T) {
	db := storetest.Open(t, 4)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users (subject, email, is_admin) VALUES ('anna', 'a@x.invalid', 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var email string
	var admin bool
	found, err := sqlutil.ScanOne(db.QueryRowContext(ctx, `SELECT email, is_admin FROM users WHERE subject = ?`, "anna"), &email, &admin)
	if err != nil || !found || email != "a@x.invalid" || admin {
		t.Fatalf("row = %q, %v, found %v, err %v", email, admin, found, err)
	}

	found, err = sqlutil.ScanOne(db.QueryRowContext(ctx, `SELECT email FROM users WHERE subject = ?`, "bruno"), &email)
	if err != nil || found {
		t.Fatalf("no row: found %v, err %v", found, err)
	}

	found, err = sqlutil.ScanOne(db.QueryRowContext(ctx, `SELECT email FROM no_such_table`), &email)
	if err == nil || found || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failure: found %v, err %v", found, err)
	}
}

type fakeResult struct {
	n   int64
	err error
}

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, r.err }

func TestAffected_reportsAMatchAndWrapsEitherFailureWithTheLabel(t *testing.T) {
	if ok, err := sqlutil.Affected(fakeResult{n: 1}, nil, "rename thread"); !ok || err != nil {
		t.Fatalf("one row: %v, %v", ok, err)
	}
	if ok, err := sqlutil.Affected(fakeResult{n: 0}, nil, "rename thread"); ok || err != nil {
		t.Fatalf("no row: %v, %v", ok, err)
	}
	boom := errors.New("boom")
	if ok, err := sqlutil.Affected(nil, boom, "rename thread"); ok || !errors.Is(err, boom) || err.Error() != "rename thread: boom" {
		t.Fatalf("exec failure: %v, %v", ok, err)
	}
	if ok, err := sqlutil.Affected(fakeResult{err: boom}, nil, "rename thread"); ok || !errors.Is(err, boom) || err.Error() != "rename thread: boom" {
		t.Fatalf("count failure: %v, %v", ok, err)
	}
}

func TestParseStamp_readsWhatDatetimeNowWritesAndZeroesTheRest(t *testing.T) {
	want := time.Date(2026, 10, 9, 13, 4, 5, 0, time.UTC)
	if got := sqlutil.ParseStamp(want.Format(sqlutil.Stamp)); !got.Equal(want) {
		t.Fatalf("ParseStamp = %v, want %v", got, want)
	}
	if got := sqlutil.ParseStamp("yesterday"); !got.IsZero() {
		t.Fatalf("ParseStamp(garbage) = %v, want zero", got)
	}
	if !strings.Contains(sqlutil.Stamp, "2006-01-02 15:04:05") {
		t.Fatalf("Stamp = %q", sqlutil.Stamp)
	}
}
