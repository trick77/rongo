package sqlutil

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Scanner is *sql.Row and *sql.Rows.
type Scanner interface {
	Scan(dest ...any) error
}

// ScanOne reads one row into dest, telling "no row" apart from a failure:
// found is false and err nil when the query matched nothing. Every lookup
// whose caller answers 404 or "none" to an empty result reads through it, so
// sql.ErrNoRows is checked in one place and the same way.
func ScanOne(row Scanner, dest ...any) (found bool, err error) {
	err = row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Affected is the tail of an UPDATE or DELETE whose caller wants to know
// whether a row matched. Both the statement's error and RowsAffected's are
// wrapped as "<what>: …", so a caller passes its own label once.
func Affected(res sql.Result, err error, what string) (bool, error) {
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return n > 0, nil
}

// Stamp is the layout datetime('now') writes.
const Stamp = "2006-01-02 15:04:05"

// ParseStamp reads a stored stamp; an unreadable one is the zero time, as
// every read of a stamp column has always treated it.
func ParseStamp(s string) time.Time {
	t, _ := time.Parse(Stamp, s)
	return t
}
