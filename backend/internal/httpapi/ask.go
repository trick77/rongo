package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

// turnFailed is what a failed turn says, in the stream AND in the stored
// record. The underlying error may quote an upstream response body, and the
// thread history is served back to the browser too — a generic message in one
// place and the raw text in the other would leak it through the other door.
const turnFailed = "Der Zug ist fehlgeschlagen."

// errNotYours separates "this thread is not yours" from "the query failed".
// Collapsing them turns a locked database into a 403 and hands its text to the
// browser.
var errNotYours = errors.New("thread does not belong to this user")

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
	if errors.Is(err, errNotYours) {
		// Refused, not explained: whether the id exists at all is not something
		// to confirm to someone who does not own it.
		http.Error(w, "no such thread", http.StatusForbidden)
		return
	}
	if err != nil {
		// A locked database is not a permissions problem, and its text is not
		// for the browser — the same rule the error event below follows.
		slog.Error("resolve thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	// The title is written alongside the answer and never in front of it. It is
	// a label; the answer must not wait for it, and a title that never arrives
	// is not a failure anyone needs to see.
	if s.deps.Titler != nil && thread.Title != "" && msg.Ordinal == 0 {
		go func(id int64, question string) {
			bg := context.WithoutCancel(ctx)
			if title := s.deps.Titler(bg, question); title != "" {
				if err := s.deps.Threads.SetTitle(bg, id, title); err != nil {
					slog.Warn("set thread title failed", "err", err)
				}
			}
		}(thread.ID, req.Question)
	}

	// The record is written on a context that outlives the request. A reader
	// who closes the tab mid-answer cancels r.Context(), and writing the
	// outcome on it would leave a row with neither an answer nor an error —
	// indistinguishable from a turn still in flight.
	record := context.WithoutCancel(ctx)

	answer, err := s.deps.Ask.Run(ctx, req.Question, audience, ask.Events{
		OnStatus: func(step string) { send("status", map[string]any{"step": step}) },
		OnToken:  func(tok string) { send("token", map[string]any{"text": tok}) },
	})
	if err != nil {
		slog.Error("turn failed", "err", err)
		if ferr := s.deps.Threads.Fail(record, msg.ID, turnFailed); ferr != nil {
			slog.Error("record turn failure failed", "err", ferr)
		}
		// A generic message: the error may quote an upstream body, and that is
		// not something to hand a browser.
		send("error", map[string]any{"message": turnFailed})
		return
	}

	if err := s.deps.Threads.Finish(record, msg.ID, answer.Text, answer.Citations); err != nil {
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
		return threads.Thread{}, errNotYours
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
