package usage

// Call is one paid request, as the upstream reported it.
//
// Table: message_usage
type Call struct {
	// Step names what the call was for: understand, route, name, embed,
	// answer, title. It is the label a reader sees in the breakdown.
	Step string `json:"step"`
	// Model is the deployment or embedding model the call went to, and the
	// id its context window is looked up by.
	Model      string `json:"model"`
	Prompt     int    `json:"prompt_tokens"`
	Completion int    `json:"completion_tokens"`
	// Cached is the part of Prompt the upstream served from its prompt cache,
	// as prompt_tokens_details.cached_tokens. A SUBSET of Prompt, never an
	// addition: total_tokens equals prompt plus completion whether anything
	// was cached or not. It matters because a cached token is priced at
	// cache_read, which providers list far below the input price — a thread's second turn repeats the prefix of its
	// first, and charging that at full price overstates what it cost.
	//
	// Reasoning is completion_tokens_details.reasoning_tokens: the part of
	// Completion the model spent thinking rather than writing. Also a subset.
	//
	// Ms is how long the call took, wall clock, request to last byte.
	//
	// All three are pointers for one reason: a turn answered before these
	// were recorded must not read as a call that cached nothing, reasoned
	// about nothing and took no time. Absent is absent — the same rule
	// Report.CostUSD keeps for a turn nothing priced.
	Cached    *int `json:"cached_tokens,omitempty"`
	Reasoning *int `json:"reasoning_tokens,omitempty"`
	Ms        *int `json:"ms,omitempty"`
	// CostNanoUSD is what the call cost at the vendor's list price, in
	// billionths of a dollar, priced by llmwire from its own table as the
	// reply came in. Absent when llmwire had no rate for the model, and on
	// every row written before the column existed: a call nobody priced must
	// not read as a call that cost nothing. Integer, because a sum of floats
	// drifts and a bill does not.
	CostNanoUSD *int64 `json:"cost_nano_usd,omitempty"`
}
