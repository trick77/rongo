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
// them: past loginFreeFailures attempts from one address at one account name,
// every further attempt waits first, loginDelayBase doubling per attempt up
// to loginDelayCap.
//
// An attempt is counted when it STARTS and forgiven only by a good login, so
// parallel guesses each read a higher count instead of all reading the same
// one and sleeping together. The wait comes before the check, so a guesser
// cannot hang up early on a slow answer and read it as "wrong". A record
// nobody added to for loginFailureTTL is forgotten.
//
// In process: a restart forgets. Behind a reverse proxy every caller is the
// proxy's address, so one guesser slows every login at that account name,
// which for one admin account is the side to err on. The map holds at most
// loginMaxTracked keys; past that, new keys share one overflow count, so a
// flood of addresses or names neither grows memory nor escapes the brake.
const (
	loginFreeFailures = 5
	loginDelayBase    = time.Second
	loginDelayCap     = 30 * time.Second
	loginFailureTTL   = 15 * time.Minute
	loginMaxTracked   = 10000
	// loginSweepEvery is how many attempts pass between sweeps of expired
	// records, so a burst does not walk the whole map on every request.
	loginSweepEvery = 256
	// loginOverflow is the key every attempt shares once the map is full.
	loginOverflow = "\x00overflow"
)

type loginThrottle struct {
	mu    sync.Mutex
	seen  map[string]loginAttempts
	calls int
	max   int
	// sweepEvery is loginSweepEvery, a field so a test can sweep at once.
	sweepEvery int
	// now and sleep are indirect so a test can hold the clock still.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool
}

type loginAttempts struct {
	n    int
	last time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{seen: map[string]loginAttempts{}, max: loginMaxTracked, sweepEvery: loginSweepEvery, now: time.Now, sleep: sched.Sleep}
}

// loginKey is what an attempt is counted against: the address without the
// port, since every attempt opens a connection of its own, and the account
// name, so one guesser does not slow a different account.
func loginKey(host, user string) string {
	return host + "\x00" + user
}

// remoteHost is the request's address without the port.
func remoteHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// wait counts one attempt against key and holds it for the delay the
// attempts before it earned. False is a caller that went away during it,
// which gets no verdict.
func (t *loginThrottle) wait(ctx context.Context, key string) bool {
	t.mu.Lock()
	now := t.now()
	t.calls++
	if t.calls%t.sweepEvery == 0 {
		for k, a := range t.seen {
			if now.Sub(a.last) >= loginFailureTTL {
				delete(t.seen, k)
			}
		}
	}
	a, ok := t.seen[key]
	if ok && now.Sub(a.last) >= loginFailureTTL {
		a = loginAttempts{}
	}
	if !ok && len(t.seen) >= t.max {
		key = loginOverflow
		a = t.seen[key]
	}
	t.seen[key] = loginAttempts{n: a.n + 1, last: now}
	t.mu.Unlock()
	if a.n < loginFreeFailures {
		return true
	}
	d := loginDelayCap
	if shift := a.n - loginFreeFailures; shift < 5 {
		d = min(loginDelayBase<<shift, loginDelayCap)
	}
	return t.sleep(ctx, d)
}

// succeeded forgives key's attempts.
func (t *loginThrottle) succeeded(key string) {
	t.mu.Lock()
	delete(t.seen, key)
	t.mu.Unlock()
}
