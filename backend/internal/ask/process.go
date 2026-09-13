package ask

import (
	"context"
	"log/slog"
	"strings"

	"github.com/trick77/rongo/internal/bpmn"
	"github.com/trick77/rongo/internal/llm"
)

// Models is the index as the process listing needs it: which process model
// files a repository carries, and their content at the commit they were
// indexed at. *sourceview.Service satisfies it.
type Models interface {
	// Models lists the indexed process model files of repo with the sha
	// each was indexed at.
	Models(ctx context.Context, repo string) ([]ModelRef, error)
	// ReadModel returns one model file at one commit.
	ReadModel(ctx context.Context, repo, path, sha string) ([]byte, error)
}

// ModelRef is one process model file the index carries.
type ModelRef struct {
	Path string
	SHA  string
}

// maxProcessChars caps the listing. A model with fifty nodes renders in about
// three thousand characters; the cap holds the flagship process, its called
// sub-processes and one more before it stops, which is what a trace question
// needs and well under the budget the sources have.
const maxProcessChars = 16000

// describeProcesses renders the wiring of every process model among the
// sources, plus the models their call activities run, one level down.
//
// Per turn and never stored, like the structure block: the listing is read
// from the model file at the commit the source was cited at, so it is as
// fresh as the index and cannot rot on its own. It is not a source and is
// never cited; it tells the answer the ORDER of the steps the sources
// describe, and which step is in which model. Without it a trace question is
// answered from whichever node chunks retrieval returned, in the order they
// came, which is a paragraph per chunk and no walk.
//
// A failure is logged and swallowed: the listing improves a trace answer, it
// does not decide whether an answer is right, and a turn that read its
// sources fine should not fail because one model would not parse.
func (p *Pipeline) describeProcesses(ctx context.Context, sources []Source) string {
	if p.models == nil {
		return ""
	}
	type key struct{ repo, path string }
	var order []key
	seen := map[key]bool{}
	for _, s := range sources {
		if !strings.HasSuffix(strings.ToLower(s.Path), ".bpmn") {
			continue
		}
		k := key{s.Repo, s.Path}
		if seen[k] {
			continue
		}
		seen[k] = true
		order = append(order, k)
	}
	if len(order) == 0 {
		return ""
	}

	// Every model of a repository that has one among the sources, parsed
	// once, so a call activity's target resolves to a file and a process id
	// is a thing the listing can follow. A repository holds a dozen models
	// at most; parsing them is cheaper than one embedding call.
	type parsed struct {
		repo, path string
		model      *bpmn.Model
	}
	byRepo := map[string][]parsed{}
	// process id -> the file defining it, per repository: a call activity
	// runs a process deployed beside it, and two repositories may each have
	// an "order" process without either meaning the other's.
	byProcess := map[string]map[string]parsed{}
	for _, k := range order {
		if _, done := byRepo[k.repo]; done {
			continue
		}
		refs, err := p.models.Models(ctx, k.repo)
		if err != nil {
			slog.Warn("process models unavailable, answering without the wiring",
				"thread", llm.ThreadID(ctx), "repo", k.repo, "err", err)
			byRepo[k.repo] = nil
			continue
		}
		for _, r := range refs {
			body, err := p.models.ReadModel(ctx, k.repo, r.Path, r.SHA)
			if err != nil {
				slog.Warn("process model unreadable", "thread", llm.ThreadID(ctx), "repo", k.repo, "path", r.Path, "err", err)
				continue
			}
			m, err := bpmn.Parse(body)
			if err != nil {
				slog.Warn("process model unparsable", "thread", llm.ThreadID(ctx), "repo", k.repo, "path", r.Path, "err", err)
				continue
			}
			pd := parsed{repo: k.repo, path: r.Path, model: m}
			byRepo[k.repo] = append(byRepo[k.repo], pd)
			if byProcess[k.repo] == nil {
				byProcess[k.repo] = map[string]parsed{}
			}
			for _, pr := range m.Processes {
				if len(pr.Nodes) > 0 {
					byProcess[k.repo][pr.ID] = pd
				}
			}
		}
	}
	find := func(repo, path string) (parsed, bool) {
		for _, pd := range byRepo[repo] {
			if pd.path == path {
				return pd, true
			}
		}
		return parsed{}, false
	}
	resolvedIn := func(repo string) func(string) string {
		return func(id string) string {
			if pd, ok := byProcess[repo][id]; ok {
				return pd.repo + "/" + pd.path
			}
			return ""
		}
	}

	// The models among the sources first, then what they call, one level:
	// a sub-process the answer is told about by name is one it can describe;
	// the level below that is a different question.
	var b strings.Builder
	rendered := map[string]bool{}
	type call struct{ repo, id string }
	var called []call
	render := func(pd parsed) bool {
		where := pd.repo + "/" + pd.path
		if rendered[where] {
			return true
		}
		text := bpmn.Describe(where, pd.model, resolvedIn(pd.repo))
		if text == "" {
			return true
		}
		if b.Len()+len(text) > maxProcessChars {
			return false
		}
		rendered[where] = true
		b.WriteString(text)
		b.WriteString("\n")
		for _, pr := range pd.model.Processes {
			for _, id := range calls(pr) {
				called = append(called, call{pd.repo, id})
			}
		}
		return true
	}
	cut := false
	for _, k := range order {
		if pd, ok := find(k.repo, k.path); ok && !render(pd) {
			cut = true
		}
	}
	// A copy: render appends what the called models call in turn, and that
	// second level stays out.
	for _, c := range append([]call(nil), called...) {
		if pd, ok := byProcess[c.repo][c.id]; ok && !render(pd) {
			cut = true
		}
	}
	if b.Len() == 0 {
		return ""
	}
	if cut {
		b.WriteString("(Further models were left out for length.)\n")
	}
	return b.String()
}

// calls lists the processes p's call activities run, embedded sub-processes
// included.
func calls(p *bpmn.Process) []string {
	var out []string
	for _, n := range p.Nodes {
		if n.Called != "" {
			out = append(out, n.Called)
		}
	}
	for _, sub := range p.Subs {
		out = append(out, calls(sub)...)
	}
	return out
}
