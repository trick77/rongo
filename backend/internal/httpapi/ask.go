package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

// Asker runs one question end to end. An interface so the HTTP layer can be
// tested without a model endpoint.
type Asker interface {
	Run(ctx context.Context, question string, audience ask.Audience, ev ask.Events) (ask.Answer, error)
}

type askRequest struct {
	ThreadID int64  `json:"thread_id"`
	Question string `json:"question"`
	Audience string `json:"audience"`
}

// handleAsk answers a question over SSE.
//
// This is the only streaming route in rongo. Everything the pipeline does
// before the answer is an ordinary internal call, which is why a failure there
// arrives as one error event rather than as a half-written answer the reader
// has already started believing.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ask == nil || s.deps.Threads == nil {
		http.Error(w, "the question pipeline is unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		http.Error(w, "the question is empty", http.StatusBadRequest)
		return
	}
	audience := ask.AudienceBA
	if req.Audience == string(ask.AudienceDev) {
		audience = ask.AudienceDev
	}

	ctx := r.Context()
	thread, err := s.thread(ctx, u.Subject, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	msg, err := s.deps.Threads.AddQuestion(ctx, thread.ID, string(audience), req.Question)
	if err != nil {
		slog.Error("record question failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Headers before the first flush: once anything is written the status code
	// is fixed, so every failure after this point is an SSE error event, not a
	// 500 the browser could still act on.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(event string, payload any) {
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		_ = rc.Flush()
	}

	send("thread", map[string]any{"thread_id": thread.ID, "title": thread.Title, "message_id": msg.ID})

	answer, err := s.deps.Ask.Run(ctx, req.Question, audience, ask.Events{
		OnStatus: func(step string) { send("status", map[string]any{"step": step}) },
		OnToken:  func(tok string) { send("token", map[string]any{"text": tok}) },
	})
	if err != nil {
		slog.Error("turn failed", "err", err)
		if ferr := s.deps.Threads.Fail(ctx, msg.ID, err.Error()); ferr != nil {
			slog.Error("record turn failure failed", "err", ferr)
		}
		// A generic message: the error may quote an upstream body, and that is
		// not something to hand a browser.
		send("error", map[string]any{"message": "Der Zug ist fehlgeschlagen."})
		return
	}

	if err := s.deps.Threads.Finish(ctx, msg.ID, answer.Text, answer.Citations); err != nil {
		slog.Error("record answer failed", "err", err)
	}
	send("citations", answer.Citations)
	send("usage", answer.Usage)
	send("done", map[string]any{"message_id": msg.ID})
}

// thread returns the thread this turn belongs to, creating one when the request
// names none. An existing thread is checked against its owner: the id comes
// from the browser, and a thread belongs to the person who asked.
func (s *Server) thread(ctx context.Context, subject string, req askRequest) (threads.Thread, error) {
	if req.ThreadID == 0 {
		return s.deps.Threads.Create(ctx, subject, req.Question)
	}
	owns, err := s.deps.Threads.Owns(ctx, subject, req.ThreadID)
	if err != nil {
		return threads.Thread{}, err
	}
	if !owns {
		return threads.Thread{}, fmt.Errorf("no such thread")
	}
	return threads.Thread{ID: req.ThreadID}, nil
}

func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	list, err := s.deps.Threads.List(r.Context(), u.Subject)
	if err != nil {
		slog.Error("list threads failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "malformed thread id", http.StatusBadRequest)
		return
	}
	msgs, err := s.deps.Threads.Messages(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("read thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}
