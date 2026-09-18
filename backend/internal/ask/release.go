package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/stages"
)

// The release turn: "what is between production and testing, as release
// notes". The infrastructure repository's overlays name a version per image
// per stage; each image is declared on the repository it is built from
// (repos.Spec.Image); the two versions are tags of that repository; the
// commits between them are the commit lane's rows. Nothing is searched and
// nothing is inferred: the stages are declared, the image is declared, the
// tags are read from the checkout, and every version that cannot be turned
// into a range is one line saying why, never a guess.

// IntentRelease is the understanding's intent for a release question.
const IntentRelease = "release"

// ErrVersionUnknown is what a Releaser's ResolveTag wraps when a version
// names no tag and no commit: the release turn reads it as one line for
// that component, never as a failed turn.
var ErrVersionUnknown = errors.New("version not found")

// RepoHead is what the release turn needs to know about a repository's
// checkout: the commit the index was built from, the commit the remote
// branch is at, and whether the entry is a snapshot, which has no tags.
type RepoHead struct {
	SHA      string
	Remote   string
	Branch   string
	Snapshot bool
}

// Releaser is the checkout side of a release turn. An interface for the
// reason Histories is: a test drives every line of the matrix without git.
type Releaser interface {
	// ResolveTag turns a version as an image tag writes it into a commit
	// sha, or wraps ErrVersionUnknown.
	ResolveTag(ctx context.Context, repo, tag string) (string, error)
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error)
	// Range lists the first-parent shas of from..to, newest first, at most
	// limit of them.
	Range(ctx context.Context, repo, from, to string, limit int) ([]string, error)
	// Head is the repository's indexed and remote state.
	Head(ctx context.Context, repo string) (RepoHead, error)
	// Depth is how many first-parent commits the lane records per
	// repository (BACKEND_HISTORY_DEPTH).
	Depth() int
}

// WithReleases wires the checkout side. Without it a release question runs
// the ordinary pipeline, which is what it did before the lane existed.
func (p *Pipeline) WithReleases(r Releaser) *Pipeline {
	p.releases = r
	return p
}

func (p *Pipeline) isRelease(u Understanding) bool {
	return p.releases != nil && p.history != nil && u.Intent == IntentRelease
}

// The notes a component line can carry. Each is one templated sentence in
// the reader's language (releaseNotes) and one English clause in the
// prompt (noteEnglish); the empty note is a range with commits.
const (
	NoteUnchanged   = "unchanged"
	NoteUndeclared  = "undeclared"
	NoteMissing     = "missing"
	NoteAmbiguous   = "ambiguous"
	NoteDigest      = "digest"
	NoteSnapshot    = "snapshot"
	NoteTagUnknown  = "tag-unknown"
	NoteOffBranch   = "off-branch"
	NoteNotIndexed  = "not-indexed"
	NoteNoIndex     = "no-index"
	NoteDiverged    = "diverged"
	NoteBeyondDepth = "beyond-depth"
	NoteUnrecorded  = "unrecorded"
)

// ReleaseLine is one image of the infrastructure repository as the turn
// resolved it: the repository it is declared on (empty when none is), its
// version per stage, which stage is ahead, how many commits lie between,
// and the note saying why there is no range when there is none. Part of
// the record: a re-explain describes the same ranges.
type ReleaseLine struct {
	Image    string            `json:"image"`
	Repo     string            `json:"repo,omitempty"`
	Versions map[string]string `json:"versions,omitempty"`
	// Ahead is the stage whose version descends from the other's, or empty.
	Ahead string `json:"ahead,omitempty"`
	// Commits is the whole range's size, whatever the prompt was cut to.
	Commits int    `json:"commits,omitempty"`
	Note    string `json:"note,omitempty"`
	// Detail carries the note's argument: the unknown tag, the stage with
	// no version, the branch a tag is off.
	Detail string `json:"detail,omitempty"`
}

