package sessionevents

import (
	"encoding/json"
	"strings"
)

// subagents is how many subagents one actor is running now: foreground
// Agent/Task tool calls whose tool_result hasn't arrived, plus the agents in
// claude's latest background task list. A background Agent call
// (run_in_background) answers at once, so it is counted only through that
// list, never as a call.
type subagents struct {
	calls      map[string]struct{} // outstanding Agent/Task tool_use ids
	background int                 // agent tasks in the latest background_tasks_changed
}

func (s *subagents) count() int { return len(s.calls) + s.background }

// subagentTool reports whether a tool_use starts a subagent ("Task" is the
// Agent tool's former name).
func subagentTool(name string) bool { return name == "Agent" || name == "Task" }

// agentTask reports whether a background task is a subagent (local_agent,
// remote_agent) rather than, say, a shell (local_bash) or a workflow.
func agentTask(taskType string) bool { return strings.HasSuffix(taskType, "_agent") }

// rawSubagentLine is all of a line the count reads: tool ids and names, an
// Agent call's run_in_background flag, and the background task types — never
// any content.
type rawSubagentLine struct {
	Message *struct {
		Content json.RawMessage `json:"content"` // a plain string for a user prompt
	} `json:"message"`
	Tasks []struct {
		TaskType string `json:"task_type"`
	} `json:"tasks"`
}

type rawSubagentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"` // read only for an Agent/Task call
}

// background reports whether a subagent call's input asks to run it in the
// background; an unreadable input counts as foreground.
func (b rawSubagentBlock) background() bool {
	var in struct {
		RunInBackground bool `json:"run_in_background"`
	}
	_ = json.Unmarshal(b.Input, &in)
	return in.RunInBackground
}

// subagentLine is what one event says about its actor's subagents.
type subagentLine struct {
	turn       bool     // a turn boundary (system/init or result): no foreground call outlives it
	snapshot   bool     // a background task list: Background replaces the old count
	background int      // with snapshot
	started    []string // foreground Agent/Task tool_use ids
	finished   []string // tool_result tool_use ids (any tool's)
}

// deriveSubagents reads ev's subagent news, or ok=false when it has none.
func deriveSubagents(ev Event) (l subagentLine, ok bool) {
	if ev.Kind != KindEvent {
		return l, false
	}
	ix := ev.Index
	switch {
	case ix.Type == "result", ix.Type == "system" && ix.Subtype == "init":
		return subagentLine{turn: true}, true
	case ix.Type == "system" && ix.Subtype == "background_tasks_changed",
		ix.Type == "assistant", ix.Type == "user":
	default:
		return l, false
	}
	var raw rawSubagentLine
	if json.Unmarshal(ev.Raw, &raw) != nil {
		return l, false
	}
	if ix.Type == "system" {
		l.snapshot = true
		for _, t := range raw.Tasks {
			if agentTask(t.TaskType) {
				l.background++
			}
		}
		return l, true
	}
	if raw.Message == nil {
		return l, false
	}
	var blocks []rawSubagentBlock
	if json.Unmarshal(raw.Message.Content, &blocks) != nil {
		return l, false // a user prompt's plain string
	}
	for _, b := range blocks {
		switch {
		case b.Type == "tool_use" && subagentTool(b.Name) && b.ID != "" && !b.background():
			l.started = append(l.started, b.ID)
		case b.Type == "tool_result" && b.ToolUseID != "":
			l.finished = append(l.finished, b.ToolUseID)
		}
	}
	return l, len(l.started)+len(l.finished) > 0
}

// apply updates s from one event's news. claude starts every turn with a
// system/init and ends it with a result, so either clears the foreground
// calls; the background list is a full snapshot, replaced whenever claude
// sends one, and survives turn boundaries.
func (s *subagents) apply(l subagentLine) {
	if l.turn {
		s.calls = nil
	}
	if l.snapshot {
		s.background = l.background
	}
	for _, id := range l.started {
		if s.calls == nil {
			s.calls = map[string]struct{}{}
		}
		s.calls[id] = struct{}{}
	}
	for _, id := range l.finished {
		delete(s.calls, id)
	}
}
