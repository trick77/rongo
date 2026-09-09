package indexer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/sched"
)

// DefaultPollInterval is how often the poller checks every repository. Thirty
// minutes of staleness is irrelevant to the questions rongo answers, and
// polling needs no ingress and no delivery-failure recovery, which is why there
// are no webhooks.
const DefaultPollInterval = 30 * time.Minute

// FirstPollDelay is how long the poller waits before its FIRST cycle, as
// opposed to the interval between later ones. It exists because a fresh
// deployment is restarted several times while the operator fixes the
// configuration, and each restart used to reset a full DefaultPollInterval:
// half an hour of a healthy-looking process that clones nothing, with no way
// to tell "not yet" from "broken". Short enough to see the first clone during
// setup, still jittered so a fleet coming up together does not arrive at one
// forge in the same second.
const FirstPollDelay = 30 * time.Second

// IndexFunc indexes one repository at one commit. paths is nil for a full index
// and carries the changed paths for an incremental one.
type IndexFunc func(ctx context.Context, st RepoState, sha string, paths []string) (Counts, error)

// TokenFunc resolves a repository's forge token from the environment. It takes
// the name of the environment variable, never a value from repos.yaml.
type TokenFunc func(tokenEnv string) string

// PollerDeps are the poller's collaborators.
type PollerDeps struct {
	State    *StateStore
	Git      *gitrepo.Client
	Index    IndexFunc
	Tokens   TokenFunc
	Interval time.Duration
	// FirstDelay overrides FirstPollDelay. A test that wants the first cycle
	// immediately sets it; nothing in production does.
	FirstDelay time.Duration
	Logger     *slog.Logger
}

// Poller keeps every active repository current. It is deliberately sequential:
// indexing is IO- and API-bound, and a stampede of parallel clones against one
// forge is how a token gets rate-limited.
type Poller struct {
	state      *StateStore
	git        *gitrepo.Client
	index      IndexFunc
	tokens     TokenFunc
	interval   time.Duration
	firstDelay time.Duration
	log        *slog.Logger
}

// NewPoller builds a Poller, filling in the defaults.
func NewPoller(d PollerDeps) *Poller {
	if d.Interval <= 0 {
		d.Interval = DefaultPollInterval
	}
	if d.FirstDelay <= 0 {
		d.FirstDelay = FirstPollDelay
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Tokens == nil {
		d.Tokens = func(string) string { return "" }
	}
	return &Poller{
		state: d.State, git: d.Git, index: d.Index,
		tokens: d.Tokens, interval: d.Interval, firstDelay: d.FirstDelay,
		log: d.Logger,
	}
}

// Run polls until the context ends. It sleeps before every cycle, the first
// one included, so a restart does not stampede every remote at once — but that
// first wait is FirstPollDelay, not the full interval.
func (p *Poller) Run(ctx context.Context) {
	// The first wait is short and announced. Without the log line "nothing is
	// happening" is indistinguishable from a broken indexer, which is exactly
	// how a fresh deployment reads while it waits.
	delay := sched.Jittered(p.firstDelay)
	p.log.Info("indexing scheduled", "first_poll_in", delay.Round(time.Second), "interval", p.interval)
	for {
		if !sched.Sleep(ctx, delay) {
			return
		}
		if err := p.PollOnce(ctx); err != nil {
			p.log.Error("poll cycle failed", "err", err)
		}
		delay = sched.Jittered(p.interval)
	}
}

// PollOnce processes every active repository once.
//
// It returns an error only for a failure that stops the whole cycle, such as
// being unable to read the repository list. A failure affecting ONE repository
// is recorded on that repository and the loop continues: one unreachable forge
// must not stall the rest of the corpus.
func (p *Poller) PollOnce(ctx context.Context) error {
	active, err := p.state.Active(ctx)
	if err != nil {
		return err
	}
	// A cycle used to be entirely silent unless something broke, so "the index
	// is stale" and "the poller stopped running" produced identical logs —
	// which is to say no logs. The pair of lines around the loop is what makes
	// a healthy run visible, and the counts are what make an unhealthy one
	// obvious without reading every repository's line.
	started := time.Now()
	p.log.Info("poll cycle started", "repositories", len(active))

	var indexed, unchanged, failed int
	for _, st := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		repoStart := time.Now()
		res, err := p.pollRepo(ctx, st)
		switch {
		case err != nil:
			failed++
			p.log.Warn("repository poll failed", "repo", st.Name,
				"took", took(repoStart), "err", err)
			if markErr := p.state.MarkError(ctx, st.Name, err.Error()); markErr != nil {
				p.log.Error("recording the failure failed too", "repo", st.Name, "err", markErr)
			}
		case res.Changed:
			indexed++
			// Info, because this is the event somebody is looking for when they
			// ask whether a push has landed in the answers yet.
			p.log.Info("repository indexed", "repo", st.Name, "mode", res.Mode(),
				"sha", gitrepo.ShortSHA(res.SHA), "files", res.Counts.Files,
				"chunks", res.Counts.Chunks, "took", took(repoStart))
		default:
			unchanged++
			// Debug: at a thirty-minute interval this is most lines most of the
			// time, and the cycle summary already carries the count. Raising
			// BACKEND_LOG_LEVEL to debug is what "which repository did nothing"
			// is for.
			p.log.Debug("repository unchanged", "repo", st.Name,
				"sha", gitrepo.ShortSHA(res.SHA), "took", took(repoStart))
		}
	}

	p.log.Info("poll cycle finished", "checked", len(active), "indexed", indexed,
		"unchanged", unchanged, "failed", failed, "took", took(started))
	return nil
}