// releaseLimit caps one component's commits in the prompt and releaseTotal
// the turn's: release notes across five components need more than the
// changes lane's forty lines, and thirty per component is a release, not a
// changelog. The lines carry the true count.
const (
	releaseLimit = 30
	releaseTotal = 90
)

// answerRelease is the turn from the scope on: no search, no routing, no
// walk. The two stages and the project ARE the scope.
func (p *Pipeline) answerRelease(ctx context.Context, question string, audience Audience, lang Language,
	u Understanding, scope Scope, followingUp string, ev Events) (Answer, error) {

	declared := p.declaredStages(ctx)
	pair := releasePair(question, u.Between, declared)
	scope.Between = pair
	scope = p.describeProjects(ctx, scope)
	if len(pair) != 2 {
		return Answer{Text: releaseNeedsTwoStages(lang, declared), Scope: scope}, nil
	}
	pm, err := p.router.Projects(ctx)
	if err != nil {
		return Answer{}, fmt.Errorf("projects: %w", err)
	}
	infra, why := infraRepo(declared, pair, scope.Known, pm)
	if infra == "" {
		return Answer{Text: releaseNoInfra(lang, pair, why), Scope: scope}, nil
	}

	ev.status("searching")
	lines, commits, err := releaseLines(ctx, p.gatherer, p.releases, p.history, pm, infra, declared, pair)
	if err != nil {
		return Answer{}, err
	}
	scope.Release = lines
	sources := releaseSources(lines, commits)
	ev.detail("searching", releaseDetail(lines, sources, infra, pair))
	slog.Info("release", "thread", llm.ThreadID(ctx), "infra", infra, "between", pair,
		"images", len(lines), "commits", len(sources))
	if len(sources) == 0 {
		return Answer{Text: NoRelease(lang, pair, infra, lines), Scope: scope}, nil
	}
	return p.answer(ctx, question, audience, lang, sources, scope, followingUp, ev)
}

// releasePair settles the two stages: the reader's own words first, then
// the model's guesses, the first two distinct declared names. The two
// compose because they have to — "testing" is a refused stage word
// (repos.Load), so "production vs. testing" names one stage by word and
// the other only through the model's mapping. Order carries no meaning:
// ancestry decides per component which stage is ahead. Anything but two
// distinct declared names is no pair.
func releasePair(question string, guessed []string, declared stages.Set) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] && len(out) < 2 {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, name := range declared.Mentioned(question) {
		add(name)
	}
	for _, g := range guessed {
		if name, ok := declared.Resolve(g); ok {
			add(name)
		}
	}
	if len(out) != 2 {
		return nil
	}
	return out
}

// infraRepo is the one enabled repository declaring both stages, inside the
// projects the question named when it named any. None, or more than one, is
// no repository and a reason: with several projects each running its own
// stages the reader has to say which product, because a release turn has no
// search whose hits could tell.
func infraRepo(declared stages.Set, pair, known []string, pm projects.Map) (string, string) {
	has := map[string]map[string]bool{}
	for _, st := range declared {
		if has[st.Repo] == nil {
			has[st.Repo] = map[string]bool{}
		}
		has[st.Repo][st.Name] = true
	}
	wanted := map[string]bool{}
	for _, k := range known {
		wanted[pm.Of(k)] = true
	}
	var candidates []string
	for repo, names := range has {
		if !names[pair[0]] || !names[pair[1]] {
			continue
		}
		if len(wanted) > 0 && !wanted[pm.Of(repo)] {
			continue
		}
		candidates = append(candidates, repo)
	}
	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", "none"
	case 1:
		return candidates[0], ""
	}
	return "", strings.Join(candidates, ", ")
}

// stageVersion is one image's version under one stage: a tag, a digest, or
// several values that disagree.
type stageVersion struct {
	tag, digest string
	ambiguous   []string
}

