package switchboard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderInbox renders a batch into the agent's turn input, each message tagged
// with its channel and author so the agent can disambiguate speakers. An empty
// batch renders the sentinel the agent sees after a `get` with nothing waiting.
func RenderInbox(batch []Message) string {
	if len(batch) == 0 {
		return "There are no messages."
	}
	var b strings.Builder
	b.WriteString("New Discord messages:\n")
	for _, m := range batch {
		fmt.Fprintf(&b, "[#%s] %s: %s\n", m.Channel, m.Author, m.Content)
	}
	return b.String()
}

// ParseTurnResult parses the agent's JSON result-file contents and validates the
// action. Unknown/missing actions are an error (the loop must never guess).
func ParseTurnResult(data []byte) (TurnResult, error) {
	var r TurnResult
	if err := json.Unmarshal(data, &r); err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: bad turn result: %w", err)
	}
	switch r.Action {
	case ActionExit, ActionWait, ActionGet:
		return r, nil
	default:
		return TurnResult{}, fmt.Errorf("switchboard: invalid action %q (want exit|wait|get)", r.Action)
	}
}
