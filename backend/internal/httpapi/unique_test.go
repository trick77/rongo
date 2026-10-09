package httpapi

import (
	"slices"
	"testing"
)

func TestAddUnique_keepsTheFirstOfARepeatInOrder(t *testing.T) {
	seen := map[string]bool{}
	var got []string

	got = addUnique(got, seen, "lib", "api")
	got = addUnique(got, seen, "lib", "ui", "api")

	if want := []string{"lib", "api", "ui"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAddUnique_nothingAddedLeavesNil(t *testing.T) {
	if got := addUnique(nil, map[string]bool{"a": true}, "a"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
