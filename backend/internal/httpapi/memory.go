package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/memory"
)

// memoryPage is what the Memory page reads: whether the deployment keeps
// memory at all, and the reader's rules when it does.
type memoryPage struct {
	Enabled  bool         `json:"enabled"`
	Memories []memory.Row `json:"memories"`
}

// handleMemory lists the reader's standing instructions, newest first.
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	out := memoryPage{Memories: []memory.Row{}}
	if s.deps.Memory != nil {
		rows, err := s.deps.Memory.List(r.Context(), u.Subject)
		if err != nil {
			slog.Error("list memories failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		out.Enabled, out.Memories = true, rows
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleForgetMemory deletes one rule: the page's ×, and the undo under the
// answer that saved it. Unknown and not-yours are one 404, as with threads.
func (s *Server) handleForgetMemory(w http.ResponseWriter, r *http.Request) {
	if s.deps.Memory == nil {
		http.Error(w, "memory unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "no such memory", http.StatusNotFound)
		return
	}
	found, err := s.deps.Memory.Remove(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("forget memory failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such memory", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// memoryHolder reads the reader's rules for one turn onto the context. A
// deployment without memory attaches nothing, which is how the pipeline
// knows not to ask for a directive. A read that fails is logged and the turn
// runs with no rules rather than not at all: an answer without the reader's
// preferences is worse than one with them and better than no answer.
func (s *Server) memoryHolder(ctx context.Context, subject string) context.Context {
	if s.deps.Memory == nil {
		return ctx
	}
	rows, err := s.deps.Memory.List(ctx, subject)
	if err != nil {
		slog.Error("read memories failed", "err", err)
		rows = nil
	}
	return memory.With(ctx, memory.NewHolder(rows))
}

// wireMemory is the memory event: what the turn saved, what it replaced,
// what the reader forgot, and a scope the index did not carry.
type wireMemory struct {
	ID           int64    `json:"id,omitempty"`
	Text         string   `json:"text,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	Replaced     []string `json:"replaced,omitempty"`
	Removed      []string `json:"removed,omitempty"`
	ScopeDropped string   `json:"scope_dropped,omitempty"`
}

// onMemory is the pipeline's OnMemory for one turn: write the directive under
// the reader, link it to the turn, tell the browser. record outlives the
// request, so a rule the reader gave is kept even when they closed the tab
// before the answer.
func (s *Server) onMemory(record context.Context, subject string, messageID int64, send func(string, any)) func(memory.Directive) (memory.Added, error) {
	if s.deps.Memory == nil {
		return nil
	}
	return func(d memory.Directive) (memory.Added, error) {
		added, err := s.deps.Memory.Add(record, subject, d, messageID)
		if err != nil {
			return memory.Added{}, err
		}
		// A directive that changed nothing (the model named an id the reader
		// deleted already) gets no event: the chip would draw an empty note.
		if added.Row.ID == 0 && len(added.Replaced) == 0 && len(added.Removed) == 0 {
			return added, nil
		}
		if added.Row.ID != 0 {
			if err := s.deps.Threads.SetMemory(record, messageID, added.Row.ID); err != nil {
				recordFailed(record, "record memory link failed", err)
			}
		}
		send("memory", wireMemory{
			ID: added.Row.ID, Text: added.Row.Text, Scope: added.Row.Scope,
			Replaced: added.Replaced, Removed: added.Removed, ScopeDropped: added.ScopeDropped,
		})
		return added, nil
	}
}
