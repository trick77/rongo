package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/sourceview"
)

// Evidence is the checkout as the record reads it back: a file at the commit
// an answer read it at, and a commit an answer was written from. The source
// viewer satisfies it, so a rework reads what a citation opens — same
// redaction, and a file the index now skips stays refused.
type Evidence interface {
	ReadRecorded(ctx context.Context, repo, path, sha string) (sourceview.File, error)
	RecordedCommit(ctx context.Context, repo, sha string) (sourceview.Commit, error)
}

// WithEvidence gives the store the checkout. Without it a source resolves
// only through the chunk it came from, which is what a test store without a
// git binary wants.
func (s *Store) WithEvidence(e Evidence) *Store {
	s.evidence = e
	return s
}

// chunkCeiling is the chunker's own ceiling. A window read back from git
// larger than a chunk can be was an overlong line the chunker split into
// siblings; the whole line is not what any one of them held, and it can be
// hundreds of kilobytes of minified code.
var chunkCeiling = indexer.DefaultChunkOptions()

// SaveSources records what an answer was actually written from: every source
// by what it is — repository, path, commit, lines — beside the row it came
// from. The row id is not stable across a re-index; the commit is.
func (s *Store) SaveSources(ctx context.Context, messageID int64, sources []ask.Source) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := saveSourcesTx(ctx, tx, messageID, sources); err != nil {
		return err
	}
	return tx.Commit()
}

func saveSourcesTx(ctx context.Context, tx *sql.Tx, messageID int64, sources []ask.Source) error {
	for _, src := range sources {
		// One id per row: a commit source has no chunk, a chunk no commit.
		chunkID, commitID := src.ChunkID, int64(0)
		path, start, end, symbol := src.Path, src.StartLine, src.EndLine, src.Symbol
		if src.IsCommit() {
			chunkID, commitID = 0, src.CommitID
			path, start, end, symbol = "", 0, 0, ""
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_sources (message_id, chunk_id, commit_id, reason, hop, repo, path, sha, start_line, end_line, symbol)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			messageID, chunkID, commitID, src.Reason, src.Hop, src.Repo, path, src.SHA, start, end, symbol); err != nil {
			return fmt.Errorf("store source %d/%d: %w", chunkID, commitID, err)
		}
	}
	return nil
}

// recorded is one message_sources row as stored.
type recorded struct {
	chunkID, commitID int64
	repo, path, sha   string
	start, end        int
	symbol, reason    string
	hop               int
}

