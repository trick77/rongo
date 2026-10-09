package sqlutil

import "testing"

func TestArgs(t *testing.T) {
	got := Args([]string{"a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Args = %v", got)
	}
	if ints := Args([]int64{7}); len(ints) != 1 || ints[0] != int64(7) {
		t.Fatalf("Args(int64) = %v", ints)
	}
	if empty := Args[string](nil); len(empty) != 0 {
		t.Fatalf("Args(nil) = %v", empty)
	}
}