// imageCensus reads the versions the infrastructure repository deploys
// under one stage's prefix: image name -> version. A kustomize images:
// entry (a kustomization file) wins over an image: line in a manifest of
// the same overlay, because the transformer rewrites the manifest; two
// entries of one class naming different versions are ambiguous, and the
// base overlay is never read (repos.yaml declares which directories are
// stages, and a base may hold a placeholder).
func imageCensus(ctx context.Context, g *Gatherer, repo, prefix string) (map[string]stageVersion, error) {
	rows, err := edges.InRepo(ctx, g.db, repo, edges.KindImage)
	if err != nil {
		return nil, fmt.Errorf("read the images of %s: %w", repo, err)
	}
	type seen struct{ kustomize, inline map[string]bool }
	by := map[string]*seen{}
	for _, n := range rows {
		if !strings.HasPrefix(n.Path, prefix) {
			continue
		}
		name, version := splitImage(n.Value)
		if name == "" || version == "" {
			continue
		}
		s := by[name]
		if s == nil {
			s = &seen{kustomize: map[string]bool{}, inline: map[string]bool{}}
			by[name] = s
		}
		if isKustomization(n.Path) {
			s.kustomize[version] = true
		} else {
			s.inline[version] = true
		}
	}
	out := map[string]stageVersion{}
	for name, s := range by {
		versions := s.kustomize
		if len(versions) == 0 {
			versions = s.inline
		}
		var vs []string
		for v := range versions {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		switch {
		case len(vs) > 1:
			out[name] = stageVersion{ambiguous: vs}
		case strings.HasPrefix(vs[0], "@"):
			out[name] = stageVersion{digest: vs[0][1:]}
		default:
			out[name] = stageVersion{tag: vs[0][1:]}
		}
	}
	return out, nil
}

func (v stageVersion) empty() bool {
	return v.tag == "" && v.digest == "" && len(v.ambiguous) == 0
}

// splitImage cuts "name:tag" into name and ":tag", "name@digest" into name
// and "@digest", so the separator travels with the version and the two
// kinds stay apart. The tag is what follows the last "/".
func splitImage(ref string) (name, version string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at], ref[at:]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return ref, ""
	}
	return ref[:colon], ref[colon:]
}

func isKustomization(p string) bool {
	base := strings.ToLower(path.Base(p))
	return base == "kustomization.yaml" || base == "kustomization.yml" || base == "kustomization"
}