// records reads a message's rows, chunks by hop then id, commits after.
// A message that does not belong to a thread owned by subject has none.
func (s *Store) records(ctx context.Context, subject string, messageID int64) ([]recorded, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ms.chunk_id, ms.commit_id, ms.repo, ms.path, ms.sha, ms.start_line, ms.end_line, ms.symbol, ms.reason, ms.hop
		FROM message_sources ms
		JOIN messages m ON m.id = ms.message_id
		JOIN threads t ON t.id = m.thread_id
		WHERE ms.message_id = ? AND t.user_subject = ?
		ORDER BY ms.commit_id <> 0, ms.hop, ms.chunk_id, ms.commit_id`, messageID, subject)
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []recorded
	for rows.Next() {
		var r recorded
		if err := rows.Scan(&r.chunkID, &r.commitID, &r.repo, &r.path, &r.sha, &r.start, &r.end, &r.symbol, &r.reason, &r.hop); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SourceRefs is the record of an answer's basis without its text: what the
// sources are, read from the database alone. Every follow-up needs this
// much — whether there is a basis, which repositories it spans — and only a
// rework needs the text, so the git reads wait for Sources.
func (s *Store) SourceRefs(ctx context.Context, subject string, messageID int64) ([]ask.Source, error) {
	recs, err := s.records(ctx, subject, messageID)
	if err != nil {
		return nil, err
	}
	out := make([]ask.Source, 0, len(recs))
	for _, r := range recs {
		src := ask.Source{ChunkID: r.chunkID, Repo: r.repo, Path: r.path, SHA: r.sha, Symbol: r.symbol,
			StartLine: r.start, EndLine: r.end, Reason: r.reason, Hop: r.hop}
		if r.commitID != 0 {
			src = ask.Source{Kind: ask.SourceCommit, CommitID: r.commitID, Repo: r.repo, SHA: r.sha, Reason: r.reason, Hop: r.hop}
		}
		out = append(out, src)
	}
	return out, nil
}

// Sources reads an answer's basis back, chunks ordered by hop, then commits
// newest first. A source is read from git at the commit it was read at, so a
// re-index since the answer changes nothing; one git can no longer produce —
// a purged repository, a replaced snapshot, a file the index now skips — is
// silently omitted. A message that does not belong to a thread owned by
// subject yields an empty slice, the same shape as "no sources yet", never
// another user's evidence.
//
// Sources also reports total: how many sources the record holds for this
// message, scoped by the same ownership check. The caller decides what an
// incomplete set means (Sources itself does not know), but it can only
// decide correctly by comparing len(returned) against total — a basis short
// by SOME of its evidence looks identical to a whole one if only the
// resolved slice is visible.
func (s *Store) Sources(ctx context.Context, subject string, messageID int64) (sources []ask.Source, total int, err error) {
	recs, err := s.records(ctx, subject, messageID)
	if err != nil {
		return nil, 0, err
	}

	// Deliberately NOT filtered on enabled, unlike every retrieval and
	// routing query. This is the RECORD: a turn answered before its
	// repository was parked cites it, and a thread is never rewritten.
	// Parking stops NEW answers, it does not revise old ones.
	files := map[string]fileRead{}
	out := []ask.Source{}
	var commits []ask.Source
	for _, r := range recs {
		var src ask.Source
		var ok bool
		if r.commitID != 0 {
			src, ok, err = s.commitSource(ctx, r)
			if ok {
				commits = append(commits, src)
			}
		} else {
			src, ok, err = s.chunkSource(ctx, r, files)
			if ok {
				out = append(out, src)
			}
		}
		if err != nil {
			return nil, 0, err
		}
	}
	sort.SliceStable(commits, func(i, j int) bool { return commits[i].CommittedAt.After(commits[j].CommittedAt) })
	return append(out, commits...), len(recs), nil
}

// fileRead is one file read from git, kept for the other windows of it.
type fileRead struct {
	branch string
	lines  []string
	err    error
}

// chunkSource resolves a file source: from git at its commit, else from the
// chunk it came from while that row still exists — a chunk id is never
// reused, so a row that still joins holds the text it held.
func (s *Store) chunkSource(ctx context.Context, r recorded, files map[string]fileRead) (ask.Source, bool, error) {
	if text, branch, ok := s.window(ctx, r, files); ok {
		return ask.Source{
			ChunkID: r.chunkID, Repo: r.repo, Branch: branch, Path: r.path, SHA: r.sha, Symbol: r.symbol,
			StartLine: r.start, EndLine: r.end, Text: text, Reason: r.reason, Hop: r.hop,
		}, true, nil
	}
	src := ask.Source{ChunkID: r.chunkID, Reason: r.reason, Hop: r.hop}
	err := s.db.QueryRowContext(ctx, `
		SELECT f.repo, rs.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		JOIN repo_state rs ON rs.name = f.repo
		WHERE c.id = ?`, r.chunkID).Scan(&src.Repo, &src.Branch, &src.Path, &src.SHA, &src.Symbol, &src.StartLine, &src.EndLine, &src.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return ask.Source{}, false, nil
	}
	if err != nil {
		return ask.Source{}, false, fmt.Errorf("read source chunk %d: %w", r.chunkID, err)
	}
	return src, true, nil
}

// window is r's lines read from git at its commit, or false: no identity,
// no checkout, git cannot produce the file, or the window is larger than a
// chunk can be — an overlong line split into siblings, which only the
// chunk rows hold part by part.
func (s *Store) window(ctx context.Context, r recorded, files map[string]fileRead) (string, string, bool) {
	if r.repo == "" || s.evidence == nil {
		return "", "", false
	}
	k := r.repo + "|" + r.path + "|" + r.sha
	f, ok := files[k]
	if !ok {
		file, err := s.evidence.ReadRecorded(ctx, r.repo, r.path, r.sha)
		f = fileRead{branch: file.Branch, err: err}
		if err == nil {
			// Split the way the chunker splits, so a window is the lines
			// it was cut from.
			f.lines = strings.Split(strings.TrimSuffix(file.Content, "\n"), "\n")
		} else {
			// Warn, not Debug: the basis is meant to be re-read from git, and
			// chunkSource falling back to the chunk row must not pass unseen.
			slog.Warn("source not readable from git, falling back to the chunk rows", "repo", r.repo, "path", r.path, "sha", r.sha, "err", err)
		}
		files[k] = f
	}
	if f.err != nil || r.start < 1 || r.end < r.start || r.end > len(f.lines) {
		return "", "", false
	}
	text := strings.Join(f.lines[r.start-1:r.end], "\n")
	if chunkCeiling.Splits(text) {
		return "", "", false
	}
	return text, f.branch, true
}

// commitSource resolves a commit source: from the commit lane while it still
// holds the commit, else from git — the lane's window slides with every push.
func (s *Store) commitSource(ctx context.Context, r recorded) (ask.Source, bool, error) {
	src := ask.Source{Kind: ask.SourceCommit, CommitID: r.commitID, Reason: r.reason, Hop: r.hop}
	var at, paths string
	q, args := `
		SELECT c.repo, rs.branch, c.sha, c.committed_at, c.subject, c.body, c.paths
		FROM commits c JOIN repo_state rs ON rs.name = c.repo
		WHERE c.id = ?`, []any{r.commitID}
	if r.repo != "" {
		q, args = `
		SELECT c.repo, rs.branch, c.sha, c.committed_at, c.subject, c.body, c.paths
		FROM commits c JOIN repo_state rs ON rs.name = c.repo
		WHERE c.repo = ? AND c.sha = ?`, []any{r.repo, r.sha}
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&src.Repo, &src.Branch, &src.SHA, &at, &src.Subject, &src.Text, &paths)
	switch {
	case err == nil:
		src.CommittedAt, _ = time.Parse(time.RFC3339, at)
		if paths != "" {
			src.Paths = strings.Split(paths, "\n")
		}
		return src, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return ask.Source{}, false, fmt.Errorf("read commit source %d: %w", r.commitID, err)
	case r.repo == "" || s.evidence == nil:
		return ask.Source{}, false, nil
	}
	c, err := s.evidence.RecordedCommit(ctx, r.repo, r.sha)
	if err != nil {
		slog.Debug("commit not readable from git", "repo", r.repo, "sha", r.sha, "err", err)
		return ask.Source{}, false, nil
	}
	src.Repo, src.Branch, src.SHA, src.Subject, src.Text = c.Repo, c.Branch, c.SHA, c.Subject, c.Body
	src.CommittedAt, _ = time.Parse(time.RFC3339, c.CommittedAt)
	src.CommittedAt = src.CommittedAt.UTC()
	for _, f := range c.Files {
		src.Paths = append(src.Paths, f.Path)
	}
	return src, true, nil
}