// pollResult is what one repository's cycle did, so the caller can log and
// count it. The poller itself decides nothing from this — it exists to make the
// cycle legible.
type pollResult struct {
	// Changed is false when the commit was already indexed.
	Changed bool
	// Full is true for an index of every path, as opposed to a diff.
	Full   bool
	SHA    string
	Counts Counts
}

// Mode names what an index run did, for the log line. "full" and "incremental"
// are the words the pipeline itself uses for nil versus non-nil paths.
func (r pollResult) Mode() string {
	if r.Full {
		return "full"
	}
	return "incremental"
}

// took rounds a duration to something a person reads at a glance. Milliseconds
// below a second, because a clone is seconds and a no-op poll is milliseconds
// and both belong on the same line.
func took(start time.Time) time.Duration {
	d := time.Since(start)
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}

func (p *Poller) pollRepo(ctx context.Context, st RepoState) (pollResult, error) {
	// A snapshot has no remote, so every step below — the origin check, the
	// clone, the default branch, the fetch — is asking a question of something
	// that is not there. It takes its own path rather than growing four
	// conditions into this one.
	if st.Snapshot() {
		return p.pollSnapshot(ctx, st)
	}

	spec := repos.Spec{
		Name: st.Name, CloneURL: st.CloneURL, Branch: st.Branch,
		TokenEnv: st.TokenEnv, Enabled: true,
	}
	// The ENVIRONMENT VARIABLE NAME, not the repository name: TokenFunc reads
	// the environment, and passing the repository name here resolved every
	// token to the empty string, so every private repository was fetched
	// anonymously while repos.yaml looked correctly configured.
	token := p.tokens(st.TokenEnv)

	// A checkout is named after its YAML entry, and until this check nothing
	// ever asked whether the directory of that name actually holds the
	// repository the entry points at. It did not always: an entry whose
	// clone_url was corrected kept its old clone, and every answer built from it
	// cited one repository's code under the other's name. The URL is the
	// identity; the name is only a label.
	//
	// The index built from the old remote goes with the checkout. Keeping it
	// would leave the corpus holding two repositories under one name until every
	// stale path happened to be overwritten, which for a file the new repository
	// does not have is never.
	origin, err := p.git.OriginURL(ctx, spec)
	if err != nil {
		return pollResult{}, err
	}
	if origin != "" && origin != spec.CloneURL {
		p.log.Info("checkout points at a different remote; re-cloning",
			"repo", st.Name, "checkout_origin", origin, "configured", spec.CloneURL)
		if err := p.state.ResetRepo(ctx, st.Name); err != nil {
			return pollResult{}, err
		}
		if err := p.git.RemoveCheckout(st.Name); err != nil {
			return pollResult{}, err
		}
		// The reset cleared last_sha in the database; this copy is what the rest
		// of the run reads, and a stale value here would send it into an
		// incremental diff against a commit from the previous repository.
		st.LastSHA = ""
	}

	if err := p.git.EnsureCloned(ctx, spec, token); err != nil {
		return pollResult{}, err
	}

	// An omitted branch is resolved from the remote and written back, so the
	// Repos page shows what is actually being indexed. Never assume master.
	branch := st.Branch
	if branch == "" {
		resolved, err := p.git.DefaultBranch(ctx, spec, token)
		if err != nil {
			return pollResult{}, err
		}
		branch = resolved
		spec.Branch = resolved
		if err := p.state.SetBranch(ctx, st.Name, resolved); err != nil {
			return pollResult{}, err
		}
		st.Branch = resolved
		p.log.Info("branch resolved from the remote", "repo", st.Name, "branch", resolved)
	}

	if err := p.git.Fetch(ctx, spec, token); err != nil {
		return pollResult{}, err
	}

	head, err := p.git.HeadSHA(ctx, spec, branch)
	if err != nil {
		// ErrBranchGone is passed through deliberately: the caller records it,
		// and the Repos page shows it. A silent stop here would freeze the
		// index while every status looked healthy.
		if errors.Is(err, gitrepo.ErrBranchGone) {
			return pollResult{}, err
		}
		return pollResult{}, err
	}

	if head == st.LastSHA {
		// Nothing new, but the poll SUCCEEDED — so a last_error left by an
		// earlier network failure has to go. Returning early without clearing
		// it left a healthy repository showing a permanent error until someone
		// happened to push to it.
		return pollResult{SHA: head}, p.state.MarkChecked(ctx, st.Name)
	}

	var paths []string
	if st.LastSHA != "" {
		// Only the changed paths. This is what keeps a push from costing a full
		// re-index. Valid across a branch change too, since both commits live
		// in the same object store.
		changed, err := p.git.ChangedPaths(ctx, spec, st.LastSHA, head)
		if err != nil {
			return pollResult{}, err
		}
		paths = changed
		p.log.Debug("changed paths since the indexed commit", "repo", st.Name,
			"from", gitrepo.ShortSHA(st.LastSHA), "to", gitrepo.ShortSHA(head),
			"paths", len(changed))
	}

	counts, err := p.index(ctx, st, head, paths)
	if err != nil {
		// Deliberately do NOT advance last_sha here. Recording the new SHA
		// after a failed index would make the next run see "unchanged" and
		// leave the repository permanently un-indexed while looking healthy.
		return pollResult{}, err
	}

	res := pollResult{Changed: true, Full: paths == nil, SHA: head, Counts: counts}
	return res, p.state.MarkIndexed(ctx, st.Name, head, counts)
}

