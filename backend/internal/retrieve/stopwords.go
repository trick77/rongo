package retrieve

// stopwords are the function words a question is built from. They occur in
// essentially every chunk, so ANDing them into the keyword lane guarantees zero
// rows for any natural question.
//
// GERMAN AND ENGLISH, both, and the pairing is the point. rongo's users ask in
// German while the code, the identifiers and most comments are English, so a
// question mixes the two in one sentence ("Wie wird der AbandonedCartJob
// getriggert?"). peeq's list is English-only, which for a German question
// leaves the content rung identical to the strict rung — it is then dropped as
// redundant, and the ladder collapses to its OR floor at weight 0.4, BELOW the
// semantic lane. The keyword lane would still return rows, so nothing looks
// broken; it would simply have stopped being the strong evidence it exists to
// be.
//
// The list is deliberately conservative: only words carrying no topical signal
// at all. Anything a user might plausibly be searching FOR stays out — the
// strict rung runs first anyway, so this only affects a query that would
// otherwise have matched nothing.
//
// Both spellings of every umlaut word are listed (fuer and für): FTS5's
// unicode61 folds diacritics, but this map is consulted before the tokenizer
// ever sees the term.
var stopwords = map[string]struct{}{}

func init() {
	words := []string{
		// --- German: articles, pronouns, particles ---
		"der", "die", "das", "den", "dem", "des", "ein", "eine", "einen",
		"einem", "einer", "eines", "kein", "keine", "keinen",
		"ich", "du", "er", "sie", "es", "wir", "ihr", "man", "sich", "mir",
		"mich", "uns", "euch", "ihnen", "sein", "seine", "ihre", "unser",
		"dieser", "diese", "dieses", "jener", "jene", "alle", "allen",
		// --- German: question words and auxiliaries ---
		"wie", "was", "wer", "wo", "wann", "warum", "wieso", "weshalb",
		"welche", "welcher", "welches", "welchem", "welchen",
		"ist", "sind", "war", "waren", "wird", "werden", "wurde", "wurden",
		"hat", "haben", "hatte", "hatten", "kann", "koennen", "können",
		"muss", "muessen", "müssen", "soll", "sollen", "darf", "duerfen",
		"dürfen", "gibt", "geben", "macht", "machen", "tut", "tun",
		// --- German: conjunctions and prepositions ---
		"und", "oder", "aber", "denn", "sondern", "dass", "ob", "wenn",
		"weil", "damit", "also", "dann", "noch", "schon", "nur", "auch",
		"nicht", "so", "als", "wie", "im", "in", "an", "am", "auf", "aus",
		"bei", "beim", "mit", "nach", "von", "vom", "vor", "zu", "zum",
		"zur", "ueber", "über", "unter", "durch", "fuer", "für", "um",
		"ohne", "gegen", "bis", "seit", "waehrend", "während", "einem",
		// --- English: articles, conjunctions, particles ---
		"a", "an", "the", "and", "or", "but", "nor", "so", "yet", "if",
		"then", "than", "as", "that", "this", "these", "those", "there",
		"here", "it", "its", "we", "you", "they", "them", "their",
		// --- English: question words and auxiliaries ---
		"how", "what", "who", "where", "when", "why", "which",
		"is", "are", "was", "were", "be", "been", "being", "do", "does",
		"did", "has", "have", "had", "can", "could", "should", "would",
		"will", "shall", "may", "might", "must",
		// --- English: prepositions ---
		"about", "after", "against", "among", "around", "at", "before",
		"between", "by", "during", "for", "from", "in", "inside", "into",
		"of", "off", "on", "onto", "out", "over", "through", "to", "under",
		"until", "up", "upon", "with", "within", "without",
	}
	for _, w := range words {
		stopwords[w] = struct{}{}
	}
}
