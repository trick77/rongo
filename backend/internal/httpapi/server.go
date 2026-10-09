// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/timeline"
	"github.com/trick77/rongo/internal/usage"
	"github.com/trick77/rongo/internal/version"
	"github.com/trick77/rongo/web"
)

// Threads is everything the HTTP layer needs from the thread record. An
// interface, not the concrete *threads.Store, so a test can fake a single
// method's failure (a Clarify that cannot write, for instance) without
// carrying a whole database through it. *threads.Store satisfies this
// structurally.
type Threads interface {
	// List is read by the tests through deps; no handler lists a reader's threads whole.
	List(ctx context.Context, subject string) ([]threads.Thread, error)
	Create(ctx context.Context, subject, question string) (threads.Thread, error)
	// SetTitle replaces `from` with `to`, and only while the row still holds
	// `from`: the title is written in the background and must not overwrite a
	// name its owner typed in the meantime.
	SetTitle(ctx context.Context, id int64, from, to string) error
	Rename(ctx context.Context, subject string, id int64, title string) (bool, error)
	Delete(ctx context.Context, subject string, id int64) (bool, error)
	SetStarred(ctx context.Context, subject string, id int64, starred bool) (bool, error)
	// SetFeedback, ClearFeedback and Feedback are the reader's verdict on a
	// thread. Owner inside the statement, like SetStarred.
	SetFeedback(ctx context.Context, subject string, id int64, verdict int, reason string) (bool, error)
	ClearFeedback(ctx context.Context, subject string, id int64) (bool, error)
	Feedback(ctx context.Context, subject string, id int64) (threads.Feedback, bool, error)
	AddQuestion(ctx context.Context, threadID int64, audience, language, question string, headID int64) (threads.Message, error)
	// FinishWithSources writes the answer, its citations and the sources it
	// was written from in one transaction — and, for a resumed turn, the
	// link closing the card it answers.
	FinishWithSources(ctx context.Context, messageID int64, answer string, citations []ask.Citation, sources []ask.Source, choice *threads.Choice) error
	Fail(ctx context.Context, messageID int64, msg string) error
	// ListPage, Get and Search are the Threads page's reads: the rail and the
	// page read the list a page at a time, the header asks for one thread the
	// rail's page does not carry, and the search box asks by title and by
	// what was said.
	ListPage(ctx context.Context, subject string, opts threads.ListOptions) (threads.ThreadPage, error)
	Get(ctx context.Context, subject string, threadID int64) (threads.Thread, bool, error)
	Search(ctx context.Context, subject, query string, limit int) ([]threads.Hit, error)
	Message(ctx context.Context, subject string, messageID int64) (threads.Message, bool, error)
	Messages(ctx context.Context, subject string, threadID int64) ([]threads.Message, error)
	Owns(ctx context.Context, subject string, threadID int64) (bool, error)
	// Resolve turns a thread's public address — what stands in every URL — into
	// the row id everything in here works with. Owner-blind on purpose: the
	// caller pairs it with an ownership predicate so "not yours" and "gone"
	// answer alike.
	Resolve(ctx context.Context, publicID string) (int64, bool, error)
	// PublicIDFor is the way back, for the turns that reach their thread
	// through a message: a resume and a retry have a row id and still have to
	// tell the browser which address the thread lives at.
	PublicIDFor(ctx context.Context, id int64) (string, error)
	// ThreadScope is the repositories earlier turns of this thread already
	// narrowed to, so a follow-up inherits them instead of being asked which
	// repository was meant.
	ThreadScope(ctx context.Context, subject string, threadID int64) ([]string, error)
	// LastTurnBefore is the most recent turn of this thread that answered and
	// sits below an ordinal, which is what a follow-up is a follow-up to. The
	// bound is what places a continuation: a card or a retried row need not
	// be the newest turn of its thread, and what the new turn follows sits
	// below the turn it joins. A turn typed into a thread passes math.MaxInt.
	LastTurnBefore(ctx context.Context, subject string, threadID int64, before int) (threads.Message, bool, error)
	// MessageOrdinal is where one message sits in its thread, for placing a
	// continuation under the row its head belongs to.
	MessageOrdinal(ctx context.Context, subject string, messageID int64) (int, bool, error)
	Clarify(ctx context.Context, messageID int64, c ask.Clarification) (int64, error)
	Clarification(ctx context.Context, subject string, messageID int64) (*threads.Clarification, error)
	CandidateHits(ctx context.Context, subject string, clarificationID int64, idx int) (ask.Understanding, []retrieve.Hit, error)
	// SetScope records what the turn's question said about repositories, so a
	// reload can render the notice and a resumed turn can rebuild the rules.
	SetScope(ctx context.Context, messageID int64, scope ask.Scope) error
	// SetMemory records the standing instruction a turn saved, so its chip
	// and undo survive a reload.
	SetMemory(ctx context.Context, messageID, memoryID int64) error
	Sources(ctx context.Context, subject string, messageID int64) (sources []ask.Source, total int, err error)
	// SourceRefs is what an answer's basis is, without the text: one read of
	// the database, where Sources reads every file from git.
	SourceRefs(ctx context.Context, subject string, messageID int64) ([]ask.Source, error)
	// SaveUsage records the paid calls one turn made, however it ended.
	SaveUsage(ctx context.Context, messageID int64, calls []usage.Call) error
	// SaveFollowups records what the finished answer offered to ask next.
	SaveFollowups(ctx context.Context, messageID int64, questions []string) error
	// SavePastedTexts records which trailing blocks of a row's question were
	// pasted, so the page can fold them into chips.
	SavePastedTexts(ctx context.Context, messageID int64, pasted []threads.PastedText) error
	// SaveSteps records the activity timeline one turn was watched through,
	// however it ended.
	SaveSteps(ctx context.Context, messageID int64, tr timeline.Trace) error

	// Sharing. Share/RaiseShare/RevokeShare/ShareFor/Shares/SharedIDs all take
	// a subject: a link is made, moved and taken back by the thread's owner.
	// SharedThread and SharedCitation do not — the token is the authorisation,
	// and they are the only two reads in rongo that answer without a session.
	Share(ctx context.Context, subject string, threadID int64) (threads.Share, error)
	RaiseShare(ctx context.Context, subject string, threadID int64) (threads.Share, error)
	RevokeShare(ctx context.Context, subject string, threadID int64) (bool, error)
	Shares(ctx context.Context, subject string) ([]threads.Share, error)
	SharedThread(ctx context.Context, token string) (threads.Share, []threads.Message, error)
	// SharedTitle is the same read cut down to one column, for the link
	// preview in the served HTML: that runs on every page view of a share
	// link, and does not need the turns.
	SharedTitle(ctx context.Context, token string) (string, error)
	SharedCitation(ctx context.Context, token, repo, path, sha string) (bool, error)
	// SharedCommit is SharedCitation for a commit citation.
	SharedCommit(ctx context.Context, token, repo, sha string) (bool, error)
	// CitedBy is SharedCitation for the owner: a thread this subject owns
	// cites that file at that commit.
	CitedBy(ctx context.Context, subject, repo, path, sha string) (bool, error)
	// CommitCitedBy is CitedBy for a commit citation.
	CommitCitedBy(ctx context.Context, subject, repo, sha string) (bool, error)
}