// pollSnapshot is pollRepo for a hand-extracted source drop: the same shape
// with the remote taken out.
//
// There is nothing to clone, nothing to fetch and no remote to name a default
// branch, so the cycle is "commit whatever is on disk, then index it if that
// changed anything". For a drop nobody touched it changes nothing, which is why
// a snapshot costs one `git add` per cycle and never re-indexes — the one-off
// behaviour is the ordinary path here, not a special case.
func (p *Poller) pollSnapshot(ctx context.Context, st RepoState) (pollResult, error) {
	spec := repos.Spec{Name: st.Name, Snapshot: true, Enabled: true}

	sha, err := p.git.EnsureSnapshot(ctx, spec)
	if err != nil {
		return pollResult{}, err
	}

	// Written back so the Repos page and every citation carry a branch that is
	// true of the checkout. It is a constant, but it still has to be recorded:
	// the column is what sourceview and the citation renderer read.
	if st.Branch != gitrepo.SnapshotBranch {
		if err := p.state.SetBranch(ctx, st.Name, gitrepo.SnapshotBranch); err != nil {
			return pollResult{}, err
		}
		st.Branch = gitrepo.SnapshotBranch
	}

	if sha == st.LastSHA {
		// Unchanged, and the poll SUCCEEDED — clearing a last_error left by an
		// earlier missing directory is part of that. For a snapshot this is the
		// steady state, not the exception.
		return pollResult{SHA: sha}, p.state.MarkChecked(ctx, st.Name)
	}

	var paths []string
	if st.LastSHA != "" {
		if p.git.HasCommit(ctx, spec, st.LastSHA) {
			changed, err := p.git.ChangedPaths(ctx, spec, st.LastSHA, sha)
			if err != nil {
				return pollResult{}, err
			}
			paths = changed
			p.log.Debug("changed paths since the indexed commit", "repo", st.Name,
				"from", gitrepo.ShortSHA(st.LastSHA), "to", gitrepo.ShortSHA(sha),
				"paths", len(changed))
		} else {
			// The drop was deleted and re-extracted, so `git init` built a new
			// object store and the recorded commit is not in it. Diffing against
			// it would fail with "bad object" on this and every later cycle,
			// leaving the entry in a permanent error while the files sit there
			// perfectly readable. Drop the index and read the new tree whole.
			p.log.Info("snapshot was replaced; re-indexing in full",
				"repo", st.Name, "indexed_sha", st.LastSHA)
			if err := p.state.ResetRepo(ctx, st.Name); err != nil {
				return pollResult{}, err
			}
			st.LastSHA = ""
		}
	}

	counts, err := p.index(ctx, st, sha, paths)
	if err != nil {
		// Same reason as pollRepo: recording the sha after a failed index would
		// make the next cycle see "unchanged" and leave the repository
		// permanently un-indexed while looking healthy.
		return pollResult{}, err
	}
	res := pollResult{Changed: true, Full: paths == nil, SHA: sha, Counts: counts}
	return res, p.state.MarkIndexed(ctx, st.Name, sha, counts)
}
