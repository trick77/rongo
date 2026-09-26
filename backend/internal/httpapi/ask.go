package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/timeline"
	"github.com/trick77/rongo/internal/usage"
)

// Asker runs one question end to end. An interface so the HTTP layer can be
// tested without a model endpoint.
type Asker interface {
	// t is what earlier turns of this thread left behind: the repositories it
	// narrowed to, and the last question it answered. Zero for a first turn.
	Run(ctx context.Context, question string, audience ask.Audience, lang ask.Language, t ask.Thread, ev ask.Events) (ask.Answer, *ask.Clarification, error)
	// Resume continues a turn from the hits one clarification candidate was
	// built from — no search, no routing. t is what earlier turns left
	// behind, as Run takes it: a turn answered through a card is still a turn
	// of the thread, and a follow-up knows what it follows.
	Resume(ctx context.Context, question string, audience ask.Audience, lang ask.Language,
		hits []retrieve.Hit, scope ask.Scope, t ask.Thread, ev ask.Events) (ask.Answer, error)
	// ResumeRepo continues a turn after the reader chose a REPOSITORY off a
	// card, searching that repository at full depth — or, for an empty repo,
	// the whole corpus. The one resume path that searches again; see
	// ask.Pipeline.ResumeRepo for why a repository card cannot replay stored
	// hits.
	ResumeRepo(ctx context.Context, question string, u ask.Understanding, repos []string,
		audience ask.Audience, lang ask.Language, scope ask.Scope, t ask.Thread, ev ask.Events) (ask.Answer, error)
	// Reexplain answers the same question for the other audience from sources
	// a prior turn already gathered, without searching or gathering again.
	Reexplain(ctx context.Context, question string, audience ask.Audience, lang ask.Language, sources []ask.Source, scope ask.Scope, ev ask.Events) (ask.Answer, error)
	// Rework restates the previous answer of t in the form the instruction
	// asks for, from that answer's own sources. Run takes this path on its
	// own; the re-explain of a rework row takes it from here.
	Rework(ctx context.Context, instruction string, audience ask.Audience, lang ask.Language, t ask.Thread, scope ask.Scope, ev ask.Events) (ask.Answer, error)
}

// turnFailed is what a failed turn says, in the stream AND in the stored
// record. The underlying error may quote an upstream response body, and the
// thread history is served back to the browser too — a generic message in one
// place and the raw text in the other would leak it through the other door.
const turnFailed = "The turn failed."

// turnOverBudget is what a turn says when the token ceiling refused its next
// call (llm.ErrTurnBudget). Its own message, because the fix is a different
// one: not "ask again" but "this question spends more than one turn may",
// and a reader who sees the generic line retries a turn that will trip again.
const turnOverBudget = "The turn stopped: it spent more tokens than one turn is allowed."

// failureMessage is what the stream and the record say for a failed turn:
// the ceiling's own line when that is what stopped it, the generic one for
// everything else. Never the error's text; see turnFailed.
func failureMessage(err error) string {
	if errors.Is(err, llm.ErrTurnBudget) {
		return turnOverBudget
	}
	if errors.Is(err, ask.ErrBasisGone) {
		return reworkBasisGone
	}
	return turnFailed
}

// maxNarrowRepos is how many repositories a too-broad panel lets the reader
// pick. Three, because every one of them costs its own search at full depth
// and the fused result is still cut to one comparison's worth — a fourth side
// competes for room rather than adding any. Enforced here rather than in the
// page: the cap is a product rule, and the page is not the only thing that
// can post.
const maxNarrowRepos = 3

// basisGone is what a re-explain says when the code an answer was written
// from is no longer indexed. Not turnFailed: the pipeline never ran, and
// telling the reader "the turn failed" would claim a failure that
// did not happen — the truth is the material itself is gone.
const basisGone = "The basis of this answer is no longer indexed."

// reworkBasisGone is basisGone for a rework ("summarize"): the answer whose
// basis is gone is the one above, and the turn that asked is the one that
// fails. Same rule, no fresh search in its place — a new answer to
// "summarize" is a different answer dressed as a summary.
const reworkBasisGone = "The basis of the previous answer is no longer indexed, so it cannot be reworked. Ask the question again."

// errNotYours separates "this thread is not yours" from "the query failed".
// Collapsing them turns a locked database into a 403 and hands its text to the
// browser.
var errNotYours = errors.New("thread does not belong to this user")

