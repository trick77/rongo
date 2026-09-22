package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/store"
)

// fakeEmbedServer answers /embeddings with a vector per input whose every
// component is the input's length, and records each request's inputs.
func fakeEmbedServer(t *testing.T) (*httptest.Server, func() [][]string) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		seen = append(seen, req.Input)
		mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, in := range req.Input {
			v := make([]float32, embed.Dim())
			for j := range v {
				v[j] = float32(len(in))
			}
			data[i] = item{Index: i, Embedding: v}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), seen...)
	}
}

func queryCacheDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, embed.Dim()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func fakeClient(t *testing.T, srv *httptest.Server) *embed.Client {
	t.Helper()
	c, err := embed.NewClient(embed.Config{BaseURL: srv.URL, APIKey: "invented", HeartbeatInterval: -1}, srv.Client())
	if err != nil {
		t.Fatalf("embed.NewClient: %v", err)
	}
	return c
}

func TestQueryCache_secondCallWithTheSameTextMakesNoRequest(t *testing.T) {
	// Given
	srv, seen := fakeEmbedServer(t)
	testee := newQueryCache(fakeClient(t, srv), queryCacheDB(t))
	ctx := context.Background()

	// When
	first, err := testee.Embed(ctx, []string{"where is the widget stored"})
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	second, err := testee.Embed(ctx, []string{"where is the widget stored"})
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}

	// Then
	if got := len(seen()); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	if len(second) != 1 || len(second[0]) != embed.Dim() || second[0][0] != first[0][0] {
		t.Errorf("cached vector differs from the embedded one")
	}
}

func TestQueryCache_embedsOnlyMissesAndKeepsInputOrder(t *testing.T) {
	// Given: one text already cached.
	srv, seen := fakeEmbedServer(t)
	testee := newQueryCache(fakeClient(t, srv), queryCacheDB(t))
	ctx := context.Background()
	if _, err := testee.Embed(ctx, []string{"abc"}); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// When: a batch of new, cached, new-again text.
	got, err := testee.Embed(ctx, []string{"abcdefgh", "abc", "abcdefgh", "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Then: one more request, carrying each miss once.
	reqs := seen()
	if len(reqs) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(reqs))
	}
	if len(reqs[1]) != 2 || reqs[1][0] != "abcdefgh" || reqs[1][1] != "a" {
		t.Errorf("second request inputs = %q, want [abcdefgh a]", reqs[1])
	}
	want := []float32{8, 3, 8, 1}
	if len(got) != len(want) {
		t.Fatalf("got %d vectors, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i][0] != w {
			t.Errorf("vector %d marker = %v, want %v", i, got[i][0], w)
		}
	}
}

func TestQueryCache_emptyInputMakesNoRequest(t *testing.T) {
	// Given
	srv, seen := fakeEmbedServer(t)
	testee := newQueryCache(fakeClient(t, srv), queryCacheDB(t))

	// When
	got, err := testee.Embed(context.Background(), nil)

	// Then
	if err != nil || len(got) != 0 || len(seen()) != 0 {
		t.Errorf("Embed(nil) = %d vectors, err %v, %d requests; want none", len(got), err, len(seen()))
	}
}

func TestQueryHash_neverCollidesWithAContentHash(t *testing.T) {
	// A chunk's content hash is bare hex; the prefix keeps a query from ever
	// reading a chunk's vector or overwriting one.
	h := queryHash("anything")
	if len(h) != len("query:")+64 || h[:6] != "query:" {
		t.Errorf("queryHash = %q, want query: plus 64 hex characters", h)
	}
}