// Deps holds every collaborator the HTTP layer needs. Phase 1 has only Auth,
// wired as its concrete *auth.Service; later phases add the indexer, the LLM
// client and the retriever the same way, one field each. A nil field means
// the feature is unconfigured; its endpoints answer 503.
type Deps struct {
	Auth *auth.Service
	// OIDC drives the login redirect and the callback. An interface, not the
	// concrete *auth.OIDCService, so a test can make a callback fail without a
	// provider. Nil means this deployment has no OIDC (dev or token mode), and
	// the two auth routes say so with a 503.
	OIDC OIDCService
	// OIDCAdminGroup is the group that grants admin. Empty means no check; see
	// auth.Service.CreateSessionFromClaims.
	OIDCAdminGroup string
	// CookieSecure marks the session cookie Secure. It comes from the OIDC
	// redirect URL or, in password mode, BACKEND_COOKIE_SECURE, because
	// behind a TLS-terminating proxy the process only ever sees plain HTTP
	// and nothing about the request says otherwise.
	CookieSecure bool
	// Repos backs the Repos page. Nil means this deployment cannot report
	// repository status, which its endpoint says with a 503 rather than an
	// empty list.
	Repos RepoStatusSource
	// Reindex takes the Repos page's one action. Nil means the poller is not
	// running here, and the page offers nothing.
	Reindex Reindexer
	// Ask runs the question pipeline; Threads persists the record. Nil means
	// this deployment cannot answer questions, which its routes say with a 503.
	Ask     Asker
	Threads Threads
	// Memory keeps each reader's standing instructions. Nil means the
	// deployment keeps none (BACKEND_MEMORY=false): the understanding step
	// asks for no directive, the answer carries no block, the page says so.
	Memory Memories
	// Source serves a cited file out of the checkout, so a source in the
	// evidence panel can be opened. Nil means this deployment has no checkout
	// to read from, which the endpoint says with a 503.
	Source SourceReader
	// Commit serves a cited commit for the commit view, under the same rule
	// as Source: nil means no checkout, and a 503.
	Commit CommitReader
	// Titler names a thread. Optional: without it the sidebar keeps the first
	// words of the question, which is a worse label but never a broken one.
	Titler func(ctx context.Context, question string, lang ask.Language) string
	// Suggester writes the follow-up questions offered under a finished
	// answer. Optional, on the same terms as Titler: without it the answer
	// simply ends, which is what it did before the pills existed.
	Suggester func(ctx context.Context, question, answer string, audience ask.Audience,
		sources []ask.Source, scope ask.Scope, lang ask.Language) []string
}

