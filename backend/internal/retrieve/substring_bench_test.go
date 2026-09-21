package retrieve

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

// benchDB builds a corpus of n chunks through the same driver and schema the
// product uses. Bulk-inserted in one transaction: the benchmark is about the
// SCAN, and a per-row commit would be most of the wall clock.
//
// One chunk in every 500 carries the identifier, which is roughly what a real
// corpus looks like and keeps the hub guard out of the measurement.
func benchDB(b *testing.B, n int) *sql.DB {
	b.Helper()
	db, err := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, dim); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES (?,?,?)`,
		"bench", "file:///bench", "master"); err != nil {
		b.Fatalf("insert repo: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	filler := `public WsVertragsnehmerType toVertragsnehmerType(final Policenantrag s) {
		val vertragsnehmer = s.getVertragsnehmer();
		return new WsVertragsnehmerType().withName(vertragsnehmer.getNachname());
	}`
	for i := range n {
		raw := filler
		if i%500 == 0 {
			raw = converterCodeOnly
		}
		res, err := tx.Exec(`INSERT INTO files (repo, path, sha, lang) VALUES (?,?,?,?)`,
			"bench", fmt.Sprintf("src/F%06d.java", i), "sha", "java")
		if err != nil {
			b.Fatalf("insert file: %v", err)
		}
		fileID, _ := res.LastInsertId()
		res, err = tx.Exec(`
			INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
			VALUES (?,0,1,9,?,?,?,?,?)`,
			fileID, "sym", "enriched "+raw, raw, 5, fmt.Sprintf("h%06d", i))
		if err != nil {
			b.Fatalf("insert chunk: %v", err)
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO chunks_fts (rowid, raw_text) VALUES (?,?)`, id, raw); err != nil {
			b.Fatalf("insert keywords: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
	return db
}

// BenchmarkSearchSubstringIn measures the rung's one real cost: it is a SCAN,
// with no index behind it, so the number that matters is how it grows with the
// corpus rather than how it does on a fixture.
//
// It runs through the same ncruces wasm driver the product uses. A native
// sqlite3 figure would flatter it: the same scan measured with the CLI over
// 21,952 chunks came out at 47ms, and wasm is several times that.
//
// The bar is sub-second per term against a production-sized corpus. Past it the
// answer is an FTS5 trigram index (tokenize='trigram'), which costs a
// mirror-managed table, a migration and a full backfill — so it is the fallback,
// not the first move.
func BenchmarkSearchSubstringIn(b *testing.B) {
	for _, n := range []int{1000, 10000, 25000} {
		b.Run(fmt.Sprintf("chunks=%d", n), func(b *testing.B) {
			db := benchDB(b, n)
			s := NewStore(db)
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.SearchSubstringIn(b.Context(), "anzahlfahrzeuge", 40, nil, nil); err != nil {
					b.Fatalf("SearchSubstringIn: %v", err)
				}
			}
		})
	}
}

// BenchmarkSubstringLane is the number that actually matters: one TURN, not one
// term. The lane issues a query per generated term, and each does two scans (the
// count guard and the fetch), so a per-term figure understates the per-turn cost
// by the number of terms.
func BenchmarkSubstringLane(b *testing.B) {
	question := "Im Policenantrag Backend, wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"
	terms := BuildSubstringTerms(question, []string{"Policenantrag", "Datenweitergabe"})
	for _, n := range []int{10000, 25000} {
		b.Run(fmt.Sprintf("chunks=%d/terms=%d", n, len(terms)), func(b *testing.B) {
			db := benchDB(b, n)
			s := NewStore(db)
			b.ResetTimer()
			for b.Loop() {
				for _, term := range terms {
					if _, err := s.SearchSubstringIn(b.Context(), term, 40, nil, nil); err != nil {
						b.Fatalf("SearchSubstringIn: %v", err)
					}
				}
			}
		})
	}
}

// BenchmarkSearchKeywordIn is the same shape against the FTS lane, so the scan's
// cost is read next to an indexed lookup rather than in isolation.
func BenchmarkSearchKeywordIn(b *testing.B) {
	for _, n := range []int{1000, 10000, 25000} {
		b.Run(fmt.Sprintf("chunks=%d", n), func(b *testing.B) {
			db := benchDB(b, n)
			s := NewStore(db)
			match := BuildFTSMatch("vertragsnehmer")
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.SearchKeywordIn(b.Context(), match, 40, nil, nil); err != nil {
					b.Fatalf("SearchKeywordIn: %v", err)
				}
			}
		})
	}
}

// BenchmarkSeedGate is the seed rung's per-TURN cost, to be read next to
// BenchmarkSubstringLane's — the scan work a turn already pays.
//
// Per turn and not per term on purpose: the gate is ONE query over the corpus
// counting every accessor spelling at once, so a per-term figure would divide
// a fixed cost by a number that does not change it. The note on
// BenchmarkSubstringLane warns of the opposite mistake one lane up, where the
// terms really are separate scans.
func BenchmarkSeedGate(b *testing.B) {
	question := "Im Policenantrag Backend, wie wird die Anzahl Fahrzeuge an Kernsystem weitergegeben"
	code := []string{"Policenantrag", "getAnzahlFahrzeuge"}
	terms := BuildAccessorTerms(
		AccessorStems(question, BuildSubstringTerms(question, code), code))
	var ask []string
	for _, t := range terms {
		if len([]rune(t)) >= minSeedRunes {
			ask = append(ask, t)
		}
	}
	for _, n := range []int{10000, 25000} {
		b.Run(fmt.Sprintf("chunks=%d/terms=%d", n, len(ask)), func(b *testing.B) {
			db := benchDB(b, n)
			s := NewStore(db)
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.FilesMatchingSubstrings(b.Context(), ask, nil, nil); err != nil {
					b.Fatalf("FilesMatchingSubstrings: %v", err)
				}
			}
		})
	}
}
