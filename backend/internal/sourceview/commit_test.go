package sourceview

import (
	"context"
	"errors"
	"testing"
)

func TestCommit_servesARecordedCommitWithItsFiles(t *testing.T) {
	f := newFixture(t, 1<<20)
	if _, err := f.db.Exec(`INSERT INTO commits (repo, sha, committed_at, subject, body, paths) VALUES ('peeq', ?, '2026-09-17T10:00:00Z', 'two', '', 'internal/a.go')`, f.second); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.Commit(context.Background(), "peeq", f.second)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got.Repo != "peeq" || got.Branch != "main" || got.SHA != f.second || got.Subject != "two" {
		t.Errorf("Commit = %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "internal/a.go" || !got.Files[0].Indexed || got.Files[0].Added != 1 {
		t.Errorf("files = %+v, want internal/a.go +1, indexed", got.Files)
	}

	// A commit the lane never recorded is not served, however real.
	_, err = f.svc.Commit(context.Background(), "peeq", f.first)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("an unrecorded commit: err = %v, want not found", err)
	}
	// Neither is a malformed sha, an unknown repository, or a service
	// without a reader.
	if _, err := f.svc.Commit(context.Background(), "peeq", "-rf"); !errors.Is(err, ErrInvalid) {
		t.Errorf("malformed sha: %v", err)
	}
	if _, err := f.svc.Commit(context.Background(), "nope", f.second); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown repo: %v", err)
	}
	if _, err := f.svc.WithCommits(nil).Commit(context.Background(), "peeq", f.second); !errors.Is(err, ErrNotFound) {
		t.Errorf("no reader: %v", err)
	}
}
