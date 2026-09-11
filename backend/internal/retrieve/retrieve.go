package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// K is how many hits to return. Zero means 10.
	K int
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
	}
}

// Search runs both lanes and fuses them.
//
// No match anywhere returns an EMPTY SLICE and NO ERROR. "No hit means no hit"
// is an answer the caller reports along with the terms it tried; an error here
// would be indistinguishable from a broken database, and the answer layer would
// have to guess which one it was looking at.
func (r *Retriever) Search(ctx context.Context, q Query) ([]Hit, error) {
	repos, err := r.knownRepos(ctx, q.Repos, q.Question)
	if err != nil {
		return nil, err
	}
	return r.searchTexts(ctx, q.texts(), repos, q.K)
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
	known, err = r.knownRepos(ctx, want, question)
	if err != nil {
		return nil, nil, err
	}
	if len(want) == 0 {
		return known, nil, nil
	}
	// Both halves of the addressable namespace: a reader may name a repository
	// or the project it belongs to, and neither is a name the index lacks.
	repos, project, err := r.reposAndProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
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
	known, project, err := r.reposAndProjects(ctx)
	if err != nil {
		return nil, err
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
	// Naming a MEMBER still narrows to that member: the expansion adds to the
	// restriction, and a question naming only shop-ui never mentions "shop" as
	// a whole word, so nothing widens it back out.
	wanted := map[string]bool{}
	for _, name := range known {
		if guessed[name] || mentions(question, name) {
			wanted[name] = true
		}
	}
	for name, members := range project {
		if guessed[name] || mentions(question, name) {
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
	return out, nil
}

// reposAndProjects reads the repository names and the projects they group into.
// A project of one is still a project: it is listed, and its own name resolves
// to its single member, which is the same answer the repository name gives.
func (r *Retriever) reposAndProjects(ctx context.Context) ([]string, map[string][]string, error) {
	// enabled = 1: a parked repository is not a name the index knows. That is
	// deliberate rather than incidental — it puts naming one on exactly the path
	// a repository that was never indexed takes, where the name is dropped from
	// the restriction and the turn says so out loud. Keeping the name would send
	// it into `WHERE f.repo IN (…)`, match nothing, and report "nothing found"
	// about the whole corpus.
	rows, err := r.store.db.QueryContext(ctx,
		`SELECT name, project FROM repo_state WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	defer rows.Close()
	var known []string
	project := map[string][]string{}
	for rows.Next() {
		var name, p string
		if err := rows.Scan(&name, &p); err != nil {
			return nil, nil, fmt.Errorf("resolve repository restriction: %w", err)
		}
		known = append(known, name)
		// An empty project can only come from a row written before projects
		// shipped; repos.Load refuses an entry without one. Grouping those
		// under "" would make one nameless product of every such repository.
		if p == "" {
			p = name
		}
		project[p] = append(project[p], name)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	return known, project, nil
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
		if !wordBefore(q, start, wordRune) && !wordAfter(q, end, wordRune) {
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
func (r *Retriever) searchTexts(ctx context.Context, texts []string, repos []string, k int) ([]Hit, error) {
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
		hits, err := r.store.SearchVector(ctx, v, candidates, r.MaxDistance, repos)
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
			hits, err := r.store.SearchKeyword(ctx, tier.Match, candidates, repos)
			if err != nil {
				return nil, err
			}
			if len(hits) == 0 {
				continue
			}
			lanes = append(lanes, Lane{
				Name:   laneName(tier.Weight),
				Hits:   hits,
				Weight: tier.Weight,
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
		return r.Reranker.Rerank(ctx, texts[0], fused, k)
	}
	return fused, nil
}

// laneName labels a keyword rung by what it means, so a result can explain
// itself: "this chunk contains every word you typed" is a different claim from
// "this chunk contains one of them".
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