// releaseLines resolves every image the infrastructure repository deploys
// under either stage into one line, and returns the commits of the lines
// that have a range, keyed by image. The matrix, in the order it is asked:
//
//	no repository declares it    undeclared
//	several versions under one   ambiguous
//	no version under one prefix  missing
//	same version                 unchanged
//	pinned by digest             digest
//	the repository is a snapshot snapshot
//	a version names no tag       tag-unknown
//	the repository never indexed no-index
//	a tag off the indexed branch off-branch, or not-indexed when the
//	                             remote branch has it and the index not yet
//	ancestry                     which stage is ahead, or diverged
//	range past the depth         beyond-depth
//	a sha the lane never recorded unrecorded
func releaseLines(ctx context.Context, g *Gatherer, rel Releaser, h Histories, pm projects.Map,
	infra string, declared stages.Set, pair []string) ([]ReleaseLine, map[string][]history.Commit, error) {

	prefixes := map[string]string{}
	for _, st := range declared {
		if st.Repo == infra && (st.Name == pair[0] || st.Name == pair[1]) {
			prefixes[st.Name] = st.Prefix
		}
	}
	census := map[string]map[string]stageVersion{}
	images := map[string]bool{}
	for _, stage := range pair {
		c, err := imageCensus(ctx, g, infra, prefixes[stage])
		if err != nil {
			return nil, nil, err
		}
		census[stage] = c
		for name := range c {
			images[name] = true
		}
	}
	project, _ := pm.Project(pm.Of(infra))
	repoOf := map[string]string{}
	for _, m := range project.Members {
		if m.Image != "" {
			repoOf[m.Image] = m.Name
		}
	}
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)

	var lines []ReleaseLine
	commits := map[string][]history.Commit{}
	for _, name := range names {
		line := ReleaseLine{Image: name, Repo: repoOf[name], Versions: map[string]string{}}
		a, b := census[pair[0]][name], census[pair[1]][name]
		for stage, v := range map[string]stageVersion{pair[0]: a, pair[1]: b} {
			switch {
			case v.tag != "":
				line.Versions[stage] = v.tag
			case v.digest != "":
				line.Versions[stage] = "@" + shortDigest(v.digest)
			case len(v.ambiguous) > 0:
				line.Versions[stage] = strings.Join(v.ambiguous, " / ")
			}
		}
		cs, err := resolveLine(ctx, rel, h, pair, a, b, &line)
		if err != nil {
			return nil, nil, err
		}
		if len(cs) > 0 {
			commits[name] = cs
		}
		lines = append(lines, line)
	}
	return lines, commits, nil
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// resolveLine walks the matrix for one image and fills the line's note,
// ahead and count; it returns the commits of a forward range.
func resolveLine(ctx context.Context, rel Releaser, h Histories, pair []string, a, b stageVersion, line *ReleaseLine) ([]history.Commit, error) {
	switch {
	case line.Repo == "":
		// First, whatever the versions say: an image nobody declared is a
		// gap in repos.yaml the operator should see, unchanged or not.
		line.Note = NoteUndeclared
		return nil, nil
	case len(a.ambiguous) > 0 || len(b.ambiguous) > 0:
		line.Note = NoteAmbiguous
		return nil, nil
	case a.empty() || b.empty():
		line.Note = NoteMissing
		line.Detail = pair[0]
		if !a.empty() {
			line.Detail = pair[1]
		}
		return nil, nil
	case a.tag == b.tag && a.digest == b.digest:
		line.Note = NoteUnchanged
		return nil, nil
	case a.digest != "" || b.digest != "":
		line.Note = NoteDigest
		return nil, nil
	}
	head, err := rel.Head(ctx, line.Repo)
	if err != nil {
		return nil, fmt.Errorf("head of %s: %w", line.Repo, err)
	}
	if head.Snapshot {
		line.Note = NoteSnapshot
		return nil, nil
	}
	// A repository the poller has not indexed yet — the first run after a
	// forced re-index empties every last_sha — has no head to measure a
	// tag against, and asking git about "" is an error, not a verdict.
	if head.SHA == "" {
		line.Note = NoteNoIndex
		return nil, nil
	}
	versions := map[string]stageVersion{pair[0]: a, pair[1]: b}
	shas := map[string]string{}
	// Over the pair, never over the map: with both tags unknown the detail
	// must name the same one on every call.
	for _, stage := range pair {
		v := versions[stage]
		sha, err := rel.ResolveTag(ctx, line.Repo, v.tag)
		if errors.Is(err, ErrVersionUnknown) {
			line.Note, line.Detail = NoteTagUnknown, v.tag
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("resolve %s of %s: %w", v.tag, line.Repo, err)
		}
		shas[stage] = sha
	}
	// Two spellings of one tag ("2024.3.1" and "v2024.3.1") are one version.
	if shas[pair[0]] == shas[pair[1]] {
		line.Note = NoteUnchanged
		return nil, nil
	}
	// Both tags must lie on the indexed history. A tag the remote branch
	// holds but the index does not yet is the poller lagging a push, which
	// is a different sentence from a tag on a side branch.
	for _, stage := range pair {
		sha := shas[stage]
		indexed, err := rel.IsAncestor(ctx, line.Repo, sha, head.SHA)
		if err != nil {
			return nil, fmt.Errorf("ancestry in %s: %w", line.Repo, err)
		}
		if indexed {
			continue
		}
		remote, err := rel.IsAncestor(ctx, line.Repo, sha, head.Remote)
		if err != nil {
			return nil, fmt.Errorf("ancestry in %s: %w", line.Repo, err)
		}
		if remote {
			line.Note, line.Detail = NoteNotIndexed, line.Versions[stage]
		} else {
			line.Note, line.Detail = NoteOffBranch, line.Versions[stage]+" "+head.Branch
		}
		return nil, nil
	}
	// Direction from ancestry, never from the order the stages were named:
	// the pair is a set, and "between testing and production" must read the
	// same as the reverse. The stage whose tag descends from the other's is
	// ahead, and the range runs from the older tag to it. Without a
	// declared promotion order nothing here can call a direction a
	// rollback, so it does not.
	from, to := shas[pair[0]], shas[pair[1]]
	line.Ahead = pair[1]
	forward, err := rel.IsAncestor(ctx, line.Repo, from, to)
	if err != nil {
		return nil, fmt.Errorf("ancestry in %s: %w", line.Repo, err)
	}
	if !forward {
		back, err := rel.IsAncestor(ctx, line.Repo, to, from)
		if err != nil {
			return nil, fmt.Errorf("ancestry in %s: %w", line.Repo, err)
		}
		if !back {
			line.Note, line.Ahead = NoteDiverged, ""
			return nil, nil
		}
		from, to = to, from
		line.Ahead = pair[0]
	}
	// Sized FIRST, at the whole depth: a cap applied before the check would
	// always pass it, because the newest commits always have rows.
	depth := rel.Depth()
	shasBetween, err := rel.Range(ctx, line.Repo, from, to, depth+1)
	if err != nil {
		return nil, fmt.Errorf("range %s..%s of %s: %w", from, to, line.Repo, err)
	}
	line.Commits = len(shasBetween)
	if len(shasBetween) > depth {
		line.Note, line.Detail = NoteBeyondDepth, fmt.Sprint(depth)
		return nil, nil
	}
	rows, err := h.BySHAs(ctx, line.Repo, shasBetween)
	if err != nil {
		return nil, fmt.Errorf("commits of %s: %w", line.Repo, err)
	}
	// A sha with no row is not necessarily depth: the lane holds the head's
	// first-parent chain, and a tag on a merged hotfix commit walks a
	// first-parent line of its own. Either way the commits are not in the
	// record, and the line says that rather than blaming the depth.
	if len(rows) != len(shasBetween) {
		line.Note, line.Detail = NoteUnrecorded, fmt.Sprint(len(shasBetween)-len(rows))
		return nil, nil
	}
	return rows, nil
}

