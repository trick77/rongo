package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/projects"
)

// defaultCandidates is how many rows each lane retrieves before fusion. Fusion
// can only rank what the lanes handed it, so this is the real recall ceiling;
// the caller's K only trims the fused list.
const defaultCandidates = 40

// Embedder turns query text into vectors. It is the same interface the indexer
// uses, so the query and the corpus are embedded by the same client and the
// same model — anything else compares vectors from two different spaces.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Query is one search.
type Query struct {
	Text string
	// Texts replaces Text when set: the question phrased several ways, each
	// getting its own semantic lane before fusion. The understanding step fills
	// it with the business-language restatement and the guessed code
	// vocabulary, which is the bridge a raw question does not build — "Apple TV"
	// never embeds near "AirPlay" on its own.
	//
	// The raw question belongs in here too, first. A model's guess is a guess,
	// and a wrong one must not be able to replace what was actually asked.
	Texts []string
	// Code is the code-terms text out of Texts, named rather than positional:
	// the keyword lane can then weigh "one of these identifiers appears here"
	// differently from the same rung over the question's prose. Empty when the
	// understanding step guessed no identifiers, and inert unless the
	// Retriever's CodeWeight is set.
	Code string
	// Repos is the understanding step's GUESS at which repositories the
	// question is about. It is not the restriction on its own: knownRepos
	// unions it with the repositories Question names, so an empty Repos can
	// still narrow the search and a filled one never bounds it alone. Both
	// empty means the whole corpus.
	Repos []string
	// Question is the raw question, used to read repository names out of the
	// reader's own words; see knownRepos. It duplicates Texts[0] on purpose:
	// "the raw question" is a fact about the query, not a position in a slice,
	// and a restriction must not depend on which lane happens to be first.
	Question string
	// Prior is the question this thread asked a turn ago, or empty on a first
	// turn. It reaches the RERANKER and nothing else: a follow-up naming its
	// subject nowhere - "in welchem Formularschritt passiert das?" - is a
	// question the reranker cannot read either, and judging sixty chunks
	// against it reads the ones the thread is actually about as irrelevant.
	// The lane pulls them up and the cut would drop them again.
	//
	// Not part of the restriction: knownRepos reads Question alone, so a
	// thread's older question can never widen what this turn searches.
	Prior string
	// K is how many hits to return. Zero means 10.
	K int
	// Stage narrows the repositories that declare stages to one stage's
	// directory; see StagePrefixes. Nil is every stage. Unlike Repos it is
	// never a guess: it is built from the reader's own words against the
	// declared stage names, and a repository declaring no stages is never
	// touched by it.
	Stage StagePrefixes
}

// texts is what the lanes actually search for.
func (q Query) texts() []string {
	if len(q.Texts) > 0 {
		return q.Texts
	}
	return []string{q.Text}
}

// Retriever answers a Query by fusing the semantic and keyword lanes.
type Retriever struct {
	store    *Store
	embedder Embedder
	// MaxDistance bounds the semantic lane; see DefaultMaxDistance. Negative
	// disables it, which makes an empty result impossible.
	MaxDistance float64
	// Candidates is how many rows each lane fetches before fusion.
	Candidates int
	// RepoDecay demotes a repository's repeated hits so a second repository
	// has room in the cut; see FuseWeightedDiverse. 1 is off, which is what
	// ships until the evaluation names a value.
	RepoDecay float64
	// TestDecay cuts a test hit's fused score; see DefaultTestDecay. 1 — and
	// the zero value, so a struct-literal Retriever keeps the behaviour that
	// shipped before it existed — is off.
	TestDecay float64
	// DocDecay cuts a documentation hit's fused score; see DefaultDocDecay.
	// Reads its zero the way TestDecay does.
	DocDecay float64
	// CodeWeight, when above zero, gives the OR rung of the CODE-TERMS text a
	// weight of its own instead of the prose floor; see WeightKeywordCode.
	// New ships it at 0.8; zero — and so a struct-literal Retriever — is the
	// prose floor, which is what the harness's baseline arm runs.
	CodeWeight float64
	// SubstringWeight, when above zero, runs the substring rung: a scan for
	// identifier-shaped terms that occur only INSIDE a larger token, which the
	// FTS lane cannot reach at all. See WeightKeywordSubstring and
	// Store.SearchSubstringIn.
	//
	// New ships it on; zero — and so a struct-literal Retriever — leaves the
	// rung off, which is the arm the harness measures the baseline with.
	SubstringWeight float64
	// Reranker, when set, reorders a deeper fused list before the cut to K;
	// see LLMReranker. The product sets it; nil is the fused order as it has
	// always been, and the eval harness's baseline.
	Reranker *LLMReranker
}