// Memories is the reader's standing instructions, as the HTTP layer needs
// them. *memory.Store satisfies it structurally.
type Memories interface {
	List(ctx context.Context, subject string) ([]memory.Row, error)
	Add(ctx context.Context, subject string, d memory.Directive, sourceMessageID int64) (memory.Added, error)
	Remove(ctx context.Context, subject string, id int64) (bool, error)
}

// OIDCService is the login half of authentication, as the HTTP layer needs it.
// *auth.OIDCService satisfies it structurally.
type OIDCService interface {
	StartLogin(w http.ResponseWriter, r *http.Request)
	HandleCallback(r *http.Request) (auth.Claims, error)
	ClearTransientCookies(w http.ResponseWriter)
}

// Server routes requests and owns the middleware chain.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	handler http.Handler
	// turns is the answers being streamed right now, so a thread deleted
	// mid-answer stops the turn writing into it instead of paying for it.
	turns *turns
	// claims is the clarifications being answered right now, so a card
	// cannot be chosen twice before its first answer has closed it.
	claims *claims
	// logins is the password login's failures per address; see
	// loginThrottle.
	logins *loginThrottle
}

// NewServer builds the router and wraps it in the middleware chain once,
// rather than per request.
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux(), turns: newTurns(), claims: newClaims(), logins: newLoginThrottle()}
	s.routes()
	// logging outermost: a panicked request must still produce an access-log
	// line (as a 500), so recovery has to run inside logging, not around it.
	s.handler = logging(recovery(s.mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// routes is the single place every route is registered.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	// Login and callback sit outside requireAuth: a caller who still has to
	// sign in has no session, and gating them would be a redirect loop.
	// Logout does not, because revoking a session needs one.
	s.mux.HandleFunc("GET /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("GET /api/auth/callback", s.handleAuthCallback)
	s.mux.HandleFunc("POST /api/auth/password", s.handleAuthPassword)
	s.authed("POST /api/auth/logout", s.handleAuthLogout)
	s.authed("GET /api/me", s.handleMe)
	s.authed("GET /api/repos", s.handleRepos)
	s.authed("POST /api/repos/reindex", s.handleReindex)
	s.authed("POST /api/repos/{name}/reindex", s.handleReindex)
	s.authed("GET /api/threads", s.handleThreads)
	s.authed("GET /api/threads/search", s.handleSearchThreads)
	s.authed("GET /api/threads/{id}", s.handleThread)
	s.authed("GET /api/threads/{id}/summary", s.handleThreadSummary)
	s.authed("PATCH /api/threads/{id}", s.handleRenameThread)
	s.authed("DELETE /api/threads/{id}", s.handleDeleteThread)
	s.authed("POST /api/threads/{id}/star", s.handleStarThread)
	s.authed("POST /api/threads/{id}/unstar", s.handleUnstarThread)
	s.authed("GET /api/threads/{id}/feedback", s.handleGetFeedback)
	s.authed("PUT /api/threads/{id}/feedback", s.handlePutFeedback)
	s.authed("DELETE /api/threads/{id}/feedback", s.handleDeleteFeedback)
	s.authed("GET /api/source", s.handleSource)
	s.authed("GET /api/commit", s.handleCommit)
	s.authed("POST /api/ask", s.handleAsk)
	s.authed("POST /api/messages/{id}/reexplain", s.handleReexplain)

	// The reader's standing instructions: listed on the Memory page, deleted
	// there or by the undo under the answer that saved one. Written only
	// through chat, so there is no POST.
	s.authed("GET /api/memory", s.handleMemory)
	s.authed("DELETE /api/memory/{id}", s.handleForgetMemory)

	// Making, moving and taking back a link is the owner's, so these are gated
	// like every other thread action.
	s.authed("POST /api/threads/{id}/share", s.handleShare)
	s.authed("POST /api/threads/{id}/share/update", s.handleShareUpdate)
	s.authed("DELETE /api/threads/{id}/share", s.handleRevokeShare)
	s.authed("GET /api/shares", s.handleShares)

	// The only unauthenticated output path in rongo. Registered with
	// HandleFunc, so they never enter the middleware at all: an anonymous
	// reader has no session and gating them would answer 401 to the one
	// audience they exist for. The token is the authorisation, the ceiling is
	// the limit, and neither handler can reach a turn the link does not cover.
	s.mux.HandleFunc("GET /api/shares/{token}", s.handlePublicShare)
	s.mux.HandleFunc("GET /api/shares/{token}/source", s.handlePublicShareSource)
	s.mux.HandleFunc("GET /api/shares/{token}/commit", s.handlePublicShareCommit)

	// "/" is the catch-all: everything not matched above goes to the SPA.
	// The shell is handed the share title so a link unfurls in Slack and X
	// with the thread's own question — no crawler runs the JavaScript that
	// would otherwise set it.
	s.mux.Handle("/", web.HandlerWithShareTitles(s.shareTitle))
}

// authed mounts h behind requireAuth.
func (s *Server) authed(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.requireAuth(h))
}

// requireAuth is the single gate every authenticated route goes through.
//
// A request that changes something and comes from another site is refused
// before auth runs. Dev and proxy mode admit a request that carries no cookie,
// so SameSite protects only password and OIDC; without this a cross-site form
// could post to a bodiless action (reindex, star, share, logout) in the
// operator's own browser.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.deps.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
		})
	}
	authed := s.deps.Auth.Middleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if crossSite(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		authed.ServeHTTP(w, r)
	})
}