// releaseSources numbers the commits for the answer, component by
// component in line order, newest first inside one, capped per component
// and overall. A cut is said in the prompt, and the line keeps the count.
func releaseSources(lines []ReleaseLine, commits map[string][]history.Commit) []Source {
	var out []Source
	for _, l := range lines {
		cs := commits[l.Image]
		if len(cs) > releaseLimit {
			cs = cs[:releaseLimit]
		}
		if room := releaseTotal - len(out); len(cs) > room {
			cs = cs[:room]
		}
		reason := fmt.Sprintf("release:%s %s..%s", l.Image, l.Versions[other(l, l.Ahead)], l.Versions[l.Ahead])
		for _, s := range commitSources(cs) {
			s.Reason = reason
			out = append(out, s)
		}
	}
	return out
}

// other is the stage of the line that is not ahead.
func other(l ReleaseLine, ahead string) string {
	for stage := range l.Versions {
		if stage != ahead {
			return stage
		}
	}
	return ""
}

// releaseDetail is what the searching step found: commits is the whole of
// every range, shown how many of them the prompt holds after the caps.
func releaseDetail(lines []ReleaseLine, sources []Source, infra string, pair []string) map[string]any {
	d := map[string]any{"infrastructure": infra, "between": pair, "images": len(lines), "shown": len(sources)}
	perRepo := map[string]int{}
	notes := map[string]int{}
	total := 0
	for _, l := range lines {
		if l.Commits > 0 && l.Note == "" {
			perRepo[l.Repo] = l.Commits
			total += l.Commits
		}
		if l.Note != "" {
			notes[l.Note]++
		}
	}
	d["commits"] = total
	if len(perRepo) > 0 {
		d["per_repo"] = perRepo
	}
	if len(notes) > 0 {
		d["notes"] = notes
	}
	return d
}