// New builds a Retriever with the default bounds.
func New(db *sql.DB, embedder Embedder) *Retriever {
	return &Retriever{
		store:       NewStore(db),
		embedder:    embedder,
		MaxDistance: DefaultMaxDistance,
		Candidates:  defaultCandidates,
		RepoDecay:   DefaultRepoDecay,
		TestDecay:   DefaultTestDecay,
		DocDecay:    DefaultDocDecay,
		CodeWeight:  WeightKeywordCode,

		SubstringWeight: WeightKeywordSubstring,
	}
}

// Substring scans the raw source for one literal term and returns the chunks
// it occurs in, at most n of them.
//
// For a caller that means "find this exact text" — the locate loop's grep
// tool, which tells the model that nothing found means the spelling was
// wrong — the term is scanned as written, with nothing derived from it.
//
// The hub guard inside SearchSubstringIn still applies: a term in more than
// a fiftieth of the corpus comes back empty. The loop reads that as "not
// found" and re-spells a name that was right, which is worth telling apart —
// but the counting query that would tell them apart is not on this branch, so
// for now the two look the same to the caller.
func (r *Retriever) Substring(ctx context.Context, term string, n int, repos []string, question string, stage StagePrefixes) ([]Hit, error) {
	known, err := r.knownRepos(ctx, repos, question)
	if err != nil {
		return nil, err
	}
	return r.store.SearchSubstringIn(ctx, term, n, known, stage)
}

// Search runs both lanes and fuses them.
//
// No match anywhere returns an EMPTY SLICE and NO ERROR. "No hit means no hit"
// is an answer the caller reports along with the terms it tried; an error here
// would be indistinguishable from a broken database, and the answer layer would
// have to guess which one it was looking at.
func (r *Retriever) Search(ctx context.Context, q Query) ([]Hit, error) {
	texts := q.texts()
	// The rung is found by comparing the code text against the texts being
	// searched, so a Code that is not one of them switches it off silently —
	// and a caller that built the two separately would never notice. Says so
	// once, rather than answering from a lane the operator thinks is running.
	if q.Code != "" && !slices.Contains(texts, q.Code) {
		slog.Warn("code text is not among the query texts, the code rung is off for this search",
			"code", q.Code, "texts", len(texts))
	}
	repos, err := r.knownRepos(ctx, q.Repos, q.Question)
	if err != nil {
		return nil, err
	}
	return r.searchTexts(ctx, texts, q.Code, q.Prior, repos, q.K, q.Stage)
}

