package ask

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/llm"
)

// DefaultSinceDays is the window a changes question gets when it names none:
// "what changed lately" means the last two weeks. Applied by the pipeline,
// never by the model, so the record says which days were looked at.
const DefaultSinceDays = 14

// maxSinceDays caps what a question may ask back. The lane holds a bounded
// history (indexer.DefaultHistoryDepth), and "everything ever" is a question
// about a changelog, not about recent changes.
const maxSinceDays = 365

// changesLimit is how many commits reach the answer. Forty one-line commits
// is a digest; a busier window is cut newest first (or best match first
// under a topic), and the prompt says the list may be cut.
const changesLimit = 40

// Histories is the commit lane as the pipeline needs it. An interface for
// the reason Searcher is: a test drives the branch without a database.
type Histories interface {
	Search(ctx context.Context, q history.Query) ([]history.Commit, error)
	// BySHAs is the rows for a range read from the checkout, in the
	// range's order, without the ones the lane never recorded.
	BySHAs(ctx context.Context, repo string, shas []string) ([]history.Commit, error)
}

// WithHistory wires the commit lane and the clock a changes turn measures
// its window against. Without it a changes question runs the ordinary
// pipeline, which is what it did before the lane existed. The clock is a
// parameter so the harness can ask about a pinned corpus's last days from
// any date.
func (p *Pipeline) WithHistory(h Histories, now func() time.Time) *Pipeline {
	p.history = h
	p.now = now
	return p
}

// isChanges reports whether this turn is answered from the commit lane.
func (p *Pipeline) isChanges(u Understanding) bool {
	return p.history != nil && u.Intent == IntentChanges
}

// IntentChanges is the understanding's intent for "what changed".
const IntentChanges = "changes"

// answerChanges is the turn from the scope on: no fused search, no routing
// ladder, no reference walk. The window and the named repositories ARE the
// scope; a question across eight repositories is answered as a digest
// grouped by repository, never carded, because a reader asking what
// changed wants the list, not a question back. Sources are commits, cited
// like files and stored like them.
func (p *Pipeline) answerChanges(ctx context.Context, question string, audience Audience, lang Language,
	u Understanding, scope Scope, followingUp string, ev Events) (Answer, error) {

	scope.SinceDays = clampSinceDays(int(u.SinceDays))
	scope.Topic = strings.TrimSpace(u.Topic)
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	since := now.Add(-time.Duration(scope.SinceDays) * 24 * time.Hour)

	ev.status("searching")
	commits, err := p.history.Search(ctx, history.Query{
		Repos: scope.Known, Since: since, Topic: scope.Topic, Limit: changesLimit,
	})
	if err != nil {
		return Answer{}, fmt.Errorf("commit search: %w", err)
	}
	sources := commitSources(commits)
	ev.detail("searching", changesDetail(sources, scope))
	slog.Info("changes", "thread", llm.ThreadID(ctx), "since_days", scope.SinceDays,
		"topic", scope.Topic, "repos", scope.Known, "commits", len(commits))

	scope = p.describeProjects(ctx, scope)
	if len(sources) == 0 {
		return Answer{Text: NoChanges(lang, scope), Scope: scope}, nil
	}
	return p.answer(ctx, question, audience, lang, sources, scope, followingUp, ev)
}

func clampSinceDays(days int) int {
	switch {
	case days <= 0:
		return DefaultSinceDays
	case days > maxSinceDays:
		return maxSinceDays
	}
	return days
}

// commitSources turns the lane's rows into sources the answer step
// numbers, cites and stores. Text is the body: the subject is a field of its
// own so the chip can show it and the prompt can label it.
func commitSources(commits []history.Commit) []Source {
	out := make([]Source, 0, len(commits))
	for _, c := range commits {
		out = append(out, Source{
			Kind: SourceCommit, CommitID: c.ID, Repo: c.Repo, Branch: c.Branch, SHA: c.SHA,
			Subject: c.Subject, CommittedAt: c.CommittedAt, Paths: c.Paths, Text: c.Body,
			Reason: "hit",
		})
	}
	return out
}

// changesDetail is what the searching step found on a changes turn: the
// window, the topic, and the commits per repository.
func changesDetail(sources []Source, scope Scope) map[string]any {
	d := map[string]any{"commits": len(sources), "since_days": scope.SinceDays}
	if scope.Topic != "" {
		d["topic"] = scope.Topic
	}
	perRepo := map[string]int{}
	for _, s := range sources {
		perRepo[s.Repo]++
	}
	if len(perRepo) > 0 {
		d["per_repo"] = perRepo
	}
	return d
}

