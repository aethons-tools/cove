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

// contextLine is what a raw line says about the agent's context window: the
// size of the main thread's context after an assistant message, the window of
// each model in a result, or that the context was reset (a compaction, or a
// Clear/Compact press jam-studio-agent acted on).
type contextLine struct {
	Tokens  int64            // > 0: a main-thread assistant message's context size
	Model   string           // with Tokens: the model that answered
	Windows map[string]int64 // a result's context window per model
	Reset   bool
}

type rawContextEnvelope struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Command         string  `json:"command"`
	Ignored         string  `json:"ignored"`
	Message         *struct {
		Model string    `json:"model"`
		Usage *rawUsage `json:"usage"`
	} `json:"message"`
	ModelUsage map[string]struct {
		ContextWindow int64 `json:"contextWindow"`
	} `json:"modelUsage"`
}

// deriveContext reads a context update from an event, if it carries one.
func deriveContext(ev Event) (contextLine, bool) {
	switch ev.Index.Type {
	case "assistant", "result", "system", "cove":
	default:
		return contextLine{}, false
	}
	var env rawContextEnvelope
	if json.Unmarshal(ev.Raw, &env) != nil {
		return contextLine{}, false
	}
	switch {
	case env.Type == "assistant" && env.ParentToolUseID == nil && env.Message != nil && env.Message.Usage != nil:
		u := env.Message.Usage
		if n := u.InputTokens + u.CacheCreation + u.CacheRead + u.OutputTokens; n > 0 {
			return contextLine{Tokens: n, Model: env.Message.Model}, true
		}
	case env.Type == "result" && len(env.ModelUsage) > 0:
		w := map[string]int64{}
		for m, u := range env.ModelUsage {
			if u.ContextWindow > 0 {
				w[m] = u.ContextWindow
			}
		}
		return contextLine{Windows: w}, len(w) > 0
	case env.Type == "system" && env.Subtype == "compact_boundary",
		env.Type == "cove" && env.Subtype == "command" && env.Ignored == "" && (env.Command == "clear" || env.Command == "compact"):
		return contextLine{Reset: true}, true
	}
	return contextLine{}, false
}