// ResolveRepos sorts what a question said about repositories into the names
// the index carries and the names it does not.
//
// Search resolves the first half for itself and keeps the result — which is
// enough to search with and not enough to answer with. Two decisions upstream
// need the halves by name: a question naming two indexed repositories is
// asking for both and must not be turned into a card, and a name the index
// does not carry has to be said out loud rather than silently dropped, or the
// turn answers about code the reader did not ask about.
//
// known is knownRepos' own answer — the guess unioned with what the question
// names as a whole word — so the rung upstream fires on exactly the
// restriction the search ran under, never on a second reading of the same
// sentence.
//
// unknown is the narrow part, and keeping it narrow is what stops this
// becoming a machine for making false statements. knownRepos records what the
// guess measured like: of nine guesses, "peeqs" was the possessive of a real
// repository and "Peek" a plain mishearing. Saying "no repository called Peek
// is indexed", and then telling the answer model it knows nothing about Peek
// with peeq's code in front of it, is worse than the silence this replaced.
// So a guess that is only a misspelling of an indexed name is dropped exactly
// as before: not known, so it narrows nothing, and not unknown, so nothing is
// claimed about it. Only a name resembling nothing in the index is reported.
func (r *Retriever) ResolveRepos(ctx context.Context, want []string, question string) (known, unknown []string, err error) {
	// Both halves of the addressable namespace, read once for both answers:
	// a reader may name a repository or the project it belongs to, and
	// neither is a name the index lacks.
	repos, project, part, err := r.reposAndProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	known = knownReposIn(want, question, repos, project)
	if len(want) == 0 {
		return known, nil, nil
	}
	known = withGluedParts(known, want, repos, part)
	indexed := repos
	for p := range project {
		indexed = append(indexed, p)
	}

	resolved := map[string]bool{}
	for _, n := range known {
		resolved[foldRepo(n)] = true
	}
	// A project resolves through its MEMBERS, so its own name never appears in
	// known. Without this line a turn that searched "shop" correctly would
	// carry a notice saying the index has no repository called shop.
	for p := range project {
		resolved[foldRepo(p)] = true
	}
	// Walked over want, not over the index: the guess's order is the order the
	// question used, and a repeated guess must not become two notices.
	seen := map[string]bool{}
	for _, n := range want {
		folded := foldRepo(n)
		if resolved[folded] || seen[folded] {
			// Already searched under the name the index uses, whatever the
			// question spelled it: case, a possessive, an owner prefix.
			continue
		}
		seen[folded] = true
		if _, ok := repoWithPart(n, part); ok {
			// An indexed repository with its part glued on, already in known.
			continue
		}
		if nearAnyRepo(folded, indexed) {
			// A mishearing. Dropped in silence, exactly as before.
			continue
		}
		if segmentOfAnyRepo(folded, indexed) && !namesAsWholeWord(question, n) {
			// The model split an indexed name and named the half. Dropped in
			// silence: the reader never wrote it.
			continue
		}
		unknown = append(unknown, n)
	}
	return known, unknown, nil
}

// repoWithPart reports the repository a guess names when the guess is that
// repository's name and its DECLARED part glued together, in either order:
// "Wie wird im Schadenmeldung-service backend …" came back from the
// understanding step as schadenmeldung-service-backend. Neither a mishearing
// nor a segment, so it was reported as a repository the index lacks, beside
// an answer written from that very repository.
//
// Only the declared part, never any trailing word: a guess the part does not
// explain still falls through to the rules below and is reported.
func repoWithPart(guess string, part map[string]string) (string, bool) {
	g := strings.ToLower(strings.TrimSpace(guess))
	for repo, p := range part {
		if p == "" {
			continue
		}
		name, p := strings.ToLower(repo), strings.ToLower(p)
		for _, sep := range []string{"-", "_", " "} {
			if g == name+sep+p || g == p+sep+name {
				return repo, true
			}
		}
	}
	return "", false
}

// withGluedParts adds to known every repository a guess names with its part
// glued on, kept in the index's order like the rest of known.
func withGluedParts(known, want, repos []string, part map[string]string) []string {
	in := map[string]bool{}
	for _, n := range known {
		in[n] = true
	}
	added := false
	for _, w := range want {
		if repo, ok := repoWithPart(w, part); ok && !in[repo] {
			in[repo] = true
			added = true
		}
	}
	if !added {
		return known
	}
	out := make([]string, 0, len(in))
	for _, n := range repos {
		if in[n] {
			out = append(out, n)
		}
	}
	return out
}

// foldRepo normalises a name for comparison: lower case (the column is a TEXT
// PRIMARY KEY, so SQL compares it byte for byte), the owner prefix of
// "asg017/sqlite-vec" dropped, and a trailing possessive or plural removed.
func foldRepo(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, "'s")
	s = strings.TrimSuffix(s, "’s")
	return strings.TrimSuffix(s, "s")
}

