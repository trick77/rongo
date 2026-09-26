package httpapi

import (
	"testing"
	"time"
)

func TestTurnProgress_namesTheStepAndHowLongItRan(t *testing.T) {
	// A turn that dies thirteen minutes in has to say where it was: the
	// error alone named the search, not that the search was all of it.
	tr := &turn{started: time.Now().Add(-3 * time.Second)}
	tr.mark("understanding")
	tr.mark("searching")

	attrs := tr.progress()

	if len(attrs) != 4 || attrs[0] != "step" || attrs[1] != "searching" || attrs[2] != "took" {
		t.Fatalf("progress() = %v, want step=searching and took", attrs)
	}
	took, err := time.ParseDuration(attrs[3].(string))
	if err != nil || took < 3*time.Second {
		t.Errorf("took = %v (%v), want at least 3s", attrs[3], err)
	}
}

func TestTurnProgress_beforeAnyStep(t *testing.T) {
	tr := &turn{started: time.Now()}
	if attrs := tr.progress(); attrs[1] != "starting" {
		t.Errorf("progress() = %v, want step=starting", attrs)
	}
}
