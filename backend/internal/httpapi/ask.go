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
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
)

// Asker runs one question end to end. An interface so the HTTP layer can be
// tested without a model endpoint.
type Asker interface {
	Run(ctx context.Context, question string, audience ask.Audience, lang ask.Language, ev ask.Events) (ask.Answer, *ask.Clarification, error)
	// Resume continues a turn from the hits one clarification candidate was
	// built from — no search, no routing.
	Resume(ctx context.Context, question string, audience ask.Audience, lang ask.Language, hits []retrieve.Hit, ev ask.Events) (ask.Answer, error)
	// Reexplain answers the same question for the other audience from sources
	// a prior turn already gathered, without searching or gathering again.
	Reexplain(ctx context.Context, question string, audience ask.Audience, lang ask.Language, sources []ask.Source, ev ask.Events) (ask.Answer, error)
}

// turnFailed is what a failed turn says, in the stream AND in the stored
// record. The underlying error may quote an upstream response body, and the
// thread history is served back to the browser too — a generic message in one
// place and the raw text in the other would leak it through the other door.
const turnFailed = "The turn failed."

// basisGone is what a re-explain says when the code an answer was written
// from is no longer indexed. Not turnFailed: the pipeline never ran, and
// telling the reader "the turn failed" would claim a failure that
// did not happen — the truth is the material itself is gone.
const basisGone = "The basis of this answer is no longer indexed."

// errNotYours separates "this thread is not yours" from "the query failed".
// Collapsing them turns a locked database into a 403 and hands its text to the
// browser.
var errNotYours = errors.New("thread does not belong to this user")

type askRequest struct {
	ThreadID int64  `json:"thread_id"`
	Question string `json:"question"`
	Audience string `json:"audience"`
	// Language is the language the answer is written in; see ask.ParseLanguage
	// for the allowlist. Absent or unknown means English.
	Language string `json:"language"`
	// ClarificationMessageID and Choice resume a turn that previously ended
	// by asking: the id of the message that carried the clarification card,
	// and the index of the candidate the reader picked.
	ClarificationMessageID int64 `json:"clarification_message_id"`
	Choice                 int   `json:"choice"`
}

