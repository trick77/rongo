package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/trick77/rongo/internal/repos"
)

// Reader reads files of ONE checkout through one long-lived
// `git cat-file --batch`. ReadFile starts a git process per file, which is
// what an index run spends a third of its time outside embedding on:
// measured over this repository, 6.4 ms a file against 0.1 ms through the
// batch.
//
// For a file it returns the bytes ReadFile returns. A path that is not a file
// at that commit — a directory, a submodule pointer — is refused, where
// `git show` would print a listing.
//
// It is for a run that reads many files and then ends: the process holds the
// checkout's object store open, so a Reader is closed when the run is over
// and never kept. The context given to NewReader bounds every read: ending it
// kills the process, and a read's own context is only checked before the read
// starts.
//
// Safe for concurrent use; reads are answered one at a time.
type Reader struct {
	c    *Client
	ctx  context.Context
	dir  string
	name string

	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr lockedBuffer
	closed bool
	// broken is set once the stream can no longer be trusted to be in step
	// with the requests: a failed write, a short read, a header that does not
	// parse. Every later read then fails rather than returning another
	// file's bytes under this file's name.
	broken error
}

// ErrReaderBroken marks a read that failed because the batch process is gone
// or its stream is out of step — not because of the file asked for. The
// reader will not read again; the caller may read the file another way.
var ErrReaderBroken = errors.New("git batch reader is broken")

// lockedBuffer is git's stderr: written by the goroutine os/exec copies it
// on, read here when a read fails.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// NewReader starts the batch process in spec's checkout. It lives until
// Close, or until ctx ends.
func (c *Client) NewReader(ctx context.Context, spec repos.Spec) (*Reader, error) {
	dir := c.Dir(spec)
	r := &Reader{c: c, ctx: ctx, dir: dir, name: spec.Name}
	// The same command every git call is built from, so the ownership
	// exemption, the no-prompt rule and the auth environment apply here too.
	r.cmd = c.command(ctx, dir, nil, "cat-file", "--batch")
	r.cmd.Stderr = &r.stderr
	in, err := r.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open reader for %s: %w", spec.Name, err)
	}
	out, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open reader for %s: %w", spec.Name, err)
	}
	if err := r.cmd.Start(); err != nil {
		return nil, fmt.Errorf("open reader for %s: %w", spec.Name, err)
	}
	r.in, r.out = in, bufio.NewReaderSize(out, 64<<10)
	return r, nil
}

// ReadFile reads one path at one commit. spec must be the checkout the reader
// was opened on.
func (r *Reader) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	if r.c.Dir(spec) != r.dir {
		return nil, fmt.Errorf("read %s at %s: the reader is open on %s, not %s", path, ShortSHA(sha), r.name, spec.Name)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, ShortSHA(sha), err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("read %s at %s: the reader is closed", path, ShortSHA(sha))
	}
	if r.broken != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, ShortSHA(sha), r.broken)
	}
	// A newline ends a request on the batch's input. Such a path is read the
	// slow way rather than split into two requests, which would leave every
	// later answer one file out of step.
	if strings.Contains(sha+path, "\n") {
		return r.c.ReadFile(ctx, spec, sha, path)
	}
	body, err := r.read(sha + ":" + path)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, ShortSHA(sha), err)
	}
	return body, nil
}

// read sends one request and reads its answer: the header, then for an
// object its bytes and one newline.
func (r *Reader) read(object string) ([]byte, error) {
	if _, err := io.WriteString(r.in, object+"\n"); err != nil {
		return nil, r.fail(fmt.Errorf("ask git: %w", err))
	}
	header, err := r.out.ReadString('\n')
	if err != nil {
		return nil, r.fail(fmt.Errorf("read git's answer: %w", err))
	}
	kind, size, err := parseBatchHeader(strings.TrimSuffix(header, "\n"))
	if errors.Is(err, errNoObject) {
		// Answered in full: the stream is in step and the reader goes on.
		return nil, err
	}
	if err != nil {
		return nil, r.fail(err)
	}
	// The object and the newline git puts after it are consumed whatever the
	// type, so a directory asked for by mistake leaves the stream in step.
	if kind != "blob" {
		if _, err := io.CopyN(io.Discard, r.out, size+1); err != nil {
			return nil, r.fail(fmt.Errorf("skip a %s: %w", kind, err))
		}
		return nil, fmt.Errorf("not a file at that commit: it is a %s", kind)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r.out, body); err != nil {
		return nil, r.fail(fmt.Errorf("read %d bytes: %w", size, err))
	}
	if nl, err := r.out.ReadByte(); err != nil || nl != '\n' {
		return nil, r.fail(errors.New("git's answer did not end where its size said"))
	}
	return body, nil
}

// fail marks the stream unusable and reports why. A run that was cancelled
// says so: the process was killed under the read, and "EOF" would file an
// ordinary shutdown as a repository that cannot be read.
func (r *Reader) fail(err error) error {
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		r.broken = fmt.Errorf("%w: %w", ErrReaderBroken, ctxErr)
		return r.broken
	}
	// What git said, as far as it has arrived: its stderr is complete only
	// once the process has been waited for.
	if msg := strings.TrimSpace(redact(r.stderr.String())); msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	r.broken = fmt.Errorf("%w: %w", ErrReaderBroken, err)
	return r.broken
}

// Close ends the process. Closing twice is not an error.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	// Closing stdin is how cat-file is told there is nothing more to read.
	_ = r.in.Close()
	// A stream that broke mid-answer may have git blocked writing the rest of
	// an object nobody will read; it never sees the end of its input, and
	// waiting for it would hang the run's last step for good.
	if r.broken != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	// A process that was killed, or died on a broken checkout, reports a
	// failure here; the reads already said what went wrong, and a run that
	// read everything it needed has nothing to add.
	_ = r.cmd.Wait()
	return nil
}
