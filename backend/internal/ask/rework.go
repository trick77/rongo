package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/llm"
)

// IntentRework is the understanding's intent for a follow-up that asks for
// the previous answer in another form: "summarize", "shorter", "as a table".
const IntentRework = "rework"

// ErrBasisGone is a rework refused because the sources the previous answer
// was written from are no longer all indexed. The handler names it to the
// reader; the pipeline neither answers from what is left nor runs a search
// instead — a fresh answer to "summarize" is a different answer dressed as a
// summary, which is the failure this lane exists to stop.
var ErrBasisGone = errors.New("rework: the sources of the previous answer are no longer indexed")

// isRework reports whether this turn restates the previous answer. The guard
// is the pipeline's, not the prompt's: a first turn has nothing to rework
// whatever the model said, and it runs as an ordinary question. So does a
// follow-up to an answer that never had a basis — "nothing found" and "no
// changes" are answered turns with no sources, and there is no claim in
// them to keep on its source.
func isRework(u Understanding, t Thread) bool {
	return u.Intent == IntentRework && t.Answer != "" && t.SourcesTotal > 0
}

// Rework restates the previous answer of t in the form instruction asks for,
// from that answer's own sources, without searching or gathering. Run takes
// this path on its own when the understanding says so; the re-explain of a
// rework row takes it from the handler, because answering "summarize" over
// the sources alone would be the fresh answer this lane exists to stop.
func (p *Pipeline) Rework(ctx context.Context, instruction string, audience Audience, lang Language,
	t Thread, scope Scope, ev Events) (Answer, error) {
	return p.answerRework(ctx, instruction, audience, lang, t, scope, ev)
}

// answerRework is the turn from the scope on: no search, no routing, no
// gathering. The previous answer's own sources are the basis, all of them or
// none — a summary that cites the survivors of a re-index and silently drops
// the rest is the substitution "never invent" forbids.
func (p *Pipeline) answerRework(ctx context.Context, instruction string, audience Audience, lang Language,
	t Thread, scope Scope, ev Events) (Answer, error) {

	if t.Answer == "" || len(t.Sources) == 0 || len(t.Sources) < t.SourcesTotal {
		return Answer{}, fmt.Errorf("%w (%d of %d left)", ErrBasisGone, len(t.Sources), t.SourcesTotal)
	}
	slog.Info("rework", "thread", llm.ThreadID(ctx), "sources", len(t.Sources))

	// Rebuilt the way Reexplain rebuilds them: structure and process listing
	// are never persisted, and the reworked text has to stand where the first
	// one stood.
	scope = p.describeProjects(ctx, scope)
	scope.Processes = p.describeProcesses(ctx, t.Sources)
	// Said live, the way a fresh turn says it: the record alone would tell
	// the reader only after a reload.
	if scope.DocsOnly = DocsOnly(t.Sources); scope.DocsOnly {
		ev.notice(ScopeNotice(lang, scope))
	}

	ev.status("answering")
	answer, err := p.answerer.Rework(ctx, instruction, audience, lang, t, scope, ev.tokens())
	answer.Scope = scope
	if err == nil {
		ev.readAt(t.Sources).detail("writing", writingDetail(answer, len(t.Sources)))
	}
	return answer, err
}

// readAt is where a rework's basis was read from: each repository at the
// commit its files were read at, once. The basis is the record re-read from
// git, not the index as it stands now, and the trace says so.
func readAt(sources []Source) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range sources {
		if s.IsCommit() || s.SHA == "" {
			continue
		}
		k := s.Repo + " " + gitrepo.ShortSHA(s.SHA)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
