// Package sqlutil holds the few string helpers every store builds SQL with.
// A leaf on purpose: it imports nothing of rongo's, and no driver, so any
// package may use it without taking on the database's dependencies.
package sqlutil

import "strings"

// Placeholders is n bind parameters for an IN list: "?,?,?", and empty for
// none.
//
// SQLite accepts the empty list, and it means what it says: `x IN ()` is
// false for every row and `x NOT IN ()` is true for every row. A caller for
// whom "no values" should mean something else — match nothing under NOT IN,
// say — has to write that itself; this does not guess.
func Placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Args is a slice as the bind parameters of one IN list: the values boxed
// for QueryContext's variadic any, in order. Empty for none.
func Args[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}
