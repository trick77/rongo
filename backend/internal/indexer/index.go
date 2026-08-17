package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/symbols"
)

// GitClient is the part of gitrepo.Client the pipeline uses.
type GitClient interface {
	ListPaths(ctx context.Context, spec repos.Spec, sha string) ([]string, error)
	ChangedEntries(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]gitrepo.Change, error)
	ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error)
}

// SymbolExtractor is symbols.Extractor, as an interface so a test can make one
// file fail without a broken ctags on the machine.
type SymbolExtractor interface {
	Extract(ctx context.Context, path string, body []byte) ([]symbols.Symbol, error)
}

// Embedder turns texts into vectors, aligned to input order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// VectorCache is the content-hash embedding cache.
type VectorCache interface {
	Get(ctx context.Context, hashes []string) (map[string][]float32, error)
	Put(ctx context.Context, hash string, vec []float32) error
}

// Deps are the pipeline's collaborators.
type Deps struct {
	DB       *sql.DB
	Git      GitClient
	Symbols  SymbolExtractor
	Embedder Embedder
	Cache    VectorCache
	Writer   *Writer
	Selector *Selector
	Chunk    ChunkOptions
	Logger   *slog.Logger
}

// Indexer runs the indexing pipeline for one repository at one commit.
type Indexer struct {
	db       *sql.DB
	git      GitClient
	symbols  SymbolExtractor
	embedder Embedder
	cache    VectorCache
	writer   *Writer
	selector *Selector
	chunk    ChunkOptions
	log      *slog.Logger
}

// New builds an Indexer, filling in the defaults.
func New(d Deps) *Indexer {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Selector == nil {
		d.Selector = NewSelector(DefaultSelectOptions())
	}
	// Field by field, not struct by struct: replacing the whole struct would
	// discard StripComments for a caller who set only that, and the cost of
	// noticing is a full re-embed of the corpus.
	if def := DefaultChunkOptions(); d.Chunk.TargetTokens <= 0 {
		d.Chunk.TargetTokens = def.TargetTokens
		if d.Chunk.MaxTokens <= 0 {
			d.Chunk.MaxTokens = def.MaxTokens
		}
		if d.Chunk.OverlapTokens <= 0 {
			d.Chunk.OverlapTokens = def.OverlapTokens
		}
	}
	return &Indexer{
		db: d.DB, git: d.Git, symbols: d.Symbols, embedder: d.Embedder,
		cache: d.Cache, writer: d.Writer, selector: d.Selector,
		chunk: d.Chunk, log: d.Logger,
	}
}

// IndexRepo indexes one repository at sha. paths is nil for a full index and
// carries the changed paths for an incremental one.
//
// The returned Counts are the repository's TOTALS after the run, not this run's
// delta: they go straight onto repo_state, where the Repos page reads them as
// the size of the whole index. An incremental run reporting "2 files" would
// read as a corpus that had shrunk to two files.
func (ix *Indexer) IndexRepo(ctx context.Context, st RepoState, sha string, paths []string) (Counts, error) {
	spec := repos.Spec{Name: st.Name, CloneURL: st.CloneURL, Branch: st.Branch, Enabled: true}

	targets, err := ix.targets(ctx, spec, st, sha, paths)
	if err != nil {
		return Counts{}, err
	}

	for _, tg := range targets {
		if ctx.Err() != nil {
			return Counts{}, ctx.Err()
		}
		if tg.deleted {
			if err := ix.writer.DeleteFile(ctx, st.Name, tg.path); err != nil {
				return Counts{}, err
			}
			continue
		}
		if err := ix.indexOne(ctx, spec, st, sha, tg.path); err != nil {
			return Counts{}, err
		}
	}
	return ix.totals(ctx, st.Name)
}

// target is one path this run has to deal with, and whether it is gone.
type target struct {
	path    string
	deleted bool
}

// targets resolves what to work on. A full run lists the tree; an incremental
// one re-reads the diff's STATUS, because the poller hands over path names
// only and --name-only cannot say whether a path was modified or removed.
// Guessing from a failed read would conflate a broken checkout with deleted
// code and silently drop files that still exist.
func (ix *Indexer) targets(ctx context.Context, spec repos.Spec, st RepoState, sha string, paths []string) ([]target, error) {
	if paths == nil {
		all, err := ix.git.ListPaths(ctx, spec, sha)
		if err != nil {
			return nil, err
		}
		out := make([]target, 0, len(all))
		for _, p := range all {
			out = append(out, target{path: p})
		}
		return out, nil
	}
	deleted := map[string]bool{}
	if st.LastSHA != "" {
		changes, err := ix.git.ChangedEntries(ctx, spec, st.LastSHA, sha)
		if err != nil {
			return nil, err
		}
		for _, c := range changes {
			deleted[c.Path] = c.Deleted
		}
	}
	out := make([]target, 0, len(paths))
	for _, p := range paths {
		out = append(out, target{path: p, deleted: deleted[p]})
	}
	return out, nil
}

