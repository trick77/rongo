package threads

import (
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/timeline"
	"github.com/trick77/rongo/internal/usage"
)

// Thread is one conversation.
//
// Table: threads
type Thread struct {
	// ID is the row number and never leaves the process. It is the foreign key
	// every message, share and usage row hangs off, and the value the
	// in-flight turn map is keyed by.
	ID int64 `json:"-"`
	// PublicID is the thread's address: what stands in /thread/… and on every
	// /api/threads/… path, and the only thread identifier the browser ever
	// sees. Serialised as "id" because it IS the id as far as anything outside
	// this process is concerned.
	PublicID string `json:"id"`
	Title    string `json:"title"`
	// TitlePending is true while the model's title call is still running. The
	// Title on such a row is the question's first words, which is enough to
	// tell one sidebar row from another and is not a title: the header says
	// "New question" rather than show a question cut mid-word.
	TitlePending bool      `json:"title_pending"`
	CreatedAt    time.Time `json:"created_at"`
	// Shared is true while a live link points at this thread. It is what the
	// rail marks a row with; the link itself is not sent with the list,
	// because the rail has nothing to do with it.
	Shared bool `json:"shared,omitempty"`
	// Starred is the reader's own mark: the rail lists starred threads in a
	// section of their own, above the recent ones.
	Starred bool `json:"starred,omitempty"`
}

// Message is one ANSWER ATTEMPT: a question and what came of it. It is not
// one question — a resume, a retry and a re-explain each append a row that
// repeats the question text of the row it continues, and HeadMessageID is
// what says they are all the same turn.
//
// Table: messages
type Message struct {
	ID int64 `json:"id"`
	// ThreadID is the thread this message belongs to. Re-explaining reads it
	// off a bare message id to add the new turn to the same thread, the same
	// way a resumed clarification does.
	//
	// Not sent to the browser: the page already knows which thread it is
	// reading, and a row number on every message would put back the counter
	// that PublicID exists to keep off the wire — on the public share page
	// too, which is not even the owner's.
	ThreadID int64  `json:"-"`
	Ordinal  int    `json:"ordinal"`
	Audience string `json:"audience"`
	// Language is the language the answer was written in, per message like
	// Audience: the selector sits on the input field.
	Language string `json:"language"`
	Question string `json:"question"`
	// Scope is what the question said about repositories, resolved against
	// the index. Stored as ask.Scope's JSON so a resumed turn and a
	// re-explain can rebuild the same prompt rules the first turn ran under.
	//
	// Not sent to the browser, for the same reason Understanding is not: it is
	// the machinery, and the page has no use for it. What the page gets is
	// Notice, the one sentence a person reads.
	Scope ask.Scope `json:"-"`
	// Notice is Scope rendered in this message's own language. Derived on
	// read rather than stored, so a sentence is never left behind in a
	// language the turn was not answered in.
	Notice    string         `json:"notice"`
	Answer    string         `json:"answer"`
	Error     string         `json:"error"`
	Citations []ask.Citation `json:"citations"`
	// Followups are the questions this answer offered to ask next, in the
	// message's own language. Stored with the turn so a reload shows what the
	// reader was offered rather than a fresh set of guesses; empty on a turn
	// that ended in a card, a failure or a nothing-found.
	Followups []string `json:"followups"`
	// Clarification is set when this turn ended by asking which mechanism was
	// meant, so a reload renders the card instead of a turn that looks stuck.
	Clarification *Clarification `json:"clarification,omitempty"`
	// NarrowedTo is the repositories a turn resumed from the too-broad panel
	// was narrowed to. Derived on read from Scope, the same way Notice is, and
	// empty on every other turn: a card names its own choice by title, and a
	// turn that resumed nothing narrowed nothing.
	NarrowedTo []string `json:"narrowed_to,omitempty"`
	// FromCandidateIdx is the candidate this turn resumed from, or -1 when it
	// did not resume a clarification.
	FromCandidateIdx int `json:"from_candidate_idx"`
	// FromClarificationID is the clarification this turn resumed from, or 0
	// when it did not resume one. It is what lets a reload mark the right
	// candidate on the right card: two clarifications can be open in one
	// thread at once, and position alone cannot tell them apart.
	FromClarificationID int64 `json:"from_clarification_id"`
	// HeadMessageID is the turn this row belongs to: the message that carried
	// the question a reader typed. 0 when this row IS that message. A resume,
	// a retry and a re-explain each copy the question text of the row they
	// continue — the record is append-only — so this link is the only thing
	// that says the question was asked once. It points at the head, never at
	// the row one step up, and it is written when the row is inserted rather
	// than when the answer lands, so a turn that failed still groups under
	// the question it failed at.
	HeadMessageID int64     `json:"head_message_id"`
	CreatedAt     time.Time `json:"created_at"`
	// Calls is every paid call this turn made, as stored, each with the cost
	// it was priced at. The HTTP layer sums them into Usage.
	Calls []usage.Call `json:"-"`
	// Usage is what the browser sees: the calls, their sum, and the cost
	// when any call was priced. Filled by the HTTP layer, never here.
	Usage *usage.Report `json:"usage,omitempty"`
	// Steps is the activity timeline this turn was watched through. Absent
	// for a turn that announced nothing and for every turn written before the
	// column existed; the page simply shows no trace for those, as it did for
	// all of them before this was stored. A shared link never carries it.
	Steps *timeline.Trace `json:"steps,omitempty"`
	// PastedTexts are the trailing blocks of Question the reader pasted rather
	// than typed. Render-only: the page folds each into a chip and shows the
	// rest as prose. The text itself stays in Question, where every prompt
	// reads it; nothing in the pipeline looks here. Absent on a turn that
	// carried none. A shared link keeps it: the text is public either way, and
	// a chip drawn as prose would misread as the reader's words.
	PastedTexts []PastedText `json:"pasted_texts,omitempty"`
	// Sourceless is a turn that answered from no sources: nothing found, an
	// empty commit window, an instruction kept. The page hides the re-explain
	// action on it, which would otherwise answer "the sources are no longer
	// indexed" about sources that never were. Read off message_sources, so a
	// turn from before anything was recorded there reads as sourceless too,
	// which is what its re-explain would have said anyway.
	Sourceless bool `json:"sourceless,omitempty"`
	// Memory is the standing instruction this turn saved, for the chip under
	// the answer and its undo. Read off the memories row the turn points at,
	// so a rule deleted since simply is not there: the record says
	// "forgotten" by saying nothing. Never on a shared link.
	Memory *MemoryRef `json:"memory,omitempty"`
}

