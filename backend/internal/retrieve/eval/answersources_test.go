package eval

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

// The record keeps every source the answer was written from, with how it got
// there, so a part the answer missed can be told apart: never gathered, or
// gathered and not used.
func TestGatheredSources_recordHowEachSourceArrived(t *testing.T) {
	srcs := []ask.Source{
		{Repo: "letters", Path: "Vorlage/A.mtxt", StartLine: 1, EndLine: 40, Reason: "hit"},
		{Repo: "base", Path: "Baustein/#B.mtxm", StartLine: 3, EndLine: 3, Reason: "include:B from letters/Vorlage/A.mtxt", Hop: 1},
		{Repo: "letters", Kind: ask.SourceCommit, SHA: "abc123", Reason: "hit", Paths: []string{"Vorlage/D.mtxt"}},
		{Repo: "letters", Path: "README.md", Reason: "hit"},
	}
	got := gatheredSources(srcs)
	want := []gatheredSource{
		{Path: "letters/Vorlage/A.mtxt", Lines: "1-40", Reason: "hit"},
		{Path: "base/Baustein/#B.mtxm", Lines: "3", Reason: "include:B from letters/Vorlage/A.mtxt", Hop: 1},
		{Path: "letters@abc123", Reason: "hit"},
		{Path: "letters/README.md", Reason: "hit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gatheredSources =\n%+v\nwant\n%+v", got, want)
	}

	// The commit counts for the file it changed.
	parts := []flowPart{{Repo: "letters", Path: "Vorlage/A.mtxt"}, {Repo: "letters", Path: "Vorlage/C.mtxt"}, {Repo: "base", Path: "Baustein/#B.mtxm"}, {Repo: "letters", Path: "Vorlage/D.mtxt"}}
	hit, missing := partsGathered(parts, srcs)
	if hit != 3 || !reflect.DeepEqual(missing, []string{"letters/Vorlage/C.mtxt"}) {
		t.Errorf("partsGathered = %d, %v; want 3, [letters/Vorlage/C.mtxt]", hit, missing)
	}
	if hit, missing := partsGathered(parts, nil); hit != 0 || len(missing) != 4 {
		t.Errorf("no sources: %d, %v", hit, missing)
	}
}

// The answers file keeps the gathered sources under their own keys beside the
// old count, and a turn with none carries none.
func TestAnswerRecord_keepsTheGatheredSources(t *testing.T) {
	b, err := json.Marshal(answerRecord{Sources: 1, Gathered: []gatheredSource{{Path: "a/b", Reason: "hit"}}, GatherHit: 1, NotGathered: []string{"a/c"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"sources":1`, `"gathered":[{"path":"a/b","reason":"hit","hop":0}]`, `"gather_hit":1`, `"not_gathered":["a/c"]`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("record %s lacks %s", b, key)
		}
	}
	b, err = json.Marshal(answerRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"gathered"`) || strings.Contains(string(b), `"gather_hit"`) || strings.Contains(string(b), `"not_gathered"`) {
		t.Errorf("record without sources carries them: %s", b)
	}
}