// indexOne runs one file through the pipeline: read, select, symbols, chunk,
// cache, embed, write.
func (ix *Indexer) indexOne(ctx context.Context, spec repos.Spec, st RepoState, sha, path string) error {
	body, err := ix.git.ReadFile(ctx, spec, sha, path)
	if err != nil {
		// One unreadable path (a submodule pointer, a broken symlink) must not
		// fail the repository. It is logged rather than recorded, because
		// nothing about it is known well enough to record.
		ix.log.Warn("file unreadable at this commit; skipping", "repo", st.Name, "path", path, "err", err)
		return nil
	}
	lang := LanguageOf(path)
	decision, detail := ix.selector.Select(path, body)
	if decision != Include {
		// The stored reason is the DECISION, which the answer layer renders;
		// the detail (which vendored directory, which secret pattern) is a
		// diagnostic and stays in the log, never in a row a user reads.
		ix.log.Debug("file not indexed", "repo", st.Name, "path", path,
			"reason", string(decision), "detail", detail)
		return ix.writer.RecordSkipped(ctx, st.Name, path, sha, lang, string(decision), len(body))
	}

	syms, err := ix.symbols.Extract(ctx, path, body)
	if err != nil {
		// ctags failing on one file is a degradation, not a failure: the
		// chunker falls back to line windows and the file stays searchable.
		ix.log.Warn("symbol extraction failed; falling back to line windows",
			"repo", st.Name, "path", path, "err", err)
		syms = nil
	}

	chunks := ChunkFile(st.Name, st.Branch, path, body, syms, ix.chunk)
	if len(chunks) == 0 {
		// An empty or blank file. It is recorded with a REASON rather than with
		// an empty one: a file row carrying no skip reason and no chunks would
		// be indistinguishable from a file the pipeline silently failed on, and
		// "this file exists but was not indexed" has to say why.
		return ix.writer.RecordSkipped(ctx, st.Name, path, sha, lang, string(SkipEmpty), len(body))
	}

	vecs, err := ix.vectors(ctx, chunks)
	if err != nil {
		// Unlike a ctags failure this is fatal for the run. Writing the rest
		// would mark the repository indexed while part of it had silently never
		// been embedded, and no later run would notice.
		return fmt.Errorf("embed %s/%s: %w", st.Name, path, err)
	}
	return ix.writer.ReplaceFile(ctx, st.Name, path, sha, lang, len(body), chunks, vecs, syms)
}

// vectors resolves one file's chunks to vectors, embedding only the misses.
//
// The cache lookup is what makes a dev re-index bearable and a production
// diff-reindex cheap: unchanged content is never embedded twice, and the misses
// travel in ONE batched call rather than one call per chunk.
func (ix *Indexer) vectors(ctx context.Context, chunks []Chunk) ([][]float32, error) {
	hashes := make([]string, len(chunks))
	for i, c := range chunks {
		hashes[i] = c.ContentHash
	}
	cached, err := ix.cache.Get(ctx, hashes)
	if err != nil {
		return nil, err
	}

	var missTexts []string
	var missHashes []string
	seen := map[string]bool{}
	for _, c := range chunks {
		if _, ok := cached[c.ContentHash]; ok || seen[c.ContentHash] {
			continue
		}
		// A file can hold two identical chunks (two empty methods). Embedding
		// the text once and reusing it keeps the batch honest about its size.
		seen[c.ContentHash] = true
		missTexts = append(missTexts, c.Text)
		missHashes = append(missHashes, c.ContentHash)
	}

	if len(missTexts) > 0 {
		fresh, err := ix.embedder.Embed(ctx, missTexts)
		if err != nil {
			return nil, err
		}
		if len(fresh) != len(missTexts) {
			return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(fresh), len(missTexts))
		}
		for i, h := range missHashes {
			cached[h] = fresh[i]
			if err := ix.cache.Put(ctx, h, fresh[i]); err != nil {
				return nil, err
			}
		}
	}

	out := make([][]float32, len(chunks))
	for i, c := range chunks {
		v, ok := cached[c.ContentHash]
		if !ok {
			return nil, fmt.Errorf("no vector for chunk %d", c.Ordinal)
		}
		out[i] = v
	}
	return out, nil
}

// totals counts what the repository currently holds, for the Repos page.
func (ix *Indexer) totals(ctx context.Context, repo string) (Counts, error) {
	var c Counts
	err := ix.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM files WHERE repo = ?),
		       (SELECT COUNT(*) FROM chunks ch JOIN files f ON f.id = ch.file_id WHERE f.repo = ?)`,
		repo, repo).Scan(&c.Files, &c.Chunks)
	if err != nil {
		return Counts{}, fmt.Errorf("count %s: %w", repo, err)
	}
	return c, nil
}
