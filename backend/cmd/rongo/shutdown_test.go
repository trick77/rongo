package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// streamingServer is a server with one handler that streams until its
// request context ends, the shape of an answer in flight.
func streamingServer(base context.Context, t *testing.T) (*http.Server, string, chan error) {
	t.Helper()
	ended := make(chan error, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
			ended <- r.Context().Err()
		}),
		BaseContext: func(net.Listener) context.Context { return base },
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String() + "/", ended
}

func TestShutdown_cutsAStreamPastTheLimitAndStillWaitsForTheWorkers(t *testing.T) {
	// Given: an answer streaming with no end in sight, and a poller mid-work.
	handlerCtx, cancelHandlers := context.WithCancel(context.Background())
	srv, url, ended := streamingServer(handlerCtx, t)
	resp, err := http.Get(url) //nolint:noctx // the stream is what the test is about
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	pollCtx, stopPolling := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workerStopped := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-pollCtx.Done()
		time.Sleep(50 * time.Millisecond) // the transaction being rolled back
		close(workerStopped)
	}()

	// When
	cut, err := shutdown(srv, cancelHandlers, stopPolling, &workers, 200*time.Millisecond)

	// Then: the stream was cut rather than the process abandoned, and the
	// workers were seen out before the return.
	if !cut {
		t.Error("cut = false, want the stream past the limit reported as cut")
	}
	if err != nil {
		t.Errorf("shutdown err = %v, want nil once the stream was cut", err)
	}
	select {
	case <-workerStopped:
	default:
		t.Error("shutdown returned before the workers stopped")
	}
	select {
	case e := <-ended:
		if !errors.Is(e, context.Canceled) {
			t.Errorf("the stream ended with %v, want cancellation", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the streaming handler was never cancelled")
	}
}

func TestShutdown_withNothingStreamingIsClean(t *testing.T) {
	handlerCtx, cancelHandlers := context.WithCancel(context.Background())
	srv, _, _ := streamingServer(handlerCtx, t)
	_, stopPolling := context.WithCancel(context.Background())
	var workers sync.WaitGroup

	cut, err := shutdown(srv, cancelHandlers, stopPolling, &workers, time.Second)

	if cut || err != nil {
		t.Errorf("cut, err = %v, %v; want a clean drain", cut, err)
	}
}
