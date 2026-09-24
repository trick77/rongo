package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// shutdownLimit is how long a SIGTERM waits for the answers being streamed.
// compose.yaml's stop_grace_period is longer, so the container is never
// killed while this is still draining.
const shutdownLimit = 20 * time.Second

// shutdown stops everything in the order that leaves nothing behind: the
// poller first, so no fetch or index transaction starts once the end is
// known; then the HTTP server, which waits for the answers still streaming;
// then the workers, so a git command or a half-written transaction is seen
// out rather than abandoned by os.Exit.
//
// An answer can stream for minutes, so the wait is bounded. Past the limit
// the streams are cancelled through the server's base context — the reader
// gets a cut answer instead of a dead connection — and the server is asked
// once more, briefly. cut reports that this happened; err is the drain's
// own failure, never the timeout that was handled.
func shutdown(server *http.Server, cancelHandlers context.CancelFunc, stopPolling func(),
	workers *sync.WaitGroup, limit time.Duration) (cut bool, err error) {

	stopPolling()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if err = server.Shutdown(ctx); err != nil {
		cut = true
		cancelHandlers()
		grace, cancelGrace := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelGrace()
		err = server.Shutdown(grace)
	}
	workers.Wait()
	return cut, err
}
