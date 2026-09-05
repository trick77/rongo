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
	"sync"
	"time"

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
	Resume(ctx context.Context, question string, audience ask.Audience, lang ask.Language, hits []retrieve.Hit, scope ask.Scope, ev ask.Events) (ask.Answer, error)
	// Reexplain answers the same question for the other audience from sources
	// a prior turn already gathered, without searching or gathering again.
	Reexplain(ctx context.Context, question string, audience ask.Audience, lang ask.Language, sources []ask.Source, scope ask.Scope, ev ask.Events) (ask.Answer, error)
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
	// touched: an out-of-range choice, a foreign clarification or a card that
	// was already answered must come back as 400/403/409, decided before the
	// first byte of the SSE stream is written, because after that the status
	// code is fixed.
	var resume *threads.Clarification
	var resumeHits []retrieve.Hit
	var resumeScope ask.Scope
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
		if c.Answered {
			// A card is answered once. The answer it produced is in the
			// thread, and a second one would be a second answer to a question
			// already decided — refused here, before it costs a model call.
			http.Error(w, "this clarification was already answered", http.StatusConflict)
			return
		}
		_, hits, err := s.deps.Threads.CandidateHits(ctx, u.Subject, c.ID, req.Choice)
		if err != nil {
			slog.Error("resolve candidate hits failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		// The scope the card was asked under carries over. Without it a resumed
		// turn re-answers "how do loom and rongo differ" from rongo-only
		// sources with no rule saying loom is not indexed, and the model
		// writes loom's side out of its own training.
		m, ok, err := s.deps.Threads.Message(ctx, u.Subject, req.ClarificationMessageID)
		if err != nil {
			slog.Error("resolve clarification scope failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if ok {
			resumeScope = m.Scope
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
	// The title goroutine below writes to this stream from outside the
	// handler's own goroutine, so the writer is taken under a lock and the
	// stream is shut to further writes the moment the handler returns: a
	// write to a ResponseWriter whose handler has finished is not merely
	// ignored, it races the server's own cleanup.
	var sendMu sync.Mutex
	sendClosed := false
	defer func() {
		sendMu.Lock()
		sendClosed = true
		sendMu.Unlock()
	}()
	send := func(event string, payload any) {
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		_ = rc.Flush()
	}

	send("thread", map[string]any{"thread_id": thread.ID, "title": thread.Title, "message_id": msg.ID})

	// The record is written on a context that outlives the request. A reader
	// who closes the tab mid-answer cancels r.Context(), and writing the
	// outcome on it would leave a row with neither an answer nor an error —
	// indistinguishable from a turn still in flight.
	record := context.WithoutCancel(ctx)

	// The title is written alongside the answer and never in front of it. It is
	// a label; the answer must not wait for it, and a title that never arrives
	// is not a failure anyone needs to see.
	//
	// Only ever on a thread's first turn. A later turn is handed a Thread with
	// no Title on it — s.thread and the resume path build one from the id
	// alone — so anything keyed off thread.Title here would fire on every
	// continuation, and settling one while the first turn's title call is
	// still in flight would put the cut question back in the header, which is
	// the whole thing this exists to prevent.
	if msg.Ordinal == 0 {
		if s.deps.Titler == nil || thread.Title == "" {
			// No title is coming: no titler configured. Settle the row now,
			// or the header waits forever for one and reads "New question"
			// for the rest of the thread's life.
			if err := s.deps.Threads.SetTitle(record, thread.ID, thread.Title, ""); err != nil {
				slog.Warn("settle thread title failed", "err", err)
			}
		} else {
			settled := s.writeTitle(ctx, thread.ID, msg.ID, req.Question, thread.Title, lang, send)
			defer settled()
		}
	}

	events := ask.Events{
		OnStatus: func(step string) { send("status", map[string]any{"step": step}) },
		OnToken:  func(tok string) { send("token", map[string]any{"text": tok}) },
		OnNotice: func(text string) { send("notice", map[string]any{"text": text}) },
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
		send("usage", s.deps.Prices.Prices().Report(calls))
	}

	if resume != nil {
		// The resumed turn is a turn of its own: it says what its scope was
		// and records it, the same way handleReexplain does. Without this the
		// notice stops at the card, and a re-explain of the resumed answer
		// reads an empty scope and drops the rule that keeps the model from
		// writing about a repository the index never had.
		if notice := ask.ScopeNotice(lang, resumeScope); notice != "" {
			send("notice", map[string]any{"text": notice})
		}
		if serr := s.deps.Threads.SetScope(record, msg.ID, resumeScope); serr != nil {
			slog.Error("record scope failed", "err", serr)
		}
		answer, err := s.deps.Ask.Resume(ctx, req.Question, audience, lang, resumeHits, resumeScope, events)
		if err != nil {
			slog.Error("resumed turn failed", "err", err)
			if ferr := s.deps.Threads.Fail(record, msg.ID, turnFailed); ferr != nil {
				slog.Error("record turn failure failed", "err", ferr)
			}
			closeUsage()
			send("error", map[string]any{"message": turnFailed})
			return
		}
		// Written a second time, over the scope stored before the call: the
		// turn only learns after gathering whether it stood on documentation
		// alone, and the row written above is what a failed resume leaves
		// behind. The answer's own scope is the one the record keeps.
		if serr := s.deps.Threads.SetScope(record, msg.ID, answer.Scope); serr != nil {
			slog.Error("record scope failed", "err", serr)
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
		s.finishTurn(ctx, record, msg.ID, req.Question, answer, audience, resumeScope, lang, send, closeUsage)
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
	// Stored whichever way the turn ended, before the ending is sent: a card
	// and an answer both belong to a question that named repositories, and a
	// resumed turn reads the scope back off this row.
	if clar != nil {
		if serr := s.deps.Threads.SetScope(record, msg.ID, clar.Scope); serr != nil {
			slog.Error("record scope failed", "err", serr)
		}
	} else if serr := s.deps.Threads.SetScope(record, msg.ID, answer.Scope); serr != nil {
		slog.Error("record scope failed", "err", serr)
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
	s.finishTurn(ctx, record, msg.ID, req.Question, answer, audience, answer.Scope, lang, send, closeUsage)
}

const (
	// titleCallTimeout bounds the title call itself. The answer's own budget
	// is fifteen minutes, which is the right order for an answer and absurd
	// for a six-word label: a stalled title would hold a goroutine, a meter
	// and a row's pending flag for a quarter of an hour.
	titleCallTimeout = 30 * time.Second
	// titleStreamGrace is how long the finished turn holds its stream open
	// for a title still in flight. A title is started with the turn and takes
	// a second or two, so on a real answer it has landed long before this is
	// reached and the wait costs nothing. What it must never do is keep the
	// composer disabled — the browser only re-enables it when the stream
	// ends — so the wait is short and the title, if it is late, simply
	// reaches the reader on the next list fetch instead.
	titleStreamGrace = 2 * time.Second
	// followupsCallTimeout bounds the follow-up suggestions. Much shorter
	// than the title's budget, because this one is synchronous: the browser
	// re-enables the composer when the STREAM ends, not on the done event, so
	// every second spent here is a second the reader cannot type in front of
	// a finished answer. Two or three one-line questions are a second's work;
	// anything slower is not worth the wait and is dropped.
	followupsCallTimeout = 8 * time.Second
)

// finishTurn ends a turn that produced an answer: the citations, the follow-up
// questions it offers next, what it paid, and the event that closes it. All
// three answering paths - a fresh turn, a resumed clarification and a
// re-explain - end here, so the order they end in cannot drift apart.
//
// ctx is the turn's context, meter and all: the suggestion call is part of
// what the turn cost and is metered with the rest of it. record outlives the
// request, so the row is written even for a reader who closed the tab.
// scope is passed rather than read off the answer: only Run fills Answer.Scope,
// so a resumed or re-explained turn would hand the suggestion prompt an empty
// one and lose the rule that keeps a pill off a repository the index lacks.
// Both callers already have the scope in hand - they record it a few lines up.
func (s *Server) finishTurn(
	ctx, record context.Context,
	messageID int64,
	question string,
	answer ask.Answer,
	audience ask.Audience,
	scope ask.Scope,
	lang ask.Language,
	send func(string, any),
	closeUsage func(),
) {
	send("citations", answer.Citations)
	s.suggestFollowups(ctx, record, messageID, question, answer, audience, scope, lang, send)
	closeUsage()
	send("done", map[string]any{"message_id": messageID})
}

// suggestFollowups offers two or three questions to ask next, under the answer
// that prompted them.
//
// Synchronous, unlike the title: it is written FROM the answer, so it cannot
// start earlier, and running it inline is what puts its tokens in the turn's
// own usage report instead of a meter nobody reads until the next reload. The
// step is announced first, because a wait a person can see is a wait and a
// wait they cannot is a hang.
//
// An answer with no sources is the nothing-found reply. There is nothing to
// follow up on, and suggesting anything there would be inventing a question
// the index cannot answer.
func (s *Server) suggestFollowups(
	ctx, record context.Context,
	messageID int64,
	question string,
	answer ask.Answer,
	audience ask.Audience,
	scope ask.Scope,
	lang ask.Language,
	send func(string, any),
) {
	if s.deps.Suggester == nil || len(answer.Sources) == 0 {
		return
	}
	send("status", map[string]any{"step": "suggesting"})
	call, cancel := context.WithTimeout(ctx, followupsCallTimeout)
	defer cancel()
	qs := s.deps.Suggester(call, question, answer.Text, audience, answer.Sources, scope, lang)
	if len(qs) == 0 {
		return
	}
	if err := s.deps.Threads.SaveFollowups(record, messageID, qs); err != nil {
		// The pills are worth a warning and nothing more: the answer is
		// written and the turn is finished either way.
		slog.Warn("record followups failed", "err", err)
	}
	send("followups", qs)
}

// writeTitle names a thread in the background and returns the wait its caller
// defers. The answer never waits for a title; this is the connection lingering
// a moment after the last word so a title that is nearly there still reaches
// the browser on the stream it belongs to.
func (s *Server) writeTitle(
	ctx context.Context,
	threadID, messageID int64,
	question, placeholder string,
	lang ask.Language,
	send func(string, any),
) func() {
	done := make(chan struct{})
	go func() {
		// Closed last, after the title event has gone out: the caller's wait
		// is what keeps the stream open for it.
		defer close(done)
		// WithoutCancel keeps the context's values, the turn's meter among
		// them. The title gets its own so it cannot write into a meter that
		// has already been read and stored; its call is recorded against this
		// message when it finishes — after the turn's usage event, so the
		// live pill misses it and the reload shows it.
		titleMeter := usage.New()
		bg := usage.WithMeter(context.WithoutCancel(ctx), titleMeter)
		call, cancel := context.WithTimeout(bg, titleCallTimeout)
		title := s.deps.Titler(call, question, lang)
		cancel()
		// The writes run on bg, not on the call's context: a title call that
		// used its whole budget must still be able to record what it spent.
		//
		// SetTitle is called even when the call came back empty — that write
		// settles the row with the placeholder standing, and the header stops
		// waiting for a title that is not coming. `placeholder` is the title
		// Create wrote and the rail is showing right now; handing it over
		// makes the write a no-op once the reader has renamed the thread.
		if err := s.deps.Threads.SetTitle(bg, threadID, placeholder, title); err != nil {
			slog.Warn("set thread title failed", "err", err)
		} else if title != "" {
			// The stream is still open on most turns — a title takes a
			// second, an answer rather longer — so the header and the rail
			// can have the title now instead of at the end of the turn.
			// `send` is a no-op once the handler has returned, so a late one
			// costs nothing and says nothing.
			send("title", map[string]any{"thread_id": threadID, "title": title})
		}
		if err := s.deps.Threads.SaveUsage(bg, messageID, titleMeter.Calls()); err != nil {
			slog.Error("record title usage failed", "err", err)
		}
	}()
	return func() {
		select {
		case <-done:
		case <-time.After(titleStreamGrace):
		}
	}
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

	// The scope of the turn being re-explained carries over with its sources:
	// same question, same corpus, so the same rules about what was and was not
	// in the index. Rendered for this turn's reader too — the new message is a
	// turn of its own and has to stand on its own after a reload.
	if notice := ask.ScopeNotice(lang, msg.Scope); notice != "" {
		send("notice", map[string]any{"text": notice})
	}
	if err := s.deps.Threads.SetScope(record, newMsg.ID, msg.Scope); err != nil {
		slog.Error("record scope failed", "err", err)
	}

	answer, err := s.deps.Ask.Reexplain(ctx, msg.Question, audience, lang, sources, msg.Scope, ask.Events{
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
		send("usage", s.deps.Prices.Prices().Report(calls))
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
	s.finishTurn(ctx, record, newMsg.ID, msg.Question, answer, audience, msg.Scope, lang, send, closeUsage)
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
			report := s.deps.Prices.Prices().Report(msgs[i].Calls)
			msgs[i].Usage = &report
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}

// threadRequest is what the rail's own actions send: a rename carries the new
// title, a delete carries nothing.
type threadRequest struct {
	Title string `json:"title"`
}

// maxTitleRunes is what a thread title may be, matching the length the store
// cuts its placeholder to.
const maxTitleRunes = 48

func (s *Server) handleRenameThread(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	var req threadRequest
	// Capped like every other body this package reads: a title is a line of
	// text, and one that is not would be buffered here and then shipped with
	// the list on every load of the rail.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	// The same length the placeholder is cut to, so a typed title cannot
	// outgrow the one the model writes. Refused rather than truncated: the
	// rail would otherwise show something nobody asked for.
	if len([]rune(title)) > maxTitleRunes {
		http.Error(w, "title is too long", http.StatusBadRequest)
		return
	}
	renamed, err := s.deps.Threads.Rename(r.Context(), u.Subject, id, title)
	if err != nil {
		slog.Error("rename thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !renamed {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	deleted, err := s.deps.Threads.Delete(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("delete thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// threadTarget resolves the reader and the thread id both single-thread
// actions need, answering the request itself when either is missing. A thread
// that is not this reader's is never told apart from one that is gone: both
// end as the 404 the handlers write once the store reports no row.
func (s *Server) threadTarget(w http.ResponseWriter, r *http.Request) (auth.User, int64, bool) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return auth.User{}, 0, false
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return auth.User{}, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "malformed thread id", http.StatusBadRequest)
		return auth.User{}, 0, false
	}
	return u, id, true
}