// releaseListing renders the lines for the prompt, one per image, in
// English: the prompt is model-internal, and the model restates it in the
// reader's language.
func releaseListing(lines []ReleaseLine, pair []string) string {
	var b strings.Builder
	for _, l := range lines {
		who := l.Repo
		if who == "" {
			who = l.Image
		}
		fmt.Fprintf(&b, "%s: %s %s, %s %s, %s\n", who,
			pair[0], orNone(l.Versions[pair[0]]), pair[1], orNone(l.Versions[pair[1]]), noteEnglish(l))
	}
	return b.String()
}

func orNone(v string) string {
	if v == "" {
		return "no version"
	}
	return v
}

// noteEnglish is the line's clause for the prompt.
func noteEnglish(l ReleaseLine) string {
	switch l.Note {
	case "":
		n := "commit"
		if l.Commits != 1 {
			n = "commits"
		}
		cut := ""
		if l.Commits > releaseLimit {
			cut = fmt.Sprintf(" (the newest %d are among the sources)", releaseLimit)
		}
		return fmt.Sprintf("%s is ahead by %d %s%s", l.Ahead, l.Commits, n, cut)
	case NoteUnchanged:
		return "unchanged"
	case NoteUndeclared:
		return "no repository declares this image, so its history was not read"
	case NoteMissing:
		return fmt.Sprintf("no version under the %s overlay, so nothing to compare", l.Detail)
	case NoteAmbiguous:
		return "named with two versions in one overlay, so nothing to compare"
	case NoteDigest:
		return "pinned by digest, not a version, so nothing to compare"
	case NoteSnapshot:
		return "the repository is a snapshot with no history"
	case NoteTagUnknown:
		return fmt.Sprintf("version %s is no tag of the repository", l.Detail)
	case NoteOffBranch:
		parts := strings.SplitN(l.Detail, " ", 2)
		return fmt.Sprintf("version %s is not on the indexed branch %s", parts[0], strings.Join(parts[1:], ""))
	case NoteNotIndexed:
		return fmt.Sprintf("version %s is not indexed yet", l.Detail)
	case NoteNoIndex:
		return "the repository is not indexed yet"
	case NoteDiverged:
		return "the two versions diverged, neither descends from the other"
	case NoteBeyondDepth:
		return fmt.Sprintf("%d commits apart, older than the recorded history of %s, so not summarised", l.Commits, l.Detail)
	case NoteUnrecorded:
		return fmt.Sprintf("%d commits apart, %s of them not in the recorded history, so not summarised", l.Commits, l.Detail)
	}
	return l.Note
}

// releaseBlock is the prompt rule for a release turn.
func releaseBlock(scope Scope, audience Audience) string {
	if scope.Intent != IntentRelease || len(scope.Between) != 2 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThe question asks for RELEASE NOTES: what lies between two deployed versions, %s and %s, of each component of the product. The sources are commits between the two versions, per component, newest first, each with its date, subject, message and the paths it touched. Nothing else was read: the code itself is not among the sources, so describe what the commits say was done, never how the code works now.\n\nThe components, read from the infrastructure repository's overlays, one per line as \"repository: stage version, stage version, verdict\":\n\n%s\nOpen with one sentence naming the two stages and the components that differ. Then, per component that has commits, its two versions, then the changes grouped by theme, newest first within a theme, each with its date and its marker. A commit that carries a reason gives the reason. Close with one paragraph naming the components that are unchanged and those with a verdict other than commits, in the words of the list above; say nothing more about them, and never guess what a version not among the sources contains. A component whose list was cut is said to be cut.",
		scope.Between[0], scope.Between[1], releaseListing(scope.Release, scope.Between))
	if audience == AudienceBA {
		b.WriteString(` One line per change, in the language of the business domain: what the change means for the reader, not the files it touched.`)
	} else {
		b.WriteString(` Name the paths a change touched when they tell the reader where to look.`)
	}
	return b.String()
}