// crossSite reports whether a request that is not a read came from another
// origin. Fetch metadata decides when the browser sent it: only same-origin
// and none (typed, bookmarked) pass — same-site is a sibling subdomain, which
// SameSite would let through and is still not rongo. Only an older browser
// without it falls back to Origin, which is compared with both the host the
// request reached and the one the proxy says it was sent to — behind a proxy
// r.Host can be the loopback listener. A request with neither header is no
// browser's and passes: a token-mode script sends none.
func crossSite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site != "same-origin" && site != "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// "null" is an opaque origin: a sandboxed frame, a data: URL.
		return true
	}
	if strings.EqualFold(u.Host, r.Host) {
		return false
	}
	fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	fwd = strings.TrimSpace(fwd)
	if fwd != "" && strings.EqualFold(u.Host, fwd) {
		return false
	}
	// A proxy that rewrites Host and forwards no X-Forwarded-Host lands here
	// for every write from such a browser; without the hosts the operator
	// sees only 403s.
	slog.Warn("cross-site request refused", "origin_host", u.Host, "host", r.Host, "forwarded_host", fwd)
	return true
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	// The version rides on the session request rather than an endpoint of its
	// own: it is chrome the UI wants before its first render, and this is the
	// one call it already makes there.
	writeJSON(w, map[string]any{
		"subject":  u.Subject,
		"email":    u.Email,
		"is_admin": u.IsAdmin,
		"version":  version.Version,
	})
}
