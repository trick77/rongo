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
	indexed, err := r.allRepos(ctx)
	if err != nil {
		return nil, nil, err
	}

	resolved := map[string]bool{}
	for _, n := range known {
		resolved[foldRepo(n)] = true
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
		unknown = append(unknown, n)
	}
	return known, unknown, nil
}

// allRepos is every repository the index carries. The table has one row per
// configured repository, so this is a handful of names.
func (r *Retriever) allRepos(ctx context.Context) ([]string, error) {
	rows, err := r.store.db.QueryContext(ctx, `SELECT name FROM repo_state`)
	if err != nil {
		return nil, fmt.Errorf("read the indexed repositories: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read the indexed repositories: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the indexed repositories: %w", err)
	}
	return out, nil
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
	rows, err := r.store.db.QueryContext(ctx, `SELECT name FROM repo_state`)
	if err != nil {
		return nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	defer rows.Close()
	var known []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("resolve repository restriction: %w", err)
		}
		known = append(known, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve repository restriction: %w", err)
	}

	guessed := map[string]bool{}
	for _, w := range want {
		guessed[w] = true
	}
	var out []string
	for _, name := range known {
		if guessed[name] || mentions(question, name) {
			out = append(out, name)
		}
	}
	return out, nil
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
		if !wordBefore(q, start) && !wordAfter(q, end) {
			return true
		}
		i = start + 1
	}
}

// wordBefore and wordAfter decide the boundaries, on RUNES rather than bytes.
// The questions are German: "loomähnlich" must not name loom, and an ASCII
// test would read the leading byte of "ä" as a boundary and narrow the whole
// search to loom.
func wordBefore(s string, i int) bool {
	r, n := utf8.DecodeLastRuneInString(s[:i])
	return n > 0 && wordRune(r)
}

func wordAfter(s string, i int) bool {
	r, n := utf8.DecodeRuneInString(s[i:])
	return n > 0 && wordRune(r)
}

func wordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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

	fused := FuseWeightedDiverseTests(lanes, k, r.RepoDecay, r.TestDecay)
	if fused == nil {
		// An empty slice, never nil: the caller distinguishes "nothing found"
		// from an error, not from a nil check.
		return []Hit{}, nil
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
