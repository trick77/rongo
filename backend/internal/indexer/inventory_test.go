package indexer

import (
	"strings"
	"testing"
	"time"
)

// attrMap turns the flat key/value slice slog takes into something a test can
// assert on without counting positions.
func attrMap(t *testing.T, attrs []any) map[string]any {
	t.Helper()
	if len(attrs)%2 != 0 {
		t.Fatalf("attrs has odd length %d: %v", len(attrs), attrs)
	}
	out := map[string]any{}
	for i := 0; i < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			t.Fatalf("attrs[%d] = %v, want a string key", i, attrs[i])
		}
		out[key] = attrs[i+1]
	}
	return out
}

// TestInventoryAttrs_describesAnIndexedRemote: the line a healthy repository
// draws on boot. Every field here answers a question a support conversation
// opens with.
func TestInventoryAttrs_describesAnIndexedRemote(t *testing.T) {
	// Given
	st := RepoState{
		Name: "peeq", Project: "peeq", Part: "backend",
		CloneURL: "https://github.com/trick77/peeq.git", Branch: "master",
		Enabled: true, LastSHA: "611255ac0ffee1122334455", Files: 412, Chunks: 3120,
		LastRunAt: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),
	}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	for key, want := range map[string]any{
		"repo": "peeq", "project": "peeq", "part": "backend",
		"source": "https://github.com/trick77/peeq.git", "branch": "master",
		"enabled": true, "indexed_sha": "611255a", "files": 412, "chunks": 3120,
		"last_run_at": "2026-09-09T12:00:00Z",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["last_error"]; ok {
		t.Errorf("last_error = %v, want it left out when there is none", got["last_error"])
	}
}

// TestInventoryAttrs_namesASnapshotAsItsOwnSource: a snapshot's clone_url is
// empty, which is exactly what makes it one. Printing nothing there would read
// as a missing configuration rather than a deliberate kind of entry.
func TestInventoryAttrs_namesASnapshotAsItsOwnSource(t *testing.T) {
	// Given
	st := RepoState{Name: "acme-core", Project: "schadenmeldung", Enabled: true,
		Branch: "snapshot", LastSHA: "bb8df973717891d"}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	if got["source"] != "snapshot" {
		t.Errorf("source = %v, want %q", got["source"], "snapshot")
	}
}

// TestInventoryAttrs_saysNeverIndexedOutLoud: the single most common cause of
// "rongo cannot find anything in X". It must not look like a repository that
// indexed zero files.
func TestInventoryAttrs_saysNeverIndexedOutLoud(t *testing.T) {
	// Given
	st := RepoState{Name: "shop", Project: "shop", CloneURL: "file:///x", Enabled: true}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	if got["indexed"] != false {
		t.Errorf("indexed = %v, want false stated explicitly", got["indexed"])
	}
	for _, key := range []string{"indexed_sha", "files", "chunks", "branch", "part", "last_run_at"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s = %v, want it left out when there is no value", key, got[key])
		}
	}
}

// TestInventoryAttrs_carriesTheLastFailure: a repository whose last run failed
// says so on boot, rather than making somebody open the Repos page to find out.
func TestInventoryAttrs_carriesTheLastFailure(t *testing.T) {
	// Given
	st := RepoState{Name: "shop", Project: "shop", CloneURL: "file:///x", Enabled: true,
		LastError: "configured branch not found"}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	s, _ := got["last_error"].(string)
	if !strings.Contains(s, "branch not found") {
		t.Errorf("last_error = %v, want the failure verbatim", got["last_error"])
	}
}

// TestInventoryAttrs_statesTheDeclaredEdges: `uses` is the one part of a
// repository's configuration with no other way to check it took effect.
// repos.yaml is read once, at boot, so an operator who edits an edge and
// restarts has only the arrow on the Projects page to go by — and if the parse
// failed, that arrow is the PREVIOUS configuration's. It then reads as the
// direction being drawn backwards rather than as the file never being loaded,
// which is exactly how it was reported.
func TestInventoryAttrs_statesTheDeclaredEdges(t *testing.T) {
	// Given
	st := RepoState{Name: "schadenmeldung-ui", Project: "schadenmeldung", Part: "ui",
		Enabled: true, Uses: []string{"schadenmeldung-service"}}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	if got["uses"] != "schadenmeldung-service" {
		t.Errorf("uses = %v, want the declared edge", got["uses"])
	}
}

// TestInventoryAttrs_omitsUsesWhenThereAreNone: the consumer declares the edge,
// so the repository at the other end of it has nothing to say. An empty list
// would put `uses=` on most lines in most corpora.
func TestInventoryAttrs_omitsUsesWhenThereAreNone(t *testing.T) {
	// Given
	st := RepoState{Name: "schadenmeldung-service", Project: "schadenmeldung", Enabled: true}

	// When
	got := attrMap(t, InventoryAttrs(st))

	// Then
	if _, ok := got["uses"]; ok {
		t.Errorf("uses = %v, want it left out", got["uses"])
	}
}

// TestSummarise_countsTheCorpusTheWayAnOperatorReadsIt.
func TestSummarise_countsTheCorpusTheWayAnOperatorReadsIt(t *testing.T) {
	// Given
	states := []RepoState{
		{Name: "shop-ui", Project: "shop", CloneURL: "file:///a", Enabled: true, LastSHA: "aaa"},
		{Name: "shop-events", Project: "shop", CloneURL: "file:///b", Enabled: false},
		{Name: "acme-core", Project: "acme", Enabled: true, LastSHA: "bbb"},
		{Name: "peeq", Project: "peeq", CloneURL: "file:///c", Enabled: true,
			LastError: "boom"},
	}

	// When
	got := Summarise(states)

	// Then
	want := Inventory{Projects: 3, Repos: 4, Enabled: 3, Parked: 1, Snapshots: 1,
		Indexed: 2, Failing: 1}
	if got != want {
		t.Errorf("Summarise() = %+v, want %+v", got, want)
	}
}

// TestSummarise_usesTheProjectsPageFallback: a row written before projects
// shipped stands as a project of its own, so the count in the log and the count
// on the page cannot disagree.
func TestSummarise_usesTheProjectsPageFallback(t *testing.T) {
	// Given
	states := []RepoState{
		{Name: "old-one", Project: "", CloneURL: "file:///a", Enabled: true},
		{Name: "old-two", Project: "", CloneURL: "file:///b", Enabled: true},
	}

	// When
	got := Summarise(states)

	// Then
	if got.Projects != 2 {
		t.Errorf("Projects = %d, want 2 — an empty project is not one shared product", got.Projects)
	}
}

// TestInventoryAttrs_quietWhenThereIsNothingToReport: parked, snapshots and
// failing are omitted at zero, so an ordinary corpus does not carry three zeros
// on every boot and a non-zero one is worth noticing.
func TestInventoryAttrs_quietWhenThereIsNothingToReport(t *testing.T) {
	// Given
	inv := Inventory{Projects: 1, Repos: 1, Enabled: 1, Indexed: 1}

	// When
	got := attrMap(t, inv.Attrs())

	// Then
	for _, key := range []string{"parked", "snapshots", "failing"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s = %v, want it left out at zero", key, got[key])
		}
	}
	if got["projects"] != 1 || got["repositories"] != 1 || got["enabled"] != 1 {
		t.Errorf("Attrs() = %v, want the four always-present counts", got)
	}
}
