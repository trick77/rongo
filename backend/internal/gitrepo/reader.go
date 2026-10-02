package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
// It returns exactly what ReadFile returns for a file. It is for a run that
// reads many files and then ends: the process holds the checkout's object
// store open, so a Reader is closed when the run is over and never kept.
//
// Safe for concurrent use; reads are answered one at a time.
type Reader struct {
	c    *Client
	dir  string
	name string

	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr bytes.Buffer
	closed bool
	// broken is set once the stream can no longer be trusted to be in step
	// with the requests: a failed write, a short read, a header that does not
	// parse. Every later read then fails rather than returning another
	// file's bytes under this file's name.
	broken error
}

// NewReader starts the batch process in spec's checkout. It lives until
// Close, or until ctx ends.
func (c *Client) NewReader(ctx context.Context, spec repos.Spec) (*Reader, error) {
	dir := c.Dir(spec)
	r := &Reader{c: c, dir: dir, name: spec.Name}
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

// ReadFile reads one path at one commit, as Client.ReadFile does. spec must
// be the checkout the reader was opened on.
func (r *Reader) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	if r.c.Dir(spec) != r.dir {
		return nil, fmt.Errorf("read %s at %s: the reader is open on %s, not %s", path, ShortSHA(sha), r.name, spec.Name)
	}
	// A newline ends a request on the batch's input. Such a path is read the
	// slow way rather than split into two requests, which would leave every
	// later answer one file out of step.
	if strings.ContainsAny(sha+path, "\n\r") {
		return r.c.ReadFile(ctx, spec, sha, path)
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
	body, err := r.read(sha + ":" + path)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, ShortSHA(sha), err)
	}
	return body, nil
}

// errNotAFile is a request git answered in full that names no file. The
// stream is still in step, so the reader goes on.
var errNotAFile = errors.New("not a file at that commit")

// read sends one request and reads its answer:
//
//	<oid> <type> <size>\n<size bytes>\n     for an object
//	<request> missing\n                     for anything else
func (r *Reader) read(object string) ([]byte, error) {
	if _, err := io.WriteString(r.in, object+"\n"); err != nil {
		return nil, r.fail(fmt.Errorf("ask git: %w", err))
	}
	header, err := r.out.ReadString('\n')
	if err != nil {
		return nil, r.fail(fmt.Errorf("read git's answer: %w", err))
	}
	header = strings.TrimSuffix(header, "\n")
	// The header's own fields never hold a space; a request might, so the
	// fields are taken from the end of a miss and from the start of a hit.
	if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
		return nil, errNotAFile
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return nil, r.fail(fmt.Errorf("unparseable answer %q", header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return nil, r.fail(fmt.Errorf("unparseable answer %q", header))
	}
	// The object and the newline git puts after it are consumed whatever the
	// type, so a directory asked for by mistake leaves the stream in step.
	if fields[1] != "blob" {
		if _, err := io.CopyN(io.Discard, r.out, size+1); err != nil {
			return nil, r.fail(fmt.Errorf("skip a %s: %w", fields[1], err))
		}
		return nil, fmt.Errorf("%w: it is a %s", errNotAFile, fields[1])
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

// fail marks the stream unusable and reports why, with what git said.
func (r *Reader) fail(err error) error {
	if msg := strings.TrimSpace(redact(r.stderr.String())); msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	r.broken = err
	return err
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
	// A process the context killed, or one that died on a broken checkout,
	// reports a failure here; the reads already said what went wrong, and a
	// run that read everything it needed has nothing to add.
	_ = r.cmd.Wait()
	return nil
}
