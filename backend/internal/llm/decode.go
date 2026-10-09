package llm

import (
	"encoding/json"

	"github.com/trick77/llmwire"
)

// DecodeReply reads the JSON object a gate reply carries into v: the object
// as llmwire.JSONObject finds it (fenced, prefixed with prose, or bare), then
// json.Unmarshal. An error is the caller's "not JSON" case and nothing more;
// what to do about it (keep the fused order, keep the gathered sources) is
// each gate's own rule.
func DecodeReply(out string, v any) error {
	body, _ := llmwire.JSONObject(out)
	return json.Unmarshal([]byte(body), v)
}
