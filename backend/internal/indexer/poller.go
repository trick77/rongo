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
	Logger   *slog.Logger
}

// Poller keeps every active repository current. It is deliberately sequential:
// indexing is IO- and API-bound, and a stampede of parallel clones against one
// forge is how a token gets rate-limited.
type Poller struct {
	state    *StateStore
	git      *gitrepo.Client
	index    IndexFunc
	tokens   TokenFunc
	interval time.Duration
	log      *slog.Logger
}

// NewPoller builds a Poller, filling in the defaults.
func NewPoller(d PollerDeps) *Poller {
	if d.Interval <= 0 {
		d.Interval = DefaultPollInterval
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Tokens == nil {
		d.Tokens = func(string) string { return "" }
	}
	return &Poller{
		state: d.State, git: d.Git, index: d.Index,
		tokens: d.Tokens, interval: d.Interval, log: d.Logger,
	}
}

// Run polls until the context ends. It sleeps first, so a restart does not
// stampede every remote at once.
func (p *Poller) Run(ctx context.Context) {
	for {
		if !sched.Sleep(ctx, sched.Jittered(p.interval)) {
			return
		}
		if err := p.PollOnce(ctx); err != nil {
			p.log.Error("poll cycle failed", "err", err)
		}
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
	for _, st := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.pollRepo(ctx, st); err != nil {
			p.log.Warn("repository poll failed", "repo", st.Name, "err", err)
			if markErr := p.state.MarkError(ctx, st.Name, err.Error()); markErr != nil {
				p.log.Error("recording the failure failed too", "repo", st.Name, "err", markErr)
			}
		}
	}
	return nil
}

func (p *Poller) pollRepo(ctx context.Context, st RepoState) error {
	spec := repos.Spec{Name: st.Name, CloneURL: st.CloneURL, Branch: st.Branch, Enabled: true}
	token := p.tokens(st.Name)

	if err := p.git.EnsureCloned(ctx, spec, token); err != nil {
		return err
	}

	// An omitted branch is resolved from the remote and written back, so the
	// Repos page shows what is actually being indexed. Never assume master.
	branch := st.Branch
	if branch == "" {
		resolved, err := p.git.DefaultBranch(ctx, spec)
		if err != nil {
			return err
		}
		branch = resolved
		spec.Branch = resolved
		if err := p.state.SetBranch(ctx, st.Name, resolved); err != nil {
			return err
		}
		st.Branch = resolved
	}

	if err := p.git.Fetch(ctx, spec, token); err != nil {
		return err
	}

	head, err := p.git.HeadSHA(ctx, spec, branch)
	if err != nil {
		// ErrBranchGone is passed through deliberately: the caller records it,
		// and the Repos page shows it. A silent stop here would freeze the
		// index while every status looked healthy.
		if errors.Is(err, gitrepo.ErrBranchGone) {
			return err
		}
		return err
	}

	if head == st.LastSHA {
		return nil
	}

	var paths []string
	if st.LastSHA != "" {
		// Only the changed paths. This is what keeps a push from costing a full
		// re-index. Valid across a branch change too, since both commits live
		// in the same object store.
		changed, err := p.git.ChangedPaths(ctx, spec, st.LastSHA, head)
		if err != nil {
			return err
		}
		paths = changed
	}

	counts, err := p.index(ctx, st, head, paths)
	if err != nil {
		// Deliberately do NOT advance last_sha here. Recording the new SHA
		// after a failed index would make the next run see "unchanged" and
		// leave the repository permanently un-indexed while looking healthy.
		return err
	}

	return p.state.MarkIndexed(ctx, st.Name, head, counts)
}