// The templated answers, in the reader's language: an answer with no
// sources never comes from a model.
var releaseNoRange = map[Language]string{
	LanguageEN: "No commits between %s and %s in %s.",
	LanguageDE: "Keine Commits zwischen %s und %s in %s.",
	LanguageFR: "Aucun commit entre %s et %s dans %s.",
	LanguageIT: "Nessun commit tra %s e %s in %s.",
}

var releaseTwoStages = map[Language]string{
	LanguageEN: "Release notes need two deployment stages to compare; declared are: %s.",
	LanguageDE: "Release Notes brauchen zwei Stages zum Vergleich; deklariert sind: %s.",
	LanguageFR: "Les notes de version demandent deux environnements à comparer ; déclarés : %s.",
	LanguageIT: "Le note di rilascio richiedono due ambienti da confrontare; dichiarati: %s.",
}

var releaseNoInfraNone = map[Language]string{
	LanguageEN: "No repository declares both stages %s and %s.",
	LanguageDE: "Kein Repository deklariert beide Stages %s und %s.",
	LanguageFR: "Aucun dépôt ne déclare les deux environnements %s et %s.",
	LanguageIT: "Nessun repository dichiara entrambi gli ambienti %s e %s.",
}

var releaseNoInfraMany = map[Language]string{
	LanguageEN: "Several repositories declare the stages %s and %s (%s); name the product.",
	LanguageDE: "Mehrere Repositories deklarieren die Stages %s und %s (%s); nenne das Produkt.",
	LanguageFR: "Plusieurs dépôts déclarent les environnements %s et %s (%s) ; nommez le produit.",
	LanguageIT: "Più repository dichiarano gli ambienti %s e %s (%s); indica il prodotto.",
}