// nearAnyRepo reports whether a folded guess is within one edit of an indexed
// name — near enough to be a mishearing of it rather than a repository the
// index is missing.
//
// The floor of four characters keeps the rule from swallowing short names that
// genuinely differ. It leans towards saying nothing: a near miss dropped in
// silence behaves exactly as it did before the notice existed, while a false
// "not indexed" is a claim the reader has no way to check.
func nearAnyRepo(folded string, indexed []string) bool {
	if len([]rune(folded)) < 4 {
		return false
	}
	for _, n := range indexed {
		if withinOneEdit(folded, foldRepo(n)) {
			return true
		}
	}
	return false
}

// segmentOfAnyRepo reports whether a folded guess is one "-" or "_" separated
// segment of an indexed name - "transmission" out of "transmission-ui".
//
// That shape is not a mishearing, it is a reading: the model is told to name
// the repositories a question names, is shown no list of the ones that exist,
// and takes a hyphenated name apart the way a person would, into a UI and the
// daemon it must be a UI for. The half it invents resembles nothing in the
// index by edit distance, so nearAnyRepo lets it through, and the reader is
// told their index is missing a repository they never mentioned.
//
// Same four-rune floor as nearAnyRepo, for the same reason: a segment shorter
// than that ("ui", "api") is too weak a coincidence to suppress on.
func segmentOfAnyRepo(folded string, indexed []string) bool {
	if len([]rune(folded)) < 4 {
		return false
	}
	for _, n := range indexed {
		segs := strings.FieldsFunc(foldRepo(n), func(r rune) bool {
			return r == '-' || r == '_'
		})
		if len(segs) < 2 {
			// A name with no separator has no half to take. Comparing it here
			// would only repeat what nearAnyRepo already decided.
			continue
		}
		for _, seg := range segs {
			// Folded on both sides. foldRepo drops a trailing "s" from the
			// whole name, which is not the same as dropping it from a segment:
			// "tools-media" keeps its s and "media-tools" loses it, and an
			// unfolded comparison would suppress the guess in one order and
			// report it in the other.
			if foldRepo(seg) == folded {
				return true
			}
		}
	}
	return false
}

// namesAsWholeWord reports whether the QUESTION carries the name on its own,
// rather than inside a longer one. It is the second half of the segment rule,
// and the half that keeps it honest: a reader who really did write
// "how does transmission-ui talk to transmission?" is asking about two
// systems, one of which is genuinely not indexed, and must still be told so.
//
// Its boundaries are not mentions': "-" and "_" count as word runes here, so
// "transmission-ui" does NOT carry the word transmission. mentions needs the
// opposite - a question spelling a longer name still narrows to the indexed
// repository inside it - which is why this is a separate test rather than a
// second caller of that one.
//
// The name is tested BOTH as the guess spells it and folded, because folding
// takes a trailing "s" off: a reader asking "how does media-tools differ from
// tools?" wrote "tools", the folded guess is "tool", and searching for that
// alone finds an occurrence whose own s is a word rune - so the question that
// names the missing repository outright would read as never naming it.
func namesAsWholeWord(question, name string) bool {
	q := strings.ToLower(question)
	for _, form := range spellings(name) {
		if carriesWholeWord(q, form) {
			return true
		}
	}
	return false
}

// spellings is the guess as the reader may have typed it and as foldRepo
// compares it, without a duplicate when the two agree.
func spellings(name string) []string {
	var out []string
	raw := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	if raw != "" {
		out = append(out, raw)
	}
	if folded := foldRepo(name); folded != "" && folded != raw {
		out = append(out, folded)
	}
	return out
}

