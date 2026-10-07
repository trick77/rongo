package httpapi

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/trick77/rongo/internal/sched"
)

// The password login's brake. bcrypt makes one guess slow, not a thousand of
// them: past loginFreeFailures from one address every further attempt waits
// first, loginDelayBase doubling per failure up to loginDelayCap. The wait
// comes BEFORE the check, so a guesser cannot hang up early on a slow answer
// and read it as "wrong". A good login clears the address; a record nobody
// added to for loginFailureTTL is forgotten. In process and per address: a
// restart forgets, and behind a reverse proxy every caller is the proxy's
// address, so one guesser slows every login, which for one admin account is
// the side to err on.
const (
	loginFreeFailures = 5
	loginDelayBase    = time.Second
	loginDelayCap     = 30 * time.Second
	loginFailureTTL   = 15 * time.Minute
)

type loginThrottle struct {
	mu   sync.Mutex
	seen map[string]loginFailures
	// now and sleep are indirect so a test can hold the clock still.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool
}

type loginFailures struct {
	n    int
	last time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{seen: map[string]loginFailures{}, now: time.Now, sleep: sched.Sleep}
}

// remoteHost is the address a login is counted against, without the port:
// every attempt opens a connection of its own.
func remoteHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// wait holds an address with too many failures for its delay. False is a
// caller that went away during it, which gets no verdict.
func (t *loginThrottle) wait(ctx context.Context, addr string) bool {
	t.mu.Lock()
	f, ok := t.seen[addr]
	if ok && t.now().Sub(f.last) >= loginFailureTTL {
		delete(t.seen, addr)
		f = loginFailures{}
	}
	t.mu.Unlock()
	if f.n < loginFreeFailures {
		return true
	}
	d := loginDelayCap
	if shift := f.n - loginFreeFailures; shift < 5 {
		d = min(loginDelayBase<<shift, loginDelayCap)
	}
	return t.sleep(ctx, d)
}

// failed counts one failure against addr. Stale records are dropped on the
// write path, so the map holds the addresses guessing lately and not every
// address that ever mistyped.
func (t *loginThrottle) failed(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for k, f := range t.seen {
		if now.Sub(f.last) >= loginFailureTTL {
			delete(t.seen, k)
		}
	}
	f := t.seen[addr]
	t.seen[addr] = loginFailures{n: f.n + 1, last: now}
}

// succeeded clears addr.
func (t *loginThrottle) succeeded(addr string) {
	t.mu.Lock()
	delete(t.seen, addr)
	t.mu.Unlock()
}
