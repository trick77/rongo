package retrieve

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

const dim = 4

// Unit vectors in four dimensions, so L2 distance is exactly sqrt(2-2cos) and
// the fixture's distances are known rather than measured:
//
//	query vs near = 0.0    query vs mid = 0.894    query vs far = 1.414
//
// far therefore sits outside DefaultMaxDistance (1.25) and near/mid inside it.
var (
	queryVec = []float32{1, 0, 0, 0}
	nearVec  = []float32{1, 0, 0, 0}
	midVec   = []float32{0.6, 0.8, 0, 0}
	farVec   = []float32{0, 1, 0, 0}
)

type fixedEmbedder struct{ vec []float32 }

func (e fixedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vec
	}
	return out, nil
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, dim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// addRepo registers a repository so files can reference it and hits can carry
// its branch.
func addRepo(t *testing.T, db *sql.DB, name, branch string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES (?,?,?)`,
		name, "file:///"+name, branch); err != nil {
		t.Fatalf("insert repo %s: %v", name, err)
	}
}

// addChunk inserts one indexed chunk across all three tables the way the
// writer does, and returns its id.
func addChunk(t *testing.T, db *sql.DB, repo, path, symbol, raw string, vec []float32) int64 {
	t.Helper()
	var fileID int64
	err := db.QueryRow(`SELECT id FROM files WHERE repo = ? AND path = ?`, repo, path).Scan(&fileID)
	if err == sql.ErrNoRows {
		res, err := db.Exec(`INSERT INTO files (repo, path, sha, lang) VALUES (?,?,?,?)`, repo, path, "sha", "java")
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		fileID, _ = res.LastInsertId()
	} else if err != nil {
		t.Fatalf("lookup file: %v", err)
	}
	var ordinal int
	db.QueryRow(`SELECT COALESCE(MAX(ordinal)+1, 0) FROM chunks WHERE file_id = ?`, fileID).Scan(&ordinal)
	res, err := db.Exec(`
		INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		fileID, ordinal, 10+ordinal, 20+ordinal, symbol, "enriched "+raw, raw, 5, path+raw)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO chunks_vec (rowid, embedding) VALUES (?,?)`, id, store.VecLiteral(vec)); err != nil {
		t.Fatalf("insert vector: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chunks_fts (rowid, raw_text) VALUES (?,?)`, id, raw); err != nil {
		t.Fatalf("insert keywords: %v", err)
	}
	return id
}

func TestSearchVector_dropsHitsBeyondTheDistanceBound(t *testing.T) {
	// Given: one chunk close to the query and one orthogonal to it. Without a
	// bound a KNN cannot fail — it returns k rows for any input whatsoever —
	// so this bound is what makes an empty result possible at all.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "A.java", "run", "sender.send()", nearVec)
	addChunk(t, db, "shop", "B.java", "other", "unrelated body", farVec)

	// When
	hits, err := NewStore(db).SearchVector(context.Background(), queryVec, 10, DefaultMaxDistance, nil)

	// Then
	if err != nil {
		t.Fatalf("SearchVector() err = %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "A.java" {
		t.Errorf("SearchVector() returned %d hits (%v), want only the near one", len(hits), hits)
	}
}

func TestSearchVector_repoFilterIsAPreFilter(t *testing.T) {
	// Given: a fixture whose global top-2 is entirely repository "peeq". If the
	// restriction were applied AFTER the KNN, asking for "loom" would return
	// nothing — which is what happens to every repository holding a small slice
	// of the corpus, i.e. most of them.
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "loom", "main")
	addChunk(t, db, "peeq", "a.go", "A", "alpha", nearVec)
	addChunk(t, db, "peeq", "b.go", "B", "bravo", nearVec)
	addChunk(t, db, "peeq", "c.go", "C", "charlie", nearVec)
	addChunk(t, db, "loom", "d.go", "D", "delta", midVec)

	// When
	hits, err := NewStore(db).SearchVector(context.Background(), queryVec, 2, DefaultMaxDistance, []string{"loom"})

	// Then
	if err != nil {
		t.Fatalf("SearchVector() err = %v", err)
	}
	if len(hits) != 1 || hits[0].Repo != "loom" {
		t.Errorf("SearchVector() = %v, want loom's chunk — the filter ran after the KNN", hits)
	}
}