// Head is the turn this message belongs to: its head, or its own id when it
// IS the head. One hop is enough by construction — a head is stored as the
// root of the chain, never as the row one step up — so no read here walks.
func (m Message) Head() int64 {
	if m.HeadMessageID != 0 {
		return m.HeadMessageID
	}
	return m.ID
}

// Clarification is the card a turn ended with: what rongo understood, and
// the candidates it offered. The hits each candidate was built from are NOT
// included here — they are large and never sent to the browser; fetch them
// with CandidateHits for the resumed turn only.
//
// Table: clarifications
type Clarification struct {
	ID int64 `json:"id"`
	// ThreadID is the thread the clarifying message belongs to. A resumed turn
	// continues THIS thread rather than opening a new one — the reader is
	// still in the same conversation, just answering a question rongo asked.
	//
	// Not sent to the browser, on the same terms as Message.ThreadID above:
	// the card rides the public share payload, and nothing there reads it.
	ThreadID int64 `json:"-"`
	// Understanding is provenance: it is stored so a resumed turn can say what
	// the first one searched for, and so a stored card can be read back years
	// later. It is not sent to the browser — nothing there ever read it, and
	// serialising the model's internal guesses into the page is output nobody
	// asked for. The column stays.
	Understanding ask.Understanding `json:"-"`
	Candidates    []Candidate       `json:"candidates"`
	// TooBroad is the turn having ended by asking for a narrower question
	// rather than by offering a choice. The browser needs it to tell the two
	// apart after a reload: both are a message with candidates, but one is a
	// card with written titles and one is a list of repository names.
	TooBroad bool `json:"too_broad"`
	// Answered is true once a turn resumed from this card. A card is answered
	// once: the handler refuses a second resume with it, before the choice
	// costs a model call. Not sent to the browser — the page derives the same
	// fact from the answer's from_clarification_id, which it needs anyway to
	// tell two open cards in one thread apart.
	Answered bool `json:"-"`
}

// Candidate is one entry on a clarification card, without its hits.
//
// Table: clarification_candidates
type Candidate struct {
	Idx       int    `json:"idx"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	ModuleKey string `json:"module_key"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	// Members are the repositories a PROJECT entry offered, as the card drew
	// them. Empty on a module entry, and on a card stored before projects
	// shipped — which then means the single repository in Repo.
	Members []string `json:"members,omitempty"`
}

// Share is one thread readable by anyone holding the link.
//
// Table: shared_threads
type Share struct {
	Token string `json:"token"`
	// The path, never an absolute URL: the browser puts its own origin in
	// front. A rongo behind a TLS-terminating proxy only ever sees plain HTTP
	// and would build the wrong one.
	Path string `json:"path"`
	// ThreadPublicID is the shared thread's own address, so the Shared page can
	// link back to it. Not the row number, and not the token either: the token
	// opens the public page, this opens the owner's thread.
	ThreadPublicID string `json:"thread_id"`
	// threadID is the row it all hangs off, unexported: the public read below
	// needs it to fetch the messages, and nothing outside this package has any
	// business with a row number.
	threadID int64
	Title    string `json:"title"`
	// The ceiling. Turns above it are on the record but not on the link.
	UpToMessageID int64     `json:"up_to_message_id"`
	Turns         int       `json:"turns"`
	Newer         int       `json:"newer"`
	SharedAt      time.Time `json:"shared_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// recorded is one message_sources row as stored.
//
// Table: message_sources
type recorded struct {
	chunkID, commitID int64
	repo, path, sha   string
	start, end        int
	symbol, reason    string
	hop               int
}
