package llm

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Session ids mirror the shape the upstream issues, e.g.
//
//	ses_ 0367809bfffe ejtHKm95o6rU4mQ
//	│    └─12 hex────┘ └─14 base62───┘
//	│    timestamp+counter   random
//	prefix
//
// The 12 hex digits are the bitwise inversion of (millis << 12 | counter),
// truncated to 48 bits and written big-endian — a 12-bit per-process counter
// keeps ids minted in the same millisecond distinct, and the inversion is what
// gives upstream ids their characteristic trailing f's.
const (
	sessionIDPrefix   = "ses_"
	sessionIDRandomLn = 14
	sessionIDAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

// sessionCacheLimit bounds the thread→session map. Ids only matter while a
// conversation is live, so the oldest entries are dropped once the cache is full
// rather than letting a long-running process accumulate one entry per thread it
// has ever seen.
const sessionCacheLimit = 4096

var sessionCounter atomic.Uint64

// processSessionID is the fallback affinity id for model calls made outside a
// turn — the HTTP handlers attach the thread before any model call, so the gates
// and the title call are NOT in this lane. It is minted once per backend
// process, so a threadless caller still pins to a single upstream node for the
// process lifetime.
var processSessionID = newSessionID()

var sessionCache = struct {
	sync.Mutex
	byThread map[string]string
	order    []string
}{byThread: map[string]string{}}

type threadIDKey struct{}

// WithThreadID marks ctx as belonging to one rongo thread. Every model call made
// under that ctx sends the same session id, which is what pins the conversation
// to one upstream node. Callers attach it once per turn, where the thread is
// resolved; id 0 means "no thread" and leaves ctx untouched.
func WithThreadID(ctx context.Context, id int64) context.Context {
	if id == 0 {
		return ctx
	}
	return context.WithValue(ctx, threadIDKey{}, strconv.FormatInt(id, 10))
}

// threadIDFromContext returns the thread id attached to ctx, or "" when the call
// carries none.
func threadIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(threadIDKey{}).(string)
	return id
}

// ThreadID is threadIDFromContext for callers outside this package: the thread a
// turn belongs to, or "" for a call made outside one. It exists so a log line
// written elsewhere in the turn can carry the same id the affinity header does,
// which is what makes a routing decision and the upstream calls it made line up
// in the log.
func ThreadID(ctx context.Context) string {
	return threadIDFromContext(ctx)
}

// chatSessionID returns the session/affinity id to send for a turn. Every
// request in one conversation reuses the same id — that is the point of the
// affinity header — so ids are minted once per thread and cached until eviction.
// Eviction is insertion-order FIFO, not LRU: a thread whose id was minted more
// than sessionCacheLimit threads ago re-mints on its next turn and moves to
// another node. Threads are cheap to re-pin and 4096 of them is a lot of
// conversation, so the simpler bound wins over tracking recency. Turns without a
// thread id fall back to the per-process id.
func chatSessionID(threadID string) string {
	if threadID == "" {
		return processSessionID
	}
	sessionCache.Lock()
	defer sessionCache.Unlock()
	if id, ok := sessionCache.byThread[threadID]; ok {
		return id
	}
	id := newSessionID()
	if len(sessionCache.order) >= sessionCacheLimit {
		delete(sessionCache.byThread, sessionCache.order[0])
		sessionCache.order = sessionCache.order[1:]
	}
	sessionCache.byThread[threadID] = id
	sessionCache.order = append(sessionCache.order, threadID)
	return id
}

// newSessionID mints "ses_" + 12 hex (timestamp+counter) + 14 base62 random.
func newSessionID() string {
	millis := uint64(time.Now().UnixMilli())
	counter := sessionCounter.Add(1) & 0xFFF // 12 bits
	stamp := ^(millis<<12 | counter) & 0xFFFFFFFFFFFF
	return fmt.Sprintf("%s%012x%s", sessionIDPrefix, stamp, randomBase62(sessionIDRandomLn))
}

// randomBase62 draws n characters from the base62 alphabet. Rejection sampling
// keeps the draw unbiased; if the system entropy source fails the id degrades to
// the alphabet's first character rather than failing a model call, since this is
// an opaque routing token and not a secret.
func randomBase62(n int) string {
	const limit = 256 - (256 % len(sessionIDAlphabet)) // largest unbiased byte range
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			for len(out) < n {
				out = append(out, sessionIDAlphabet[0])
			}
			break
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, sessionIDAlphabet[int(b)%len(sessionIDAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}