type askRequest struct {
	// ThreadID is the thread's public address, empty for a new one — where a
	// row number and 0 stood before. It is resolved to the internal id once,
	// at the top of the handler; nothing below this line works in addresses.
	ThreadID string `json:"thread_id"`
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
	// Repos resumes a turn that ended by asking for a NARROWER question: the
	// repositories the reader picked off that panel, at most maxNarrowRepos of
	// them. Present instead of Choice, never beside it — the panel offers no
	// candidate to choose, only repositories to narrow to. Every name is
	// checked against the panel before anything is searched: a browser sending
	// its own list must not widen a turn past what the reader was shown.
	Repos []string `json:"repos"`
	// HeadMessageID is the turn this question is another attempt at, sent when
	// the reader retries one that failed. The question text is the same, so
	// without it the record would say the question was asked twice. A resume
	// carries no HeadMessageID: the card names the turn already.
	//
	// The id is checked against the thread it claims to join before anything
	// is written — a browser naming a row is a browser that could graft an
	// answer under a question nobody asked it for.
	HeadMessageID int64 `json:"head_message_id"`
	// PastedTexts are the trailing blocks of Question the reader pasted into
	// the composer rather than typed, in order. Render-only: the text is
	// already in Question, this only says which part folds into a chip.
	PastedTexts []threads.PastedText `json:"pasted_texts"`
}

// maxQuestionBytes caps the question, pastes included, in UTF-8 bytes — the
// unit the browser measures in too. 32 KiB is some 8k tokens, and the
// question goes into the understand, rerank and answer prompts each; a paste
// past that is a file, not a question. The pasted_texts metadata gets the same
// cap: it rides inside the same body and is stored on every row of the turn.
const maxQuestionBytes = 32 << 10