// carriesWholeWord reports whether the lower-cased question holds name with a
// nameRune on neither side.
func carriesWholeWord(q, name string) bool {
	for i := 0; ; {
		j := strings.Index(q[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		if !wordBefore(q, start, nameRune) && !wordAfter(q, end, nameRune) {
			return true
		}
		i = start + 1
	}
}

// withinOneEdit reports whether a and b differ by at most one insertion,
// deletion or substitution.
func withinOneEdit(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(ra)-len(rb) > 1 {
		return false
	}
	var i, j, edits int
	for i < len(ra) && j < len(rb) {
		if ra[i] == rb[j] {
			i, j = i+1, j+1
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(ra) == len(rb) {
			i, j = i+1, j+1
			continue
		}
		i++
	}
	return edits+(len(ra)-i) <= 1
}

// knownRepos drops names no repository in the index carries.
//
// The restriction is a guess: the understanding step reads it off the wording,
// and measured over the real question catalogue three of nine guesses named
// something that does not exist — "peeqs", the possessive form of peeq;
// "Peek", a plain mishearing; and "asg017/sqlite-vec", a module nobody
// indexed. A name like that is not a narrowing, it is a wipe. It goes into
// `WHERE f.repo IN (…)`, nothing can match, and the turn reports "nothing
// found" about code that is sitting in the index.
//
// So an unknown name is dropped, and a restriction left with nothing at all
// becomes no restriction — the whole corpus, exactly as before this field
// existed. A name the index DOES know still restricts, empty result and all:
// "no hit means no hit" stays true for a repository that really is empty.
//
// The guess is not the only source. Every repository the QUESTION names as a
// whole word joins the restriction, because the guess is allowed to miss: a
// question reading "was schickt loom im header an das llm?" was answered from
// the whole corpus, and the reader was then asked to choose between modules of
// a repository they had not mentioned. The two are UNIONED — a guess that
// misses what the reader typed must not be able to exclude it.
func (r *Retriever) knownRepos(ctx context.Context, want []string, question string) ([]string, error) {
	if len(want) == 0 && strings.TrimSpace(question) == "" {
		return nil, nil
	}
	known, project, _, err := r.reposAndProjects(ctx)
	if err != nil {
		return nil, err
	}
	return knownReposIn(want, question, known, project), nil
}

// knownReposIn is knownRepos over an already-read namespace, so a caller
// that needs the namespace for a second answer reads it once.
func knownReposIn(want []string, question string, known []string, project map[string][]string) []string {
	if len(want) == 0 && strings.TrimSpace(question) == "" {
		return nil
	}

	guessed := map[string]bool{}
	for _, w := range want {
		guessed[w] = true
	}

	// A PROJECT name matched by the same two tests expands to its members.
	// This is what lets an Analyst narrow at all: they know the product, not
	// the repositories it is built from. The guards are unchanged — a project
	// called "backend" or "core" is guess-only, exactly as a repository of that
	// name already is, because commonWords does not care which of the two a
	// word happens to be.
	//
	// Naming a MEMBER still narrows to that member, which is why a project is
	// read out of the question with the hyphen as part of the name: "wie
	// funktioniert shop-ui" names shop-ui, not the product shop. Read the
	// repository way, "Schadenmeldung-service backend" searched all seven
	// members of the product for a question about one of them.
	wanted := map[string]bool{}
	for _, name := range known {
		if guessed[name] || mentions(question, name) {
			wanted[name] = true
		}
	}
	for name, members := range project {
		if guessed[name] || mentionsWhole(question, name) {
			for _, m := range members {
				wanted[m] = true
			}
		}
	}

	var out []string
	for _, name := range known {
		if wanted[name] {
			out = append(out, name)
		}
	}
	return out
}

// reposAndProjects reads the repository names and the projects they group into.
// A project of one is still a project: it is listed, and its own name resolves
// to its single member, which is the same answer the repository name gives.
//
// Read through projects.Load rather than a query of its own, so a project's
// name resolves to the same members the card and the prompt block use — the
// library a product is built on among them. projects.Load reads enabled = 1
// only: a parked repository is not a name the index knows. That is deliberate
// rather than incidental — it puts naming one on exactly the path a
// repository that was never indexed takes, where the name is dropped from the
// restriction and the turn says so out loud. Keeping the name would send it
// into `WHERE f.repo IN (…)`, match nothing, and report "nothing found" about
// the whole corpus.
//
// part is each repository's declared part, read for repoWithPart.
func (r *Retriever) reposAndProjects(ctx context.Context) (known []string, project map[string][]string, part map[string]string, err error) {
	pm, err := projects.Load(ctx, r.store.db)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	seen := map[string]bool{}
	project = map[string][]string{}
	part = map[string]string{}
	for _, p := range pm.All() {
		project[p.Name] = pm.Members(p.Name)
		for _, m := range p.Members {
			if !seen[m.Name] {
				seen[m.Name] = true
				known = append(known, m.Name)
				part[m.Name] = m.Part
			}
		}
	}
	sort.Strings(known)
	return known, project, part, nil
}

// minMentionLen is how short a repository name may be and still be read out of
// a question. Two letters are a word in every language the corpus is commented
// in, and a repository called "go" or "ui" would otherwise narrow every
// question that happens to contain it.
const minMentionLen = 3

// commonWords are never read as a repository mention, however a repository is
// named. A restriction is invisible from the outside — the turn reports
// "nothing found" plus the terms it tried, which reads as a vocabulary miss
// rather than as a corpus the reader never asked to narrow — so a repository
// called "search" must not swallow every question containing the word.
// The understanding step's guess still reaches such a repository; only the
// reader's own wording is refused as evidence for it.
var commonWords = map[string]bool{
	"api": true, "app": true, "apps": true, "backend": true, "code": true,
	"config": true, "core": true, "data": true, "docs": true, "frontend": true,
	"index": true, "lib": true, "main": true, "search": true, "server": true,
	"service": true, "shared": true, "test": true, "tests": true, "tools": true,
	"web": true,
}

// mentions reports whether question names repo as a whole word,
// case-insensitively. A substring is not a mention: "heirlooms" does not name
// loom, and reading it as one silences the rest of the corpus.
func mentions(question, repo string) bool {
	return mentionsBounded(question, repo, wordRune)
}

// mentionsWhole is mentions with the hyphen read as part of the name, so a
// project is not mentioned by the member whose name starts with it.
func mentionsWhole(question, project string) bool {
	return mentionsBounded(question, project, nameRune)
}

func mentionsBounded(question, repo string, is func(rune) bool) bool {
	if len(repo) < minMentionLen {
		return false
	}
	q, name := strings.ToLower(question), strings.ToLower(repo)
	if commonWords[name] {
		return false
	}
	for i := 0; ; {
		j := strings.Index(q[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		if !wordBefore(q, start, is) && !wordAfter(q, end, is) {
			return true
		}
		i = start + 1
	}
}

// wordBefore and wordAfter decide the boundaries, on RUNES rather than bytes.
// The questions are German: "loomähnlich" must not name loom, and an ASCII
// test would read the leading byte of "ä" as a boundary and narrow the whole
// search to loom.
// The predicate is a parameter because the two callers disagree about the
// hyphen: mentions reads it as a boundary, namesAsWholeWord does not.
func wordBefore(s string, i int, is func(rune) bool) bool {
	r, n := utf8.DecodeLastRuneInString(s[:i])
	return n > 0 && is(r)
}

func wordAfter(s string, i int, is func(rune) bool) bool {
	r, n := utf8.DecodeRuneInString(s[i:])
	return n > 0 && is(r)
}

func wordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// nameRune is wordRune plus the hyphen: the runes a repository name is spelled
// from, so a name embedded in a longer one is not read as a mention of it.
func nameRune(r rune) bool {
	return r == '-' || wordRune(r)
}

// searchTexts is Search over SEVERAL phrasings of one question.
//
// Phase 2 shaped it this way in advance, and phase 4 fills it: the understanding
// step hands in the raw question, the business-language restatement and the
// guessed code vocabulary, and each becomes its own semantic lane before fusion.
// That arrived as another lane rather than as a reshaping of this function,
// which is what the slice was for.
//
// code is the one of those phrasings that is guessed IDENTIFIERS rather than
// prose, passed by name so the keyword rungs can tell it apart; empty means
// there is none.
func (r *Retriever) searchTexts(ctx context.Context, texts []string, code, prior string, repos []string, k int, stage StagePrefixes) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	candidates := r.Candidates
	if candidates <= 0 {
		candidates = defaultCandidates
	}
	// With a reranker the fused list is cut to its pool rather than to k, and
	// the lanes must reach as deep as that pool, or the pool is the lanes:
	// forty rows per lane cannot fill a list of sixty.
	cutTo := k
	if r.Reranker != nil && r.Reranker.Pool > k {
		cutTo = r.Reranker.Pool
		if candidates < cutTo {
			candidates = cutTo
		}
	}

	var usable []string
	for _, t := range texts {
		if t != "" {
			usable = append(usable, t)
		}
	}
	if len(usable) == 0 {
		return []Hit{}, nil
	}

	var lanes []Lane

	vecs, err := r.embedder.Embed(ctx, usable)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != len(usable) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d query texts", len(vecs), len(usable))
	}
	for i, v := range vecs {
		hits, err := r.store.SearchVectorIn(ctx, v, candidates, r.MaxDistance, repos, stage)
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, Lane{
			Name:   fmt.Sprintf("semantic:%d", i),
			Hits:   hits,
			Weight: WeightSemantic,
		})
	}

	// Every rung that returns rows becomes its own lane, carrying its own
	// weight. Unlike peeq's version this does not stop descending early: there
	// the rungs are round-trips worth avoiding, here they are local SQLite
	// queries against a single file.
	for _, text := range usable {
		for _, tier := range BuildFTSQueries(text) {
			weight, name := tier.Weight, laneName(tier.Weight)
			// The OR floor of the code-terms text is not the same claim as the
			// OR floor of the question's prose: "one of these words appears
			// here" says more when the words are guessed identifiers. Only the
			// floor — the rungs above it already require every term, and there
			// the source of the words changes nothing.
			//
			// The label is set HERE, not derived from the weight afterwards: a
			// swept CodeWeight of 0.7 would otherwise report itself as the
			// prefix rung and a sweep would read as four rungs moving.
			if text == code && weight == WeightKeywordAny && r.CodeWeight > 0 {
				weight, name = r.CodeWeight, "keyword:code"
			}
			hits, err := r.store.SearchKeywordIn(ctx, tier.Match, candidates, repos, stage)
			if err != nil {
				return nil, err
			}
			if len(hits) == 0 {
				continue
			}
			lanes = append(lanes, Lane{
				Name:   name,
				Hits:   hits,
				Weight: weight,
			})
		}
	}

	// The substring rung, last because it is the only lane that does not go
	// through an index: a term that occurs solely INSIDE a larger token
	// (getAnzahlGeraete, setAnzahlgeraete) is invisible to every rung above,
	// whatever weight they carry, because unicode61 tokenizes those whole.
	//
	// Terms are derived in code from the question and the guessed identifiers,
	// never asked of the model: one more model call to recover from a model
	// guess that missed is a second chance at the same mistake.
	// ONE lane for every term, not one per term. The terms are several guesses
	// at a single claim — "this chunk contains the identifier" — and
	// get<X>, set<X> and <X> routinely all match the same chunk. As separate
	// lanes that chunk would be scored three times at SubstringWeight each,
	// an effective 2.55: above WeightKeywordStrict, from a rung that is
	// deliberately below it. Hits are deduped by ChunkID and keep the order
	// the first term found them in, which is the kind-then-address order the
	// store already applied.
	if r.SubstringWeight > 0 {
		// texts[0] and not q.Question: Query.Texts documents the raw question
		// as its FIRST entry, while Question is deliberately empty on the
		// multi-repo and chosen-repo paths (searchScoped leaves it out because
		// it names every repository being searched). Reading Question here
		// would switch the prose half of the rung off on exactly those paths
		// while the lane still reported itself as running.
		//
		// Guarded rather than assumed: an empty first text means no prose
		// candidates, and the code terms carry the rung alone.
		prose := ""
		if len(texts) > 0 {
			prose = texts[0]
		}
		terms := BuildSubstringTerms(prose, strings.Fields(code))
		// ROUND-ROBIN across the terms, never term after term. Lane rank is
		// what fusion weighs, so concatenating gives the FIRST term's hits
		// every slot near the top — and the first terms are the guessed code
		// terms, which are by construction the guesses that MISSED when this
		// rung is needed at all.
		//
		// Measured on the motivating question: the guessed term
		// "antrag" alone returned 40 hits — the whole lane — while
		// staying under the hub share, so "anzahlgeraete" and its single
		// chunk, the mapping the rung exists to recover, were cut before
		// fusion ever saw them. A wide term must cost itself, not the lane.
		perTerm := make([][]Hit, len(terms))
		for i, term := range terms {
			found, err := r.store.SearchSubstringIn(ctx, term, candidates, repos, stage)
			if err != nil {
				return nil, err
			}
			perTerm[i] = found
		}
		var hits []Hit
		seen := map[int64]bool{}
		for depth := 0; len(hits) < candidates; depth++ {
			progressed := false
			for _, found := range perTerm {
				if depth >= len(found) {
					continue
				}
				progressed = true
				h := found[depth]
				if seen[h.ChunkID] {
					continue
				}
				seen[h.ChunkID] = true
				hits = append(hits, h)
				if len(hits) >= candidates {
					break
				}
			}
			if !progressed {
				break
			}
		}
		if len(hits) > 0 {
			lanes = append(lanes, Lane{
				Name:   "keyword:substring",
				Hits:   hits,
				Weight: r.SubstringWeight,
			})
		}
	}

	fused := FuseWeightedDecayed(lanes, cutTo, Decays{Repo: r.RepoDecay, Test: r.TestDecay, Doc: r.DocDecay})
	if fused == nil {
		// An empty slice, never nil: the caller distinguishes "nothing found"
		// from an error, not from a nil check.
		return []Hit{}, nil
	}
	if r.Reranker != nil {
		// The raw question, never an expansion: the model reads what the
		// reader wrote against what the lanes found for all three phrasings.
		//
		// On a follow-up the thread's previous question goes with it, for the
		// reason it is a search lane at all: "in welchem Formularschritt
		// passiert das?" cannot be judged against anything, and a reranker
		// reading it alone scores the chunks the thread is about as
		// irrelevant and cuts them. The record, not an expansion - the same
		// distinction the rule above draws.
		return r.Reranker.Rerank(ctx, rerankQuestion(texts[0], prior), fused, k)
	}
	return fused, nil
}

// rerankQuestion is what the reranker is asked to judge against: the reader's
// question, and on a follow-up the turn it continues above it.
//
// Labelled rather than concatenated, so the model reads the current question
// as the ask and the previous one as context. Empty prior, or a prior equal
// to the question, leaves the string exactly as it was - a first turn's
// rerank prompt is unchanged, which is what the measured 27/30 to 28/30 was
// taken on.
func rerankQuestion(question, prior string) string {
	prior = strings.TrimSpace(prior)
	if prior == "" || prior == strings.TrimSpace(question) {
		return question
	}
	return "Earlier in this conversation: " + prior + "\n" + question
}

// laneName labels a keyword rung by what it means, so a result can explain
// itself: "this chunk contains every word you typed" is a different claim from
// "this chunk contains one of them".
//
// Only the four fixed rungs. The code rung is named where it is built, because
// its weight is a configured value under a sweep and a switch on the number
// would relabel the rung whenever the sweep passed another rung's constant.
func laneName(weight float64) string {
	switch weight {
	case WeightKeywordStrict:
		return "keyword:strict"
	case WeightKeywordContent:
		return "keyword:content"
	case WeightKeywordPrefix:
		return "keyword:prefix"
	default:
		return "keyword:any"
	}
}