// releaseNotes is one sentence per note, "%s" the component and a detail.
var releaseNotes = map[string]map[Language]string{
	NoteUnchanged: {
		LanguageEN: "%s: unchanged.", LanguageDE: "%s: unverändert.",
		LanguageFR: "%s : inchangé.", LanguageIT: "%s: invariato."},
	NoteUndeclared: {
		LanguageEN: "%s: no repository declares this image.", LanguageDE: "%s: kein Repository deklariert dieses Image.",
		LanguageFR: "%s : aucun dépôt ne déclare cette image.", LanguageIT: "%s: nessun repository dichiara questa immagine."},
	NoteMissing: {
		LanguageEN: "%s: no version under %s.", LanguageDE: "%s: keine Version unter %s.",
		LanguageFR: "%s : aucune version sous %s.", LanguageIT: "%s: nessuna versione sotto %s."},
	NoteAmbiguous: {
		LanguageEN: "%s: two versions in one overlay.", LanguageDE: "%s: zwei Versionen in einem Overlay.",
		LanguageFR: "%s : deux versions dans un même overlay.", LanguageIT: "%s: due versioni in un overlay."},
	NoteDigest: {
		LanguageEN: "%s: pinned by digest, not a version.", LanguageDE: "%s: per Digest fixiert, keine Version.",
		LanguageFR: "%s : figé par digest, pas de version.", LanguageIT: "%s: fissato per digest, nessuna versione."},
	NoteSnapshot: {
		LanguageEN: "%s: a snapshot with no history.", LanguageDE: "%s: ein Snapshot ohne Historie.",
		LanguageFR: "%s : un instantané sans historique.", LanguageIT: "%s: uno snapshot senza cronologia."},
	NoteTagUnknown: {
		LanguageEN: "%s: version %s is no tag of the repository.", LanguageDE: "%s: Version %s ist kein Tag des Repositories.",
		LanguageFR: "%s : la version %s n'est pas un tag du dépôt.", LanguageIT: "%s: la versione %s non è un tag del repository."},
	NoteOffBranch: {
		LanguageEN: "%s: version %s is not on the indexed branch.", LanguageDE: "%s: Version %s liegt nicht auf dem indexierten Branch.",
		LanguageFR: "%s : la version %s n'est pas sur la branche indexée.", LanguageIT: "%s: la versione %s non è sul branch indicizzato."},
	NoteNotIndexed: {
		LanguageEN: "%s: version %s is not indexed yet.", LanguageDE: "%s: Version %s ist noch nicht indexiert.",
		LanguageFR: "%s : la version %s n'est pas encore indexée.", LanguageIT: "%s: la versione %s non è ancora indicizzata."},
	NoteNoIndex: {
		LanguageEN: "%s: not indexed yet.", LanguageDE: "%s: noch nicht indexiert.",
		LanguageFR: "%s : pas encore indexé.", LanguageIT: "%s: non ancora indicizzato."},
	NoteDiverged: {
		LanguageEN: "%s: the two versions diverged.", LanguageDE: "%s: die beiden Versionen sind divergiert.",
		LanguageFR: "%s : les deux versions ont divergé.", LanguageIT: "%s: le due versioni sono divergenti."},
	NoteBeyondDepth: {
		LanguageEN: "%s: older than the recorded history of %s commits.", LanguageDE: "%s: älter als die aufgezeichnete Historie von %s Commits.",
		LanguageFR: "%s : plus ancien que l'historique enregistré de %s commits.", LanguageIT: "%s: più vecchio della cronologia registrata di %s commit."},
	NoteUnrecorded: {
		LanguageEN: "%s: %s commits between the versions are not in the recorded history.", LanguageDE: "%s: %s Commits zwischen den Versionen sind nicht in der aufgezeichneten Historie.",
		LanguageFR: "%s : %s commits entre les versions ne sont pas dans l'historique enregistré.", LanguageIT: "%s: %s commit tra le versioni non sono nella cronologia registrata."},
}

// NoRelease is the answer when no component has a range: the sentence,
// then one line per component saying why.
func NoRelease(lang Language, pair []string, infra string, lines []ReleaseLine) string {
	l := ParseLanguage(string(lang))
	var b strings.Builder
	fmt.Fprintf(&b, releaseNoRange[l], pair[0], pair[1], infra)
	for _, line := range lines {
		who := line.Repo
		if who == "" {
			who = line.Image
		}
		tmpl, ok := releaseNotes[line.Note][l]
		if !ok {
			continue
		}
		b.WriteString("\n")
		switch line.Note {
		case NoteMissing, NoteTagUnknown, NoteNotIndexed, NoteBeyondDepth, NoteUnrecorded:
			fmt.Fprintf(&b, tmpl, who, line.Detail)
		case NoteOffBranch:
			fmt.Fprintf(&b, tmpl, who, strings.SplitN(line.Detail, " ", 2)[0])
		default:
			fmt.Fprintf(&b, tmpl, who)
		}
	}
	return b.String()
}

func releaseNeedsTwoStages(lang Language, declared stages.Set) string {
	l := ParseLanguage(string(lang))
	names := declared.Names()
	if len(names) == 0 {
		names = []string{"-"}
	}
	return fmt.Sprintf(releaseTwoStages[l], strings.Join(names, ", "))
}

func releaseNoInfra(lang Language, pair []string, why string) string {
	l := ParseLanguage(string(lang))
	if why == "none" {
		return fmt.Sprintf(releaseNoInfraNone[l], pair[0], pair[1])
	}
	return fmt.Sprintf(releaseNoInfraMany[l], pair[0], pair[1], why)
}