// wireCandidate is one entry on the clarification card as the browser sees
// it: no hits (large, and the browser has no use for them) and no
// Understanding (internal reasoning nobody reads).
type wireCandidate struct {
	Idx     int    `json:"idx"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	// Members are the repositories behind a project entry, so the card can say
	// what choosing it will search. Absent on a module entry and on a project
	// of one, where the entry already names its only repository.
	Members []string `json:"members,omitempty"`
}

func wireCandidates(cands []ask.Candidate) []wireCandidate {
	out := make([]wireCandidate, len(cands))
	for i, c := range cands {
		out[i] = wireCandidate{Idx: i, Title: c.Title, Summary: c.Summary, Repo: c.Repo, Branch: c.Branch}
		if len(c.Members) > 1 {
			out[i].Members = c.Members
		}
	}
	return out
}

// candidateRepos is what one card entry searches when it is chosen: the
// repositories a project entry folded, or the single repository it named.
//
// Read from the RECORD, never resolved again. A project can gain a member in
// repos.yaml between the card being drawn and the button being pressed, and
// re-resolving would answer from a repository the card never offered — widening
// the reader's own choice behind their back.
//
// An empty Members with a non-empty Repo is a card stored before projects
// shipped, or a project of one. Both mean the same thing, and both are what
// that entry meant when it was drawn.
func candidateRepos(c threads.Candidate) []string {
	if len(c.Members) > 0 {
		return c.Members
	}
	if c.Repo != "" {
		return []string{c.Repo}
	}
	return nil
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
	if len(req.Question) > maxQuestionBytes {
		http.Error(w, "the question is too long", http.StatusBadRequest)
		return
	}
	for _, p := range req.PastedTexts {
		if strings.TrimSpace(p.Text) == "" {
			http.Error(w, "a pasted text is empty", http.StatusBadRequest)
			return
		}
	}
	if blob, err := json.Marshal(req.PastedTexts); err != nil || len(blob) > maxQuestionBytes {
		http.Error(w, "the pasted texts are too long", http.StatusBadRequest)
		return
	}
	audience := parseAudience(req.Audience)
	lang := ask.ParseLanguage(req.Language)

	// The turn's own context, cancellable from outside the request: deleting
	// the thread while it is being answered stops it here. Derived at the very
	// top rather than once the thread id is known, because the meter, the
	// thread id and the question's own INSERT all hang off ctx before that
	// point and a cancel has to reach them too.
	ctx, cancelTurn := context.WithCancelCause(r.Context())
	defer cancelTurn(nil)

	// The request names its thread by address; everything below works in row
	// ids. Resolved once, here, before anything is validated against it — and
	// before a thread could be created, so a request naming a thread that is
	// gone is refused rather than answered into a new one. Empty is a new
	// thread and resolves to 0, which is what it meant when it was a number.
	//
	// Refused as "no such thread" whether the address is unknown or simply
	// someone else's, the same 403 s.thread answers for a thread that is not
	// this reader's: the two must not be told apart.
	reqThreadID, found, err := s.deps.Threads.Resolve(ctx, req.ThreadID)
	if err != nil {
		slog.Error("resolve thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if req.ThreadID != "" && !found {
		http.Error(w, "no such thread", http.StatusForbidden)
		return
	}

	// Resuming a clarification is validated in full BEFORE the thread is
	// touched: an out-of-range choice, a foreign clarification or a card that
	// was already answered must come back as 400/403/409, decided before the
	// first byte of the SSE stream is written, because after that the status
	// code is fixed.
	var resume *threads.Clarification
	// headID is the turn this attempt joins, 0 when the reader typed a new
	// question. Set from the card on a resume, from the request on a retry.
	var headID int64
	var resumeHits []retrieve.Hit
	var resumeScope ask.Scope
	// resumeMsg is the clarification's own row, kept so the resumed turn can
	// be placed in the thread the same way every other continuation is.
	var resumeMsg *threads.Message
	// followUpBefore is the ordinal a resumed turn looks for its follow-up
	// below: the ordinal of the turn this attempt JOINS, which is the card's
	// head row when the card itself sat on a resumed one. A chain of cards is
	// one attempt at one question, so every link of it follows what the first
	// link followed.
	var followUpBefore int
	// resumeRepoChoice marks a card whose entries were repositories;
	// resumeRepos are the ones chosen, empty for the card's "all
	// repositories" entry. A card yields exactly one; the too-broad panel
	// yields up to maxNarrowRepos.
	var resumeRepoChoice bool
	var resumeRepos []string
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
		narrowing := len(req.Repos) > 0
		if narrowing && !c.TooBroad {
			// A card is one question with one answer. Narrowing to a handful
			// is the other panel's move, and accepting it here would answer
			// from repositories the card never offered as a set.
			http.Error(w, "this clarification is a card, not a narrowing", http.StatusBadRequest)
			return
		}
		if !narrowing && c.TooBroad {
			// The other direction, and the one that costs something: Choice
			// defaults to 0, the panel always has a row 0, and the turn would
			// answer from the highest-scoring repository the reader never
			// picked — then record it as a choice they never made.
			http.Error(w, "this clarification is a narrowing, not a card", http.StatusBadRequest)
			return
		}
		if !narrowing && (req.Choice < 0 || req.Choice >= len(c.Candidates)) {
			// Answering from a candidate nobody offered is worse than
			// refusing: it would look like an answer to the question asked.
			http.Error(w, "choice out of range", http.StatusBadRequest)
			return
		}
		var narrowed []string
		if narrowing {
			offered := map[string]bool{}
			for _, cand := range c.Candidates {
				if cand.Repo != "" {
					offered[cand.Repo] = true
				}
			}
			seen := map[string]bool{}
			for _, r := range req.Repos {
				if !offered[r] {
					// Not "unknown repository": the panel is the offer, and
					// anything outside it was never put to the reader.
					http.Error(w, "that repository was not offered", http.StatusBadRequest)
					return
				}
				if seen[r] {
					// A repeat is one repository named twice, not two sides of
					// a comparison. Folded rather than refused — the reader
					// cannot produce one, and it narrows nothing.
					continue
				}
				seen[r] = true
				narrowed = append(narrowed, r)
			}
			if len(narrowed) > maxNarrowRepos {
				http.Error(w, "too many repositories to compare at once", http.StatusBadRequest)
				return
			}
		}
		if c.Answered {
			// A card is answered once. The answer it produced is in the
			// thread, and a second one would be a second answer to a question
			// already decided — refused here, before it costs a model call.
			http.Error(w, "this clarification was already answered", http.StatusConflict)
			return
		}
		// Answered is read off the record, and the record closes the card
		// only once the answer has landed. Until then the claim holds it,
		// so a second choice arriving mid-turn is refused the same way.
		release, ok := s.claims.claim(c.ID)
		if !ok {
			http.Error(w, "this clarification is being answered", http.StatusConflict)
			return
		}
		defer release()
		// An empty module key is what a repository card writes: the choice was
		// a repository, not a module, and there are no stored hits to replay —
		// the resumed turn searches that repository instead. Reading the
		// discriminator off the candidate keeps every card stored before this
		// existed resuming exactly as it did.
		if narrowing {
			// The panel stores no hits to replay and offers no module: every
			// entry is a project, and the resumed turn searches the picked
			// ones at full depth — every repository of each.
			resumeRepoChoice = true
			for _, cand := range c.Candidates {
				for _, picked := range narrowed {
					if cand.Repo == picked {
						resumeRepos = append(resumeRepos, candidateRepos(cand)...)
					}
				}
			}
		} else {
			chosen := c.Candidates[req.Choice]
			resumeRepoChoice = chosen.ModuleKey == ""
			resumeRepos = candidateRepos(chosen)
		}
		var hits []retrieve.Hit
		if !resumeRepoChoice {
			_, hits, err = s.deps.Threads.CandidateHits(ctx, u.Subject, c.ID, req.Choice)
			if err != nil {
				slog.Error("resolve candidate hits failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
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
			// The resumed turn is not a second asking of the question: it is
			// the same one, carried on past the card. It joins the card's
			// turn, which is the card's own head when the card was itself a
			// resume.
			headID = m.Head()
			// And the whole chain follows what the head followed, which the
			// switch below reads off this row: a card answered by another
			// card leaves the second card sitting ABOVE the first one's turn,
			// so the card's own ordinal would make the resumed turn follow a
			// question it is still answering.
			resumeMsg = &m
		}
		if resumeRepoChoice {
			// The choice IS the scope now. Unknown carries over untouched: a
			// repository the question named that the index does not have is
			// still said out loud, whichever entry was picked. An empty repo is
			// the "all repositories" entry, which is the reader's own
			// permission to answer across the corpus and is recorded as such.
			resumeScope.All = len(resumeRepos) == 0
			resumeScope.Known = resumeRepos
		}
		resume = c
		resumeHits = hits
	}

	// A retry names the turn it is another attempt at, and that name is
	// resolved BEFORE a thread is touched — the same order the clarification
	// above is validated in, and for the same reason: a refusal must not have
	// created an empty thread on its way to a 403.
	var retryHead *threads.Message
	if resume == nil && req.HeadMessageID != 0 {
		head, ok, err := s.deps.Threads.Message(ctx, u.Subject, req.HeadMessageID)
		if err != nil {
			slog.Error("resolve head message failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		// Refused, not explained — the same rule the clarification check
		// follows: whether the id exists is not confirmed to someone who does
		// not own it. A thread_id that disagrees with the head is refused
		// too rather than quietly preferring one of them.
		if !ok || (reqThreadID != 0 && reqThreadID != head.ThreadID) {
			http.Error(w, "no such message", http.StatusForbidden)
			return
		}
		retryHead = &head
		headID = head.Head()
	}

	// prior is what earlier turns of this thread left behind: what they
	// narrowed to, and the last question they answered. Read before the stream
	// opens, from the same subject the ownership check used. A thread that
	// cannot be read is treated as a first turn rather than failing this one:
	// an un-narrowed answer is worse than a narrowed one and better than no
	// answer, and the reader is told the scope either way.
	var prior ask.Thread
	var thread threads.Thread
	// followUpIn is the thread whose last answered turn this one follows, 0
	// for a turn that follows nothing.
	var followUpIn int64
	//
	// The three shapes a turn can have — resumed from a card, a retry of a
	// row, or typed into a thread — differ in which row they sit under and
	// where the narrowing comes from, and in nothing else. Each branch says
	// both, so the read below is one read for all of them.
	switch {
	case resume != nil:
		// A resumed turn continues the thread the clarification was asked
		// in — the reader is still in the same conversation, just answering
		// a question rongo asked. And a turn of that conversation can be a
		// follow-up: the card's own turn wrote no answer, so what the new
		// question points at is the answered turn below the one it joins.
		//
		// Its narrowing is the card's own, already carried over above: the
		// choice a reader made on the card is what this turn is about, and
		// the thread's older scope must not widen or contradict it.
		thread = threads.Thread{ID: resume.ThreadID}
		followUpIn = resume.ThreadID
		if resumeMsg != nil {
			followUpBefore = s.headOrdinal(ctx, u.Subject, *resumeMsg)
		}
		prior.Pin = resumeScope.Known
	case retryHead != nil:
		// A retry continues the thread the question was asked in, read off the
		// turn it retries rather than off the request: the row is what says
		// where the attempt belongs. It sits below the row it retries, never
		// below itself, and a retry of a row that continues another follows
		// what THAT row followed.
		//
		// Its narrowing is the retried row's own when the row recorded one —
		// a turn that failed under a card is retried under that card's
		// scope — and the thread's otherwise. Without either, a retry in a
		// pinned thread widened back to the whole corpus and lost what "das"
		// pointed at.
		thread = threads.Thread{ID: retryHead.ThreadID}
		followUpIn = retryHead.ThreadID
		followUpBefore = s.headOrdinal(ctx, u.Subject, *retryHead)
		if scope := retryHead.Scope; scope.All || len(scope.Known) > 0 {
			prior.Pin = scope.Known
		} else {
			prior.Pin = s.threadPin(ctx, u.Subject, retryHead.ThreadID)
		}
	default:
		t, err := s.thread(ctx, u.Subject, reqThreadID, req)
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
		if reqThreadID != 0 {
			// Typed under the newest thing in the thread, so nothing bounds
			// the read from above.
			followUpIn = reqThreadID
			followUpBefore = math.MaxInt
			prior.Pin = s.threadPin(ctx, u.Subject, reqThreadID)
		}
	}

	// The last ANSWERED turn below that bound is what a follow-up points at,
	// because "kannst du das in einem Diagramm aufzeigen?" names no mechanism
	// — the reader named it a turn ago. A read that fails is logged and
	// treated as no previous turn, the way an unreadable thread is.
	if followUpIn != 0 {
		last, ok, err := s.deps.Threads.LastTurnBefore(ctx, u.Subject, followUpIn, followUpBefore)
		if err != nil {
			slog.Error("read last turn failed", "err", err)
		} else if ok {
			prior.Question, prior.Answer = last.Question, last.Answer
			// And what that answer was written from, for a rework. Read
			// here rather than once the understanding has said the turn is
			// one: the pipeline has no thread store, and one SELECT per
			// follow-up is the cost. A read that fails fails the request,
			// unlike the reads above: a turn that goes on without the
			// basis answers "summarize" afresh, which is worse than no
			// answer.
			sources, total, err := s.deps.Threads.Sources(ctx, u.Subject, last.ID)
			if err != nil {
				slog.Error("read last turn's sources failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			prior.Sources, prior.SourcesTotal = sources, total
		}
	}

	// Every model call this turn makes carries the thread, so the whole
	// conversation pins to one upstream node instead of scattering across the
	// deployment. Attached once here: both the fresh and the resumed path land
	// on the same thread value.
	// A resumed clarification and a retry both reach their thread through a
	// MESSAGE, so the branches above have a row id and no address. The stream
	// tells the browser which thread to put in the address bar, so it needs
	// one either way.
	if thread.PublicID == "" {
		publicID, err := s.deps.Threads.PublicIDFor(ctx, thread.ID)
		if err != nil {
			slog.Error("read thread address failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		thread.PublicID = publicID
	}

	ctx = llm.WithThreadID(ctx, thread.ID)
	// Every paid call this turn makes lands in one meter, the gates included.
	// Attached after the thread id and before the title goroutine forks off:
	// the title gets a meter of its own below.
	meter := usage.New()
	ctx = usage.WithMeter(ctx, meter)
	// And every step it announces lands in one recorder, so the trace the
	// reader watched is still there when they come back to the thread.
	steps := timeline.New()
	ctx = timeline.With(ctx, steps)
	// And the reader's standing instructions, read once here: the
	// understanding step lists them, the answer is written under them, and a
	// rule given in this very question joins them mid-turn.
	ctx = s.memoryHolder(ctx, u.Subject)

	// Registered from here on, where there is a thread id to register it
	// under. Everything before this is validation; the paid work starts below.
	defer s.turns.add(thread.ID, cancelTurn)()

	msg, err := s.deps.Threads.AddQuestion(ctx, thread.ID, string(audience), string(lang), req.Question, headID)
	if err != nil {
		slog.Error("record question failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// Render-only, so a failure costs the chip and never the turn: the paste
	// is inside the question already.
	if err := s.deps.Threads.SavePastedTexts(ctx, msg.ID, req.PastedTexts); err != nil {
		slog.Error("record pasted texts failed", "err", err)
	}
	// The record decides the language, not the request: a thread keeps the one
	// its first turn was asked in, and AddQuestion hands back what it stored.
	// Read back rather than assumed, so the answer, the notice and the
	// follow-up pills are written in the language the turn is filed under.
	lang = ask.ParseLanguage(msg.Language)

	// Shut when the handler returns — after the deferred title wait below,
	// which is registered later and so runs first.
	st := openStream(w)
	defer st.close()
	send := st.send

	// The language goes out with the thread, because the record may not have
	// taken the one that was asked for: a thread answers in the language of its
	// first turn. Sent first thing, so a composer that guessed corrects itself
	// before the answer starts arriving in a language it did not expect.
	send("thread", map[string]any{
		"thread_id":  thread.PublicID,
		"title":      thread.Title,
		"message_id": msg.ID,
		"language":   string(lang),
	})

	// The record is written on a context that outlives the request; see turn.
	record := context.WithoutCancel(ctx)
	tr := s.beginTurn(ctx, record, msg.ID, meter, steps, st)

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
				recordMissed(ctx, "settle thread title failed", err)
			}
		} else {
			settled := s.writeTitle(ctx, thread.ID, msg.ID, thread.PublicID, req.Question, thread.Title, lang, send)
			defer settled()
		}
	}

	events := tr.events()
	events.OnNotice = func(text string) { send("notice", map[string]any{"text": text}) }
	events.OnMemory = s.onMemory(record, u.Subject, msg.ID, send)
	closeRecord := tr.closeRecord

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
			recordFailed(ctx, "record scope failed", serr)
		}
		var answer ask.Answer
		var err error
		if resumeRepoChoice {
			answer, err = s.deps.Ask.ResumeRepo(ctx, req.Question, resume.Understanding, resumeRepos, audience, lang, resumeScope, prior, events)
		} else {
			answer, err = s.deps.Ask.Resume(ctx, req.Question, audience, lang, resumeHits, resumeScope, prior, events)
		}
		if err != nil {
			turnStopped(ctx, "resumed turn failed", thread.ID, err, tr.progress()...)
			tr.fail(failureMessage(err))
			return
		}
		// Written a second time, over the scope stored before the call: the
		// turn only learns after gathering whether it stood on documentation
		// alone, and the row written above is what a failed resume leaves
		// behind. The answer's own scope is the one the record keeps.
		if serr := s.deps.Threads.SetScope(record, msg.ID, answer.Scope); serr != nil {
			recordFailed(ctx, "record scope failed", serr)
		}
		tr.finish(answer.Text, answer.Citations, answer.Sources)
		// -1 is the column's own "no candidate": a narrowing resumed from the
		// panel as a whole, and there is no row on it that the answer came
		// from. The link to the clarification is what closes it either way.
		choiceIdx := req.Choice
		if len(req.Repos) > 0 {
			choiceIdx = -1
		}
		if err := s.deps.Threads.LinkChoice(record, u.Subject, msg.ID, resume.ID, choiceIdx); err != nil {
			recordFailed(ctx, "link choice failed", err)
		}
		s.finishTurn(ctx, record, msg.ID, req.Question, answer, audience, resumeScope, lang, tr.streamed, send, closeRecord)
		return
	}

	answer, clar, err := s.deps.Ask.Run(ctx, req.Question, audience, lang, prior, events)
	if err != nil {
		turnStopped(ctx, "turn failed", thread.ID, err, tr.progress()...)
		// A generic message: the error may quote an upstream body, and that is
		// not something to hand a browser.
		tr.fail(failureMessage(err))
		return
	}
	// Stored whichever way the turn ended, before the ending is sent: a card
	// and an answer both belong to a question that named repositories, and a
	// resumed turn reads the scope back off this row.
	if clar != nil {
		if serr := s.deps.Threads.SetScope(record, msg.ID, clar.Scope); serr != nil {
			recordFailed(ctx, "record scope failed", serr)
		}
	} else if serr := s.deps.Threads.SetScope(record, msg.ID, answer.Scope); serr != nil {
		recordFailed(ctx, "record scope failed", serr)
	}

	if clar != nil {
		if _, cerr := s.deps.Threads.Clarify(record, msg.ID, *clar); cerr != nil {
			recordFailed(ctx, "record clarification failed", cerr)
			// Clarify writes the clarification and its candidates in one
			// transaction precisely so that a card cannot go out with some
			// candidates missing their stored hits. If the write failed, the
			// card must not ship either: sending it anyway would offer
			// choices resuming them cannot honour, and the clarification row
			// is the only thing distinguishing "ended by asking" from "still
			// in flight" — so the turn must be recorded as failed here.
			tr.fail(turnFailed)
			return
		}
		closeRecord()
		send("clarification", map[string]any{"message_id": msg.ID, "too_broad": clar.TooBroad,
			"candidates": wireCandidates(clar.Candidates)})
		send("done", map[string]any{"message_id": msg.ID})
		return
	}

	tr.finish(answer.Text, answer.Citations, answer.Sources)
	s.finishTurn(ctx, record, msg.ID, req.Question, answer, audience, answer.Scope, lang, tr.streamed, send, closeRecord)
}

const (
	// titleCallTimeout bounds the whole naming attempt, retries included. The
	// answer's own budget is fifteen minutes, which is the right order for an
	// answer and absurd for a six-word label: a stalled title would hold a
	// goroutine, a meter and a row's pending flag for a quarter of an hour.
	// It is the ceiling over ask.Title's own per-attempt bound, so it has to
	// leave room for every attempt — cut below that and the last one is killed
	// mid-call for no reason. Nothing waits on it: the reader has their answer
	// and the stream closed after titleStreamGrace long before this is
	// reached; a title landing late arrives on the next list fetch.
	titleCallTimeout = 90 * time.Second
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
	streamed bool,
	send func(string, any),
	closeRecord func(),
) {
	// A turn with no sources was answered by a template, never a stream:
	// nothing found, no commits in the window, an instruction kept. The
	// text is on the record, and this is the one place it reaches the
	// browser live; without it the reader saw the trace close over an empty
	// answer until a reload. Only when nothing streamed: a text sent twice
	// is an answer read twice.
	sourceless := len(answer.Sources) == 0
	if !streamed && answer.Text != "" {
		send("token", map[string]any{"text": answer.Text})
	}
	send("citations", answer.Citations)
	s.suggestFollowups(ctx, record, messageID, question, answer, audience, scope, lang, send)
	closeRecord()
	// Language again, for the re-explain path: it opens no thread event, and
	// its turn is filed in the thread's language whatever it asked for.
	// Sourceless too: the page hides "Explain as Developer" on a turn that
	// has nothing to re-explain from, live as on a reload.
	send("done", map[string]any{"message_id": messageID, "language": string(lang), "sourceless": sourceless})
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
	// Never for a thread the reader deleted while it was being answered: the
	// message row this would be written to is already gone with it, and the
	// call would be paid for to fill a column nobody will ever read.
	if s.deps.Suggester == nil || len(answer.Sources) == 0 || threadWasDeleted(ctx) {
		return
	}
	send("status", map[string]any{"step": "suggesting", "at": timeline.Record(ctx, "suggesting")})
	// record, not ctx: a reader who closes the tab, reloads, or loses the
	// connection between the last word and this call cancelled the request,
	// and with it the only chance this answer ever had at suggestions - the
	// column is written once, here, and nothing goes back for it later. The
	// answer itself is already stored on record a few lines up for the same
	// reason. The meter rides along: WithoutCancel keeps the values.
	call, cancel := context.WithTimeout(record, followupsCallTimeout)
	defer cancel()
	// Dropping the request's cancellation does not mean dropping the thread's:
	// a reader who deletes the thread WHILE this call is running is owed the
	// same stop as one who deleted it a moment earlier, and the row the answer
	// would be written to is going with it either way.
	defer context.AfterFunc(ctx, func() {
		if threadWasDeleted(ctx) {
			cancel()
		}
	})()
	qs := s.deps.Suggester(call, question, answer.Text, audience, answer.Sources, scope, lang)
	if len(qs) == 0 {
		return
	}
	if err := s.deps.Threads.SaveFollowups(record, messageID, qs); err != nil {
		// The pills are worth a warning and nothing more: the answer is
		// written and the turn is finished either way.
		recordMissed(ctx, "record followups failed", err)
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
	threadPublicID string,
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
		// A thread deleted before the call went out gets no title call at all:
		// this is the one turn goroutine the cancel does not reach, since it
		// runs detached on purpose, so the check stands in for it. There is
		// nothing left to name, and the label is not worth paying for.
		if threadWasDeleted(ctx) {
			return
		}
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
			recordMissed(ctx, "set thread title failed", err)
		} else if title != "" {
			// The stream is still open on most turns — a title takes a
			// second, an answer rather longer — so the header and the rail
			// can have the title now instead of at the end of the turn.
			// `send` is a no-op once the handler has returned, so a late one
			// costs nothing and says nothing.
			send("title", map[string]any{"thread_id": threadPublicID, "title": title})
		}
		if err := s.deps.Threads.SaveUsage(bg, messageID, titleMeter.Calls()); err != nil {
			recordFailed(ctx, "record title usage failed", err)
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
		// Language is optional and only ever a request: the thread answers in
		// the language its first turn was asked in, so on a thread that has
		// one — which a re-explain always does — the record wins.
		Language string `json:"language"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	audience := parseAudience(req.Audience)

	// A re-explain is a paid turn like any other, and stops the same way when
	// the thread it re-answers is deleted under it.
	ctx, cancelTurn := context.WithCancelCause(r.Context())
	defer cancelTurn(nil)
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
	defer s.turns.add(msg.ThreadID, cancelTurn)()
	meter := usage.New()
	ctx = usage.WithMeter(ctx, meter)
	steps := timeline.New()
	ctx = timeline.With(ctx, steps)
	// The reader's standing instructions apply to a re-explain as they do
	// to any answer: same reader, same rules.
	ctx = s.memoryHolder(ctx, u.Subject)
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
		// A vanished basis is its own message, not the generic turnFailed:
		// the pipeline never ran, and the truth is that the code the answer
		// was written from is no longer indexed.
		openStream(w).send("error", map[string]any{"message": basisGone})
		return
	}

	// A successful re-explain is a NEW row, not a rewrite: the thread is a
	// record, and an earlier answer may already have been forwarded or pasted
	// into a ticket. It is not a new QUESTION, though — the same one is being
	// answered for the other audience — so the row joins that question's turn
	// and the page prints the question once. from_candidate_idx stays at its
	// default -1: this row did not resume a clarification.
	newMsg, err := s.deps.Threads.AddQuestion(ctx, msg.ThreadID, string(audience), string(lang), msg.Question, msg.Head())
	if err != nil {
		slog.Error("record re-explain question failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// The row copies the question, so it copies the fold too.
	if err := s.deps.Threads.SavePastedTexts(ctx, newMsg.ID, msg.PastedTexts); err != nil {
		slog.Error("record re-explain pasted texts failed", "err", err)
	}
	// The stored language is the thread's, whatever was asked for. Same rule
	// as /api/ask: the turn is answered in the language it is filed under.
	lang = ask.ParseLanguage(newMsg.Language)

	st := openStream(w)
	defer st.close()
	send := st.send

	// The record is written on a context that outlives the request, the same
	// as every other write in this package.
	record := context.WithoutCancel(ctx)
	tr := s.beginTurn(ctx, record, newMsg.ID, meter, steps, st)

	// The scope of the turn being re-explained carries over with its sources:
	// same question, same corpus, so the same rules about what was and was not
	// in the index. Rendered for this turn's reader too — the new message is a
	// turn of its own and has to stand on its own after a reload.
	if notice := ask.ScopeNotice(lang, msg.Scope); notice != "" {
		send("notice", map[string]any{"text": notice})
	}
	if err := s.deps.Threads.SetScope(record, newMsg.ID, msg.Scope); err != nil {
		recordFailed(ctx, "record scope failed", err)
	}

	events := tr.events()
	var answer ask.Answer
	if msg.Scope.Intent == ask.IntentRework {
		// A rework row's question is an instruction ("summarize"), and its
		// sources are the turn below's. Re-explained over the sources alone
		// it would be answered afresh — the very thing the rework lane
		// stops — so it is reworked again, over the same antecedent.
		answer, err = s.reworkAgain(ctx, u.Subject, msg, audience, lang, sources, total, events)
	} else {
		answer, err = s.deps.Ask.Reexplain(ctx, msg.Question, audience, lang, sources, msg.Scope, events)
	}
	if err != nil {
		turnStopped(ctx, "reexplain failed", msg.ThreadID, err, tr.progress()...)
		tr.fail(failureMessage(err))
		return
	}
	// The same sources, not answer.Sources: a re-explain answers from exactly
	// what the original turn gathered, so the new turn can itself be
	// re-explained later from that same, unchanged evidence.
	tr.finish(answer.Text, answer.Citations, sources)
	s.finishTurn(ctx, record, newMsg.ID, msg.Question, answer, audience, msg.Scope, lang, tr.streamed, send, tr.closeRecord)
}

// reworkAgain re-answers a rework row for the other audience: the same
// instruction over the same antecedent, the last answered turn below the
// row, whose sources the row already carries. An antecedent that cannot be
// read is a basis that is gone.
func (s *Server) reworkAgain(ctx context.Context, subject string, msg threads.Message, audience ask.Audience,
	lang ask.Language, sources []ask.Source, total int, ev ask.Events) (ask.Answer, error) {

	last, ok, err := s.deps.Threads.LastTurnBefore(ctx, subject, msg.ThreadID, s.headOrdinal(ctx, subject, msg))
	if err != nil {
		return ask.Answer{}, fmt.Errorf("read the reworked turn: %w", err)
	}
	if !ok {
		return ask.Answer{}, fmt.Errorf("%w: no answered turn below the rework", ask.ErrBasisGone)
	}
	t := ask.Thread{Pin: msg.Scope.Known, Question: last.Question, Answer: last.Answer, Sources: sources, SourcesTotal: total}
	return s.deps.Ask.Rework(ctx, msg.Question, audience, lang, t, msg.Scope, ev)
}

// headOrdinal is the ordinal a turn continuing m has to stay below: m's own,
// or its head's when m is itself a continuation. A card answered by another
// card, or a retry of a re-explain, leaves the row ABOVE the turn it belongs
// to, and its own ordinal would make the new turn follow a question that is
// still being answered.
//
// A head that cannot be read yields 0, which is no antecedent at all. That is
// the safe end of the mistake: a turn answered without the previous question
// is a turn that has to be asked more fully, where one answered under the
// WRONG previous question is a turn that quietly followed something else.
func (s *Server) headOrdinal(ctx context.Context, subject string, m threads.Message) int {
	head := m.Head()
	if head == 0 || head == m.ID {
		return m.Ordinal
	}
	ordinal, ok, err := s.deps.Threads.MessageOrdinal(ctx, subject, head)
	if err != nil || !ok {
		slog.Error("resolve head ordinal failed", "err", err, "head", head)
		return 0
	}
	return ordinal
}

// threadPin is what earlier turns of this thread narrowed to. A read that
// fails is an un-narrowed turn, which is worse than a narrowed one and better
// than no answer.
func (s *Server) threadPin(ctx context.Context, subject string, threadID int64) []string {
	pin, err := s.deps.Threads.ThreadScope(ctx, subject, threadID)
	if err != nil {
		slog.Error("read thread scope failed", "err", err)
		return nil
	}
	return pin
}

// thread returns the thread this turn belongs to, creating one when the request
// names none. An existing thread is checked against its owner: the id comes
// from the browser, and a thread belongs to the person who asked.
func (s *Server) thread(ctx context.Context, subject string, threadID int64, req askRequest) (threads.Thread, error) {
	if threadID == 0 {
		return s.deps.Threads.Create(ctx, subject, req.Question)
	}
	owns, err := s.deps.Threads.Owns(ctx, subject, threadID)
	if err != nil {
		return threads.Thread{}, err
	}
	if !owns {
		return threads.Thread{}, errNotYours
	}
	return threads.Thread{ID: threadID, PublicID: req.ThreadID}, nil
}
