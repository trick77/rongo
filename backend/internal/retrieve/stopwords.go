package retrieve

// stopwords are the function words a question is built from. They occur in
// essentially every chunk, so ANDing them into the keyword lane guarantees zero
// rows for any natural question.
//
// Dropping them is what keeps the content rung a rung of its own. A rung that
// still carries every function word is identical to the strict rung above it,
// is then dropped as redundant, and the ladder collapses to its OR floor at
// weight 0.4, BELOW the semantic lane. The keyword lane would still return
// rows, so nothing looks broken; it would simply have stopped being the strong
// evidence it exists to be.
//
// The list is deliberately conservative: only words carrying no topical signal
// at all. Anything a user might plausibly be searching FOR stays out — the
// strict rung runs first anyway, so this only affects a query that would
// otherwise have matched nothing.
var stopwords = map[string]struct{}{}

func init() {
	words := []string{
		// --- articles, pronouns, particles ---
		//
		// Identifier-shaped words stay out on purpose: "all", "any", "some",
		// "every" and "each" are real names in the corpus (Promise.all, a TS
		// any, Array.some). Dropping them would strip a question down to
		// nothing and answer "found nothing" about code that is indexed.
		"a", "an", "the", "and", "or", "but", "nor", "so", "yet", "if",
		"then", "than", "as", "that", "this", "these", "those", "there",
		"here", "it", "its", "we", "you", "they", "them", "their",
		// --- question words and auxiliaries ---
		"how", "what", "who", "where", "when", "why", "which",
		"is", "are", "was", "were", "be", "been", "being", "do", "does",
		"did", "has", "have", "had", "can", "could", "should", "would",
		"will", "shall", "may", "might", "must",
		// --- prepositions ---
		"about", "after", "against", "among", "around", "at", "before",
		"between", "by", "during", "for", "from", "in", "inside", "into",
		"of", "off", "on", "onto", "out", "over", "through", "to", "under",
		"until", "up", "upon", "with", "within", "without",
	}
	for _, w := range words {
		stopwords[w] = struct{}{}
	}
}
