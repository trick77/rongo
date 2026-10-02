package sqlutil

import "testing"

func TestPlaceholders(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: "?", 3: "?,?,?"} {
		if got := Placeholders(n); got != want {
			t.Errorf("Placeholders(%d) = %q, want %q", n, got, want)
		}
	}
}