// commitBodyRunes and commitPathsShown bound one commit in the prompt. The
// gather budget does not apply on this path, and forty squash-merge bodies
// carrying whole pull-request descriptions, or one vendor drop listing ten
// thousand paths, would otherwise exceed the context in a single call and
// fail the turn instead of answering it. 1500 runes is the first two or
// three paragraphs of a message, where the reason is.
const (
	commitBodyRunes  = 1500
	commitPathsShown = 30
)

// renderCommit numbers one commit for the prompt, the way renderSources
// numbers a chunk: the marker, the repository, the short commit, the date,
// the subject, then the body and the paths it touched, both bounded.
func renderCommit(b *strings.Builder, n int, s Source) {
	fmt.Fprintf(b, "\n[%d] %s commit %s %s: %s\n", n, s.Repo, shortSHA(s.SHA),
		s.CommittedAt.UTC().Format("2006-01-02"), s.Subject)
	if s.Text != "" {
		body := s.Text
		if r := []rune(body); len(r) > commitBodyRunes {
			body = string(r[:commitBodyRunes]) + " [cut]"
		}
		b.WriteString(body)
		b.WriteString("\n")
	}
	if len(s.Paths) > 0 {
		b.WriteString("paths: ")
		if len(s.Paths) > commitPathsShown {
			b.WriteString(strings.Join(s.Paths[:commitPathsShown], ", "))
			fmt.Fprintf(b, " and %d more", len(s.Paths)-commitPathsShown)
		} else {
			b.WriteString(strings.Join(s.Paths, ", "))
		}
		b.WriteString("\n")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// changesBlock is the prompt rule for a changes turn: what the sources are
// and what shape the digest takes. It names the window and the topic so the
// opening sentence can say what was looked at, and it says the list may be
// cut so a busy window is not reported as the whole of it.
func changesBlock(scope Scope, audience Audience) string {
	// SinceDays is set only by answerChanges: a pipeline without the lane
	// answers a changes question from chunks, and must not be told they
	// are commits.
	if scope.Intent != IntentChanges || scope.SinceDays == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThe question asks WHAT CHANGED. The sources are commits of the last %d days",
		scope.SinceDays)
	if scope.Topic != "" {
		fmt.Fprintf(&b, ", filtered to those mentioning \"%s\"", scope.Topic)
	}
	b.WriteString(`, newest first, each with its date, subject, message and the paths it touched. Nothing else was read: the code itself is not among the sources, so describe what the commits say was done, never how the code works now.

Open with one sentence naming the window and the repositories covered, then the changes grouped by theme, newest first within a theme, each change with its date and its marker. A commit that carries a reason gives the reason. Where the sources span several repositories, say which change belongs to which. If the list holds ` + fmt.Sprint(changesLimit) + ` commits it was cut there; say so.`)
	if audience == AudienceBA {
		b.WriteString(` One line per change, in the language of the business domain: what the change means for the reader, not the files it touched.`)
	} else {
		b.WriteString(` Name the paths a change touched when they tell the reader where to look.`)
	}
	return b.String()
}

// noChanges is the answer when the window holds no commit, in the reader's
// language, templated like nothingFound: an answer with no sources never
// comes from a model. %d is the window, %s the repositories.
var noChanges = map[Language]string{
	LanguageEN: "No commits in the last %d days in %s.",
	LanguageDE: "Keine Commits in den letzten %d Tagen in %s.",
	LanguageFR: "Aucun commit dans les %d derniers jours dans %s.",
	LanguageIT: "Nessun commit negli ultimi %d giorni in %s.",
}

var noChangesTopic = map[Language]string{
	LanguageEN: "No commits about \"%s\" in the last %d days in %s.",
	LanguageDE: "Keine Commits zu \"%s\" in den letzten %d Tagen in %s.",
	LanguageFR: "Aucun commit concernant \"%s\" dans les %d derniers jours dans %s.",
	LanguageIT: "Nessun commit su \"%s\" negli ultimi %d giorni in %s.",
}

var allRepositories = map[Language]string{
	LanguageEN: "the indexed repositories",
	LanguageDE: "den indexierten Repositories",
	LanguageFR: "les dépôts indexés",
	LanguageIT: "i repository indicizzati",
}

// NoChanges is the answer for a window without commits.
func NoChanges(lang Language, scope Scope) string {
	l := ParseLanguage(string(lang))
	where := allRepositories[l]
	if len(scope.Known) > 0 {
		where = strings.Join(scope.Known, ", ")
	}
	if scope.Topic != "" {
		return fmt.Sprintf(noChangesTopic[l], scope.Topic, scope.SinceDays, where)
	}
	return fmt.Sprintf(noChanges[l], scope.SinceDays, where)
}
