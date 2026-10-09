package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
)

// SourceReader serves a cited file out of rongo's own checkout.
// *sourceview.Service satisfies it.
type SourceReader interface {
	Read(ctx context.Context, repo, path, sha string) (sourceview.File, error)
	// ReadRecorded is Read for a file a turn cites: a path the index has
	// dropped since is still served at the cited commit.
	ReadRecorded(ctx context.Context, repo, path, sha string) (sourceview.File, error)
}

// handleSource answers GET /api/source?repo=&path=&sha= with the file a
// citation points at, read at the cited commit. It is what makes a source in
// the evidence panel something a reader can open rather than only read about.
//
// The route takes any triple, so a file the index no longer lists is read
// from the record only when the reader's own threads cite it. Without that
// rule a signed-in reader could open a secret manifest at a commit from
// before the index ran.
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, path, sha := q.Get("repo"), q.Get("path"), q.Get("sha")
	cited := s.ownerCited(r, func(ctx context.Context, subject string) (bool, error) {
		return s.deps.Threads.CitedBy(ctx, subject, repo, path, sha)
	})
	s.serveSource(w, r, repo, path, sha, cited)
}

// citedFunc reports whether a turn the caller may see cites the triple being
// served. Nil means nothing does.
type citedFunc func(ctx context.Context) (bool, error)

// alwaysCited is the share route's: SharedCitation authorised the triple
// before it got here.
func alwaysCited(context.Context) (bool, error) { return true, nil }

// ownerCited is the signed-in route's citedFunc: the reader's own threads,
// asked through check. Nil when there is no reader or no record to ask.
func (s *Server) ownerCited(r *http.Request, check func(ctx context.Context, subject string) (bool, error)) citedFunc {
	u, ok := auth.UserFrom(r.Context())
	if !ok || s.deps.Threads == nil {
		return nil
	}
	return func(ctx context.Context) (bool, error) { return check(ctx, u.Subject) }
}

// serveSource reads one file and writes it, or writes the reason it cannot.
// Both the signed-in route above and the share-scoped one in share.go end
// here, so a reader following a citation gets the same file and the same
// message whichever door they came through.
//
// A file the index no longer lists — renamed or deleted since the answer — is
// read from the record at its commit when cited says a visible turn cites
// it. The record is asked only on that miss: the owner's question is a join
// over every citation, and nearly every click is a file still indexed.
func (s *Server) serveSource(w http.ResponseWriter, r *http.Request, repo, path, sha string, cited citedFunc) {
	if s.deps.Source == nil {
		http.Error(w, "source view unavailable", http.StatusServiceUnavailable)
		return
	}
	serveCited(w, r, cited, citedView[sourceview.File]{
		read: func(ctx context.Context) (sourceview.File, error) {
			return s.deps.Source.Read(ctx, repo, path, sha)
		},
		readRecorded: func(ctx context.Context) (sourceview.File, error) {
			return s.deps.Source.ReadRecorded(ctx, repo, path, sha)
		},
		fromRecord:  func(err error) bool { return errors.Is(err, sourceview.ErrNotIndexed) && sha != "" },
		citationLog: "read citation failed",
		readLog:     "read source failed",
		statuses: []errStatus{
			{err: sourceview.ErrInvalid, status: http.StatusBadRequest, body: "malformed source request"},
			// The detail (unknown repository, path gone at that commit, no
			// checkout) is for the log. The reader learns that rongo cannot
			// show the file.
			{err: sourceview.ErrNotFound, status: http.StatusNotFound, body: "This file is not in Rongo's checkout at the cited commit.",
				log: func(err error) { slog.Info("source not found", "repo", repo, "path", path, "sha", sha, "err", err) }},
			{err: sourceview.ErrBinary, status: http.StatusUnsupportedMediaType, body: "This is a binary file; there are no lines to show."},
			{err: sourceview.ErrTooLarge, status: http.StatusRequestEntityTooLarge, body: "This file is too large to show here."},
		},
	})
}

// citedView is what serveCited needs to serve one kind of citation: how to
// read it, how to read the recorded copy, when the record is asked, and what
// each way of failing answers.
type citedView[T any] struct {
	read, readRecorded func(ctx context.Context) (T, error)
	// fromRecord says which read error is the miss that a citation turns
	// into a read of the record.
	fromRecord func(err error) bool
	// citationLog and readLog name the citation check and the read in the
	// 500 line.
	citationLog, readLog string
	// statuses maps a read error to its answer, first match wins; anything
	// unlisted is a 500.
	statuses []errStatus
}

// errStatus is one row of a citedView's table. log, when set, reports the
// error before the answer goes out.
type errStatus struct {
	err    error
	status int
	body   string
	log    func(err error)
}

// serveCited reads a cited thing and writes it, or writes the reason it
// cannot. On the miss the view names, a citation of a visible turn reads the
// recorded copy instead; a citation check that fails is a 500, and a miss no
// turn cites goes through the table like any other error.
func serveCited[T any](w http.ResponseWriter, r *http.Request, cited citedFunc, v citedView[T]) {
	out, err := v.read(r.Context())
	if err != nil && v.fromRecord(err) && cited != nil {
		ok, cerr := cited(r.Context())
		if cerr != nil {
			serverError(w, v.citationLog, cerr)
			return
		}
		if ok {
			out, err = v.readRecorded(r.Context())
		}
	}
	if err != nil {
		for _, st := range v.statuses {
			if !errors.Is(err, st.err) {
				continue
			}
			if st.log != nil {
				st.log(err)
			}
			http.Error(w, st.body, st.status)
			return
		}
		serverError(w, v.readLog, err)
		return
	}
	writeJSON(w, out)
}