// wireCandidate is one entry on the clarification card as the browser sees
// it: no hits (large, and the browser has no use for them) and no
// Understanding (internal reasoning nobody reads).
type wireCandidate struct {
	Idx     int    `json:"idx"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
}

func wireCandidates(cands []ask.Candidate) []wireCandidate {
	out := make([]wireCandidate, len(cands))
	for i, c := range cands {
		out[i] = wireCandidate{Idx: i, Title: c.Title, Summary: c.Summary, Repo: c.Repo, Branch: c.Branch}
	}
	return out
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
	lang := ask.ParseLanguage(req.Language)

	ctx := r.Context()

	// Resuming a clarification is validated in full BEFORE the thread is
	// touched: an out-of-range choice or a foreign clarification must come
	// back as 400/403, decided before the first byte of the SSE stream is
	// written, because after that the status code is fixed.
	var resume *threads.Clarification
	var resumeHits []retrieve.Hit
	if req.ClarificationMessageID != 0 {
		c, err := s.deps.Threads.Clarification(ctx, u.Subject, req.ClarificationMessageID)
		if err != nil {
			slog.Error("resolve clarification failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if c == nil {
			// Refused, not explained: whether the id exists at all is not
			// something to confirm to someone who does not own it.
			http.Error(w, "no such clarification", http.StatusForbidden)
			return
		}
		if req.Choice < 0 || req.Choice >= len(c.Candidates) {
			// Answering from a candidate nobody offered is worse than
			// refusing: it would look like an answer to the question asked.
			http.Error(w, "choice out of range", http.StatusBadRequest)
			return
		}
		_, hits, err := s.deps.Threads.CandidateHits(ctx, u.Subject, c.ID, req.Choice)
		if err != nil {
			slog.Error("resolve candidate hits failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		resume = c
		resumeHits = hits
	}

	var thread threads.Thread
	if resume != nil {
		// A resumed turn continues the thread the clarification was asked
		// in — the reader is still in the same conversation, just answering
		// a question rongo asked.
		thread = threads.Thread{ID: resume.ThreadID}
	} else {
		t, err := s.thread(ctx, u.Subject, req)
		if errors.Is(err, errNotYours) {
			http.Error(w, "no such thread", http.StatusForbidden)
			return
		}
		if err != nil {
			// A locked database is not a permissions problem, and its text is
			// not for the browser — the same rule the error event below
			// follows.
			slog.Error("resolve thread failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		thread = t
	}

	// Every model call this turn makes carries the thread, so the whole
	// conversation pins to one upstream node instead of scattering across the
	// deployment. Attached once here: both the fresh and the resumed path land
	// on the same thread value.
	ctx = llm.WithThreadID(ctx, thread.ID)
	// Every paid call this turn makes lands in one meter, the gates included.
	// Attached after the thread id and before the title goroutine forks off:
	// the title gets a meter of its own below.
	meter := usage.New()
	ctx = usage.WithMeter(ctx, meter)

	msg, err := s.deps.Threads.AddQuestion(ctx, thread.ID, string(audience), string(lang), req.Question)
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
		go func(id, messageID int64, question string) {
			// WithoutCancel keeps the context's values, the turn's meter
			// among them. The title gets its own so it cannot write into a
			// meter that has already been read and stored; its call is
			// recorded against this message when it finishes — after the
			// turn's usage event, so the live pill misses it and the reload
			// shows it. A label written in the background is not worth
			// holding the answer for.
			titleMeter := usage.New()
			bg := usage.WithMeter(context.WithoutCancel(ctx), titleMeter)
			if title := s.deps.Titler(bg, question, lang); title != "" {
				if err := s.deps.Threads.SetTitle(bg, id, title); err != nil {
					slog.Warn("set thread title failed", "err", err)
				}
			}
			if err := s.deps.Threads.SaveUsage(bg, messageID, titleMeter.Calls()); err != nil {
				slog.Error("record title usage failed", "err", err)
			}
		}(thread.ID, msg.ID, req.Question)
	}

	// The record is written on a context that outlives the request. A reader
	// who closes the tab mid-answer cancels r.Context(), and writing the
	// outcome on it would leave a row with neither an answer nor an error —
	// indistinguishable from a turn still in flight.
	record := context.WithoutCancel(ctx)

	events := ask.Events{
		OnStatus: func(step string) { send("status", map[string]any{"step": step}) },
		OnToken:  func(tok string) { send("token", map[string]any{"text": tok}) },
	}

	// closeUsage stores what the turn paid for and tells the browser, on
	// EVERY exit: answered, asked back, found nothing, failed. The gates ran
	// either way. Sent before the event that ends the turn, so the browser
	// has the number whichever way the turn closed. A turn that paid for
	// nothing (the first call never reached the upstream) sends nothing,
	// the same as the stored record shows for it after a reload.
	closeUsage := func() {
		calls := meter.Calls()
		if len(calls) == 0 {
			return
		}
		if err := s.deps.Threads.SaveUsage(record, msg.ID, calls); err != nil {
			slog.Error("record usage failed", "err", err)
		}
		send("usage", s.prices().Report(calls))
	}

	if resume != nil {
		answer, err := s.deps.Ask.Resume(ctx, req.Question, audience, lang, resumeHits, events)
		if err != nil {
			slog.Error("resumed turn failed", "err", err)
			if ferr := s.deps.Threads.Fail(record, msg.ID, turnFailed); ferr != nil {
				slog.Error("record turn failure failed", "err", ferr)
			}
			closeUsage()
			send("error", map[string]any{"message": turnFailed})
			return
		}
		if err := s.deps.Threads.Finish(record, msg.ID, answer.Text, answer.Citations); err != nil {
			slog.Error("record answer failed", "err", err)
		}
		if err := s.deps.Threads.SaveSources(record, msg.ID, answer.Sources); err != nil {
			slog.Error("record sources failed", "err", err)
		}
		if err := s.deps.Threads.LinkChoice(record, u.Subject, msg.ID, resume.ID, req.Choice); err != nil {
			slog.Error("link choice failed", "err", err)
		}
		send("citations", answer.Citations)
		closeUsage()
		send("done", map[string]any{"message_id": msg.ID})
		return
	}

	answer, clar, err := s.deps.Ask.Run(ctx, req.Question, audience, lang, events)
	if err != nil {
		slog.Error("turn failed", "err", err)
		if ferr := s.deps.Threads.Fail(record, msg.ID, turnFailed); ferr != nil {
			slog.Error("record turn failure failed", "err", ferr)
		}
		closeUsage()
		// A generic message: the error may quote an upstream body, and that is
		// not something to hand a browser.
		send("error", map[string]any{"message": turnFailed})
		return
	}
	if clar != nil {
		if _, cerr := s.deps.Threads.Clarify(record, msg.ID, *clar); cerr != nil {
			slog.Error("record clarification failed", "err", cerr)
			// Clarify writes the clarification and its candidates in one
			// transaction precisely so that a card cannot go out with some
			// candidates missing their stored hits. If the write failed, the
			// card must not ship either: sending it anyway would offer
			// choices resuming them cannot honour, and the clarification row
			// is the only thing distinguishing "ended by asking" from "still
			// in flight" — so the turn must be recorded as failed here.
			if ferr := s.deps.Threads.Fail(record, msg.ID, turnFailed); ferr != nil {
				slog.Error("record turn failure failed", "err", ferr)
			}
			closeUsage()
			send("error", map[string]any{"message": turnFailed})
			return
		}
		closeUsage()
		send("clarification", map[string]any{"message_id": msg.ID, "candidates": wireCandidates(clar.Candidates)})
		send("done", map[string]any{"message_id": msg.ID})
		return
	}

	if err := s.deps.Threads.Finish(record, msg.ID, answer.Text, answer.Citations); err != nil {
		slog.Error("record answer failed", "err", err)
	}
	if err := s.deps.Threads.SaveSources(record, msg.ID, answer.Sources); err != nil {
		slog.Error("record sources failed", "err", err)
	}
	send("citations", answer.Citations)
	closeUsage()
	send("done", map[string]any{"message_id": msg.ID})
}

// handleReexplain re-answers a finished turn's question for the other
// audience, from the sources the original turn already gathered — no search,
// no gather, just a second generation over the same evidence. A successful
// re-explain is a NEW turn in the thread, not a rewrite: the thread is a
// record, and an earlier answer may already have been forwarded or pasted
// into a ticket.
func (s *Server) handleReexplain(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ask == nil || s.deps.Threads == nil {
		http.Error(w, "the question pipeline is unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "malformed message id", http.StatusBadRequest)
		return
	}

	var req struct {
		Audience string `json:"audience"`
		// Language is optional: a re-explain inherits the language of the
		// turn it re-answers unless the request says otherwise.
		Language string `json:"language"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	audience := ask.AudienceBA
	if req.Audience == string(ask.AudienceDev) {
		audience = ask.AudienceDev
	}

	ctx := r.Context()
	msg, found, err := s.deps.Threads.Message(ctx, u.Subject, id)
	if err != nil {
		slog.Error("resolve message failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		// Whether the message exists is not something to confirm to someone
		// who does not own it.
		http.Error(w, "no such message", http.StatusForbidden)
		return
	}
	// A re-explain is another turn in the same conversation, so it pins to the
	// same upstream node as the turn it re-answers.
	ctx = llm.WithThreadID(ctx, msg.ThreadID)
	meter := usage.New()
	ctx = usage.WithMeter(ctx, meter)
	lang := ask.ParseLanguage(msg.Language)
	if req.Language != "" {
		lang = ask.ParseLanguage(req.Language)
	}

	sources, total, err := s.deps.Threads.Sources(ctx, u.Subject, id)
	if err != nil {
		slog.Error("resolve sources failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// The vanished-basis path writes nothing: it is decided before the new
	// turn is created, so a re-index that removed the evidence never leaves
	// a message with neither an answer nor an error.
	//
	// This fires whenever the resolved slice is shorter than what
	// message_sources actually holds for this message (a re-index removed
	// SOME chunks, not necessarily all) or when there is nothing to build
	// from at all. Answering from surviving sources when some are missing
	// would be a silent substitution: the same question, answered from
	// different code than the one the reader was shown — exactly the
	// failure mode the invariants forbid, so a partial basis is treated the
	// same as a vanished one.
	if len(sources) == 0 || len(sources) < total {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		body, _ := json.Marshal(map[string]any{"message": basisGone})
		// A vanished basis is its own message, not the generic turnFailed:
		// the pipeline never ran, and the truth is that the code the answer
		// was written from is no longer indexed.
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
		_ = rc.Flush()
		return
	}

	// A successful re-explain is a NEW turn in the thread, not a rewrite: the
	// thread is a record, and an earlier answer may already have been
	// forwarded or pasted into a ticket. from_candidate_idx stays at its
	// default -1 — this turn did not resume a clarification.
	newMsg, err := s.deps.Threads.AddQuestion(ctx, msg.ThreadID, string(audience), string(lang), msg.Question)
	if err != nil {
		slog.Error("record re-explain question failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

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

	// The record is written on a context that outlives the request, the same
	// as every other write in this package.
	record := context.WithoutCancel(ctx)

	answer, err := s.deps.Ask.Reexplain(ctx, msg.Question, audience, lang, sources, ask.Events{
		OnStatus: func(step string) { send("status", map[string]any{"step": step}) },
		OnToken:  func(tok string) { send("token", map[string]any{"text": tok}) },
	})
	// The same rule as handleAsk: what the turn paid for is stored and
	// reported however it ended.
	closeUsage := func() {
		calls := meter.Calls()
		if len(calls) == 0 {
			return
		}
		if err := s.deps.Threads.SaveUsage(record, newMsg.ID, calls); err != nil {
			slog.Error("record usage failed", "err", err)
		}
		send("usage", s.prices().Report(calls))
	}
	if err != nil {
		slog.Error("reexplain failed", "err", err)
		if ferr := s.deps.Threads.Fail(record, newMsg.ID, turnFailed); ferr != nil {
			slog.Error("record turn failure failed", "err", ferr)
		}
		closeUsage()
		send("error", map[string]any{"message": turnFailed})
		return
	}
	if err := s.deps.Threads.Finish(record, newMsg.ID, answer.Text, answer.Citations); err != nil {
		slog.Error("record answer failed", "err", err)
	}
	// The same sources, not answer.Sources: a re-explain answers from exactly
	// what the original turn gathered, so the new turn can itself be
	// re-explained later from that same, unchanged evidence.
	if err := s.deps.Threads.SaveSources(record, newMsg.ID, sources); err != nil {
		slog.Error("record sources failed", "err", err)
	}
	send("citations", answer.Citations)
	closeUsage()
	send("done", map[string]any{"message_id": newMsg.ID})
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
	// Priced here, from the current table, never from a stored figure: the
	// record holds tokens, and the price is configuration that can change.
	// A turn with no calls on record (older than the table, or nothing paid)
	// carries no usage rather than an empty one, so the browser shows nothing
	// instead of a zero.
	for i := range msgs {
		if len(msgs[i].Calls) > 0 {
			report := s.prices().Report(msgs[i].Calls)
			msgs[i].Usage = &report
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}

// prices is the current table, or an empty one when this deployment has
// none: tokens only, never a nil dereference.
func (s *Server) prices() usage.Prices {
	if s.deps.Prices == nil {
		return usage.Prices{}
	}
	return s.deps.Prices.Prices()
}
