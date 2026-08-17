package embed

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

const testDim = 4

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, testDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCache_getReturnsOnlyWhatItHolds(t *testing.T) {
	// Given
	db := testDB(t)
	testee := NewCache(db, "text-embedding-3-small", testDim)
	if err := testee.Put(context.Background(), "hash-a", vecOf(1, testDim)); err != nil {
		t.Fatalf("Put() err = %v", err)
	}

	// When
	got, err := testee.Get(context.Background(), []string{"hash-a", "hash-b"})

	// Then: the misses stay for the caller to embed. Returning a zero vector
	// for them would store silence as if it were meaning.
	if err != nil {
		t.Fatalf("Get() err = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Get() returned %d entries, want 1", len(got))
	}
	if _, ok := got["hash-a"]; !ok {
		t.Errorf("Get() = %v, want the hit for hash-a", got)
	}
}

func TestCache_roundTripsAVectorBitForBit(t *testing.T) {
	// Given: values chosen so a float32/float64 confusion or a byte-order slip
	// shows up as an inequality rather than as a rounding difference.
	db := testDB(t)
	testee := NewCache(db, "m", testDim)
	want := []float32{0.1, -0.2, float32(math.Pi), 1e-8}
	if err := testee.Put(context.Background(), "h", want); err != nil {
		t.Fatalf("Put() err = %v", err)
	}

	// When
	got, err := testee.Get(context.Background(), []string{"h"})

	// Then
	if err != nil {
		t.Fatalf("Get() err = %v", err)
	}
	for i := range want {
		if got["h"][i] != want[i] {
			t.Errorf("component %d = %v, want exactly %v", i, got["h"][i], want[i])
		}
	}
}

func TestCache_isKeyedByModelSoAnotherModelMisses(t *testing.T) {
	// Given: the same content under one model.
	db := testDB(t)
	small := NewCache(db, "text-embedding-3-small", testDim)
	if err := small.Put(context.Background(), "h", vecOf(1, testDim)); err != nil {
		t.Fatalf("Put() err = %v", err)
	}

	// When: the same hash is looked up under another model.
	large := NewCache(db, "text-embedding-3-large", testDim)
	got, err := large.Get(context.Background(), []string{"h"})

	// Then: a hit here would silently reuse the first model's vectors and make
	// the small-vs-large comparison meaningless.
	if err != nil {
		t.Fatalf("Get() err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get() = %v, want a miss under a different model", got)
	}
}

func TestCache_getHandlesMoreHashesThanSQLiteTakesVariables(t *testing.T) {
	// Given: a real index passes thousands of hashes at once. A single
	// IN (?,?,…) blows SQLite's variable limit — a hard failure on the first
	// real run that a two-hash test never reaches.
	db := testDB(t)
	testee := NewCache(db, "m", testDim)
	const n = 1500
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("hash-%04d", i)
		if err := testee.Put(context.Background(), hashes[i], vecOf(float32(i), testDim)); err != nil {
			t.Fatalf("Put(%d) err = %v", i, err)
		}
	}

	// When
	got, err := testee.Get(context.Background(), hashes)

	// Then
	if err != nil {
		t.Fatalf("Get() err = %v, want nil", err)
	}
	if len(got) != n {
		t.Fatalf("Get() returned %d entries, want %d", len(got), n)
	}
	if got["hash-1499"][0] != 1499 {
		t.Errorf("hash-1499 = %v, want the vector marked 1499", got["hash-1499"][0])
	}
}

func TestCache_putRejectsAWrongDimension(t *testing.T) {
	// Given: a short vector stored here would reach vec0 much later, far from
	// whatever produced it.
	db := testDB(t)
	testee := NewCache(db, "m", testDim)

	// When
	err := testee.Put(context.Background(), "h", vecOf(1, testDim-1))

	// Then
	if err == nil {
		t.Fatal("Put() err = nil, want a rejection of the wrong dimension")
	}
}

func TestCache_putIsIdempotent(t *testing.T) {
	// Given: re-indexing the same content hits Put again for content already
	// cached, which must not fail on the primary key.
	db := testDB(t)
	testee := NewCache(db, "m", testDim)
	if err := testee.Put(context.Background(), "h", vecOf(1, testDim)); err != nil {
		t.Fatalf("first Put() err = %v", err)
	}

	// When
	err := testee.Put(context.Background(), "h", vecOf(2, testDim))

	// Then
	if err != nil {
		t.Fatalf("second Put() err = %v, want nil", err)
	}
	got, _ := testee.Get(context.Background(), []string{"h"})
	if got["h"][0] != 2 {
		t.Errorf("stored vector = %v, want the newer one", got["h"][0])
	}
}

func TestCache_getWithNoHashesTouchesNothing(t *testing.T) {
	// Given / When
	got, err := NewCache(testDB(t), "m", testDim).Get(context.Background(), nil)

	// Then
	if err != nil || len(got) != 0 {
		t.Fatalf("Get(nil) = %v, %v; want an empty map and no error", got, err)
	}
}
