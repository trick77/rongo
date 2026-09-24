package retrieve

import "slices"

// contains is the tests' spelling of slices.Contains, kept so the assertions
// read as they always have.
func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}
