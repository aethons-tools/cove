package sessionevents

import "encoding/json"

type rawEnvelope struct {
	Type         string      `json:"type"`
	Subtype      string      `json:"subtype"`
	SessionID    string      `json:"session_id"`
	Message      *rawMessage `json:"message"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	DurationMS   int64       `json:"duration_ms"`
	IsError      bool        `json:"is_error"`
	Usage        *rawUsage   `json:"usage"`
}

type rawMessage struct {
	// Content is a block array for assistant/tool messages but a plain string
	// for a user prompt — decode lazily so neither shape fails the envelope.
	Content json.RawMessage `json:"content"`
}

type rawBlock struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error"`
}

type rawUsage struct {
	InputTokens   int64 `json:"input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}

// DeriveIndex extracts the thin, queryable index from one stream-json line.
// It is best-effort: unknown or malformed input yields a zero/partial Index,
// never an error — the raw line is what is stored.
func DeriveIndex(raw []byte) Index {
	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Index{}
	}
	ix := Index{Type: env.Type, Subtype: env.Subtype, ClaudeSessionID: env.SessionID}
	if env.Message != nil {
		var blocks []rawBlock
		if json.Unmarshal(env.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_use" && ix.ToolName == "" {
					ix.ToolName = b.Name
				}
				if b.Type == "tool_result" && b.IsError {
					ix.IsError = true
				}
			}
		}
	}
	if env.Type == "result" {
		ix.CostUSD, ix.DurationMS, ix.IsError = env.TotalCostUSD, env.DurationMS, env.IsError
		if u := env.Usage; u != nil {
			ix.InputTokens = u.InputTokens + u.CacheCreation + u.CacheRead
			ix.OutputTokens = u.OutputTokens
		}
	}
	return ix
}