func TestSearchKeyword_findsTheLiteralIdentifier(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "PromoMailJob.java", "send", "public void send() { promoMailer.dispatch(); }", farVec)
	addChunk(t, db, "shop", "Other.java", "run", "public void run() { cart.clear(); }", nearVec)

	// When
	hits, err := NewStore(db).SearchKeyword(context.Background(), BuildFTSMatch("promoMailer"), 10, nil)

	// Then
	if err != nil {
		t.Fatalf("SearchKeyword() err = %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "PromoMailJob.java" {
		t.Errorf("SearchKeyword() = %v, want the chunk containing the identifier", hits)
	}
}

func TestSearchKeyword_emptyMatchTouchesNothing(t *testing.T) {
	// Given / When
	hits, err := NewStore(testDB(t)).SearchKeyword(context.Background(), "", 10, nil)

	// Then
	if err != nil || len(hits) != 0 {
		t.Errorf("SearchKeyword(\"\") = %v, %v; want no hits and no error", hits, err)
	}
}

func TestSearch_literalIdentifierRanksAheadOfSemanticNoise(t *testing.T) {
	// Given: three chunks the embedding places right next to the query, and one
	// that literally contains the identifier but sits far away in vector space.
	// This is the whole reason the hybrid exists: the vector lane finds
	// "Teaser-Mail", the keyword lane finds PromoMailJob.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "Near1.java", "a", "irgendein anderer text", nearVec)
	addChunk(t, db, "shop", "Near2.java", "b", "noch ein anderer text", nearVec)
	addChunk(t, db, "shop", "PromoMailJob.java", "send", "class PromoMailJob { void send() {} }", midVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{Text: "PromoMailJob", K: 5})

	// Then
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search() found nothing")
	}
	if hits[0].Path != "PromoMailJob.java" {
		t.Errorf("first hit = %s, want PromoMailJob.java — semantic noise outranked a literal match", hits[0].Path)
	}
	if hits[0].Score <= 0 || len(hits[0].Lanes) == 0 {
		t.Errorf("hit = %+v, want a positive score and its lane provenance", hits[0])
	}
}

func TestSearch_noMatchesIsAnEmptySliceAndNoError(t *testing.T) {
	// Given: nothing matches either lane — the query vector is orthogonal to
	// every chunk and none of its words occur. "No hit means no hit": the
	// caller reports that with the terms it tried, and an error here would be
	// indistinguishable from a broken database.
	db := testDB(t)
	addRepo(t, db, "shop", "master")
	addChunk(t, db, "shop", "A.java", "run", "sender dispatch cart", farVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{Text: "quastenflossergetriebe", K: 5})

	// Then
	if err != nil {
		t.Fatalf("Search() err = %v, want nil", err)
	}
	if hits == nil {
		t.Fatal("Search() returned nil, want an empty slice")
	}
	if len(hits) != 0 {
		t.Errorf("Search() = %v, want no hits", hits)
	}
}

func TestSearch_everyHitIsCitable(t *testing.T) {
	// Given: every claim rongo makes must name repo, branch, file and line.
	db := testDB(t)
	addRepo(t, db, "shop", "release-2024.3")
	addChunk(t, db, "shop", "src/A.java", "run", "sender.send()", nearVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{Text: "sender", K: 5})

	// Then
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search() = %v, %v; want a hit", hits, err)
	}
	for _, h := range hits {
		if h.Repo == "" || h.Path == "" {
			t.Errorf("hit %+v has no repository or path", h)
		}
		// The branch has to travel WITH the hit: a forge URL built without it
		// may 404 off the default branch.
		if h.Branch != "release-2024.3" {
			t.Errorf("hit branch = %q, want release-2024.3", h.Branch)
		}
		if h.StartLine <= 0 || h.EndLine < h.StartLine {
			t.Errorf("hit %+v has no usable line range", h)
		}
	}
}

func TestSearch_repoFilterSurvivesTheWholePipeline(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "loom", "main")
	addChunk(t, db, "peeq", "a.go", "A", "sender dispatch", nearVec)
	addChunk(t, db, "loom", "b.go", "B", "sender dispatch", midVec)
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{Text: "sender", Repos: []string{"loom"}, K: 5})

	// Then
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search() found nothing in loom")
	}
	for _, h := range hits {
		if h.Repo != "loom" {
			t.Errorf("hit from %s leaked past the repository filter", h.Repo)
		}
	}
}
