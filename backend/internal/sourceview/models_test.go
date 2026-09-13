package sourceview

import (
	"context"
	"strings"
	"testing"
)

func TestModels_listsTheIndexedProcessModelsAndReadsThemAtTheirCommit(t *testing.T) {
	f := newFixture(t, 1<<20)
	// Two models the index took, one it skipped, and a Java file that is not
	// a model: only the first two are listed.
	for _, row := range []struct{ path, lang, skip string }{
		{"workflow/order.bpmn", "bpmn", ""},
		{"workflow/returns.bpmn", "bpmn", ""},
		{"workflow/huge.bpmn", "bpmn", "too_large"},
		{"src/Order.java", "java", ""},
	} {
		if _, err := f.db.Exec(`INSERT INTO files (repo, path, sha, lang, skip_reason) VALUES ('peeq', ?, ?, ?, ?)`,
			row.path, f.second, row.lang, row.skip); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := f.svc.Models(context.Background(), "peeq")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range refs {
		paths = append(paths, r.Path)
		if r.SHA != f.second {
			t.Errorf("%s listed at %s, want the indexed commit %s", r.Path, r.SHA, f.second)
		}
	}
	if got := strings.Join(paths, ","); got != "workflow/order.bpmn,workflow/returns.bpmn" {
		t.Errorf("Models = %s", got)
	}
	if refs, err := f.svc.Models(context.Background(), "nobody"); err != nil || len(refs) != 0 {
		t.Errorf("unknown repository: %v, %v", refs, err)
	}

	// ReadModel is Read: the indexed file at its commit, under Read's rules.
	body, err := f.svc.ReadModel(context.Background(), "peeq", "internal/a.go", f.first)
	if err != nil || !strings.Contains(string(body), "func One") || strings.Contains(string(body), "moved") {
		t.Errorf("ReadModel at the first commit: %q, %v", body, err)
	}
	if _, err := f.svc.ReadModel(context.Background(), "peeq", "config/prod.env", ""); err == nil {
		t.Error("a skipped file was read")
	}
}
