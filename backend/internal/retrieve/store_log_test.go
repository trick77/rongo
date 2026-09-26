package retrieve

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSearchVector_logsASlowSearch(t *testing.T) {
	// A vector search that took minutes was invisible: the turn died on the
	// reader's timeout and the log had only the interrupt. A slow one says
	// so, with what it searched.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "A.java", "a", "class A {}", nearVec)
	var log bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })
	threshold := slowVectorSearch
	slowVectorSearch = 0
	t.Cleanup(func() { slowVectorSearch = threshold })

	if _, err := NewStore(db).SearchVector(context.Background(), queryVec, 10, DefaultMaxDistance, []string{"shop"}); err != nil {
		t.Fatalf("SearchVector() err = %v", err)
	}

	got := log.String()
	for _, want := range []string{"level=WARN", `msg="slow vector search"`, "repos=[shop]", "k=10", "hits=1", "took="} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want %q", got, want)
		}
	}
}

func TestSearchVector_quietWhenFast(t *testing.T) {
	db := testDB(t)
	var log bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	if _, err := NewStore(db).SearchVector(context.Background(), queryVec, 10, DefaultMaxDistance, nil); err != nil {
		t.Fatalf("SearchVector() err = %v", err)
	}
	if log.Len() != 0 {
		t.Errorf("log = %q, want nothing", log.String())
	}
}

func TestSearchVector_anInterruptSaysSo(t *testing.T) {
	// sqlite-vec reports an interrupt as "SQL logic error: chunks iter
	// error"; the error has to name the cancel and how long the search ran.
	db := testDB(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("reader went away"))

	_, err := NewStore(db).SearchVector(ctx, queryVec, 10, DefaultMaxDistance, nil)

	if err == nil || !strings.Contains(err.Error(), "vector search interrupted after") ||
		!strings.Contains(err.Error(), "reader went away") {
		t.Errorf("err = %v, want the interrupt, its duration and its cause", err)
	}
}
