package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
)

// claudeMCPConfigPath is the baked-in MCP config (internal/assemble/hardening/
// image-files/etc/claude-code/mcp.json) that gives claude -p the Jam
// messaging tools. --strict-mcp-config keeps claude from also picking up any
// project/user-level MCP config.
const claudeMCPConfigPath = "/etc/claude-code/mcp.json"

// Claude is the Claude Code harness: claude -p with stream-json in and out
// (see docs/usage/jam/session-events.md). The zero value uses the baked MCP
// config.
type Claude struct {
	// MCPConfigPath is the --mcp-config file; empty = claudeMCPConfigPath.
	MCPConfigPath string
}

var _ Harness = Claude{}

func (c Claude) mcpConfig() string {
	if c.MCPConfigPath == "" {
		return claudeMCPConfigPath
	}
	return c.MCPConfigPath
}

// Validate fails loud if the MCP config is missing rather than launch a
// silently toolless agent (COV-190): claude with --mcp-config pointing at a
// nonexistent file registers no servers, so the agent would have no intercom
// read/send tools and flail. A stale image (built before mcp.json shipped in
// the hardening layer) is the typical cause.
func (c Claude) Validate() error {
	if _, err := os.Stat(c.mcpConfig()); err != nil {
		return fmt.Errorf("agentrun: MCP config %q missing or unreadable: %w", c.mcpConfig(), err)
	}
	return nil
}

// Command builds claude's argv for one episode. continued prepends
// --continue. The prompt is not in argv: it is the first stream-json message
// on stdin.
func (c Claude) Command(continued bool, contextCore string) (string, []string) {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	args = append(args, "--dangerously-skip-permissions", "--mcp-config", c.mcpConfig(), "--strict-mcp-config")
	if contextCore != "" {
		// snapshot off: the default replays the first turn's system prompt on
		// every --continue, which would hide context updates. (claude hard-fails
		// on a missing --append-system-prompt-file; Run passes "" if the context
		// could not be written.)
		args = append(args, "--append-system-prompt-file", contextCore, "--system-prompt-snapshot", "off")
	}
	return "claude", args
}

// EncodeInput encodes text as one stream-json stdin line.
func (Claude) EncodeInput(text string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message msg    `json:"message"`
	}{"user", msg{"user", text}})
	return append(b, '\n')
}

type claudeEvent struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	TaskID          string `json:"task_id"`
	QueuedTurnCount int    `json:"queued_turn_count"`
	Tasks           []struct {
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

// ParseEvent maps one claude stream-json stdout line:
//   - result → TurnEnd (QueuedEmpty when queued_turn_count is 0)
//   - system/background_tasks_changed → BackgroundTasks (the full list).
//     claude empties the list BEFORE the task's task_notification arrives.
//   - system/task_notification → BackgroundDone (the notification starts a turn)
//   - system/init, assistant, user → TurnStart
//   - anything else → Other
func (Claude) ParseEvent(line []byte) (Event, error) {
	var ev claudeEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, err
	}
	switch {
	case ev.Type == "result":
		return Event{Kind: EventTurnEnd, QueuedEmpty: ev.QueuedTurnCount == 0}, nil
	case ev.Type == "system" && ev.Subtype == "background_tasks_changed":
		tasks := make([]BackgroundTask, len(ev.Tasks))
		for i, k := range ev.Tasks {
			tasks[i] = BackgroundTask{ID: k.TaskID, Description: k.Description}
		}
		return Event{Kind: EventBackgroundTasks, Tasks: tasks}, nil
	case ev.Type == "system" && ev.Subtype == "task_notification":
		return Event{Kind: EventBackgroundDone, TaskID: ev.TaskID}, nil
	case ev.Type == "system" && ev.Subtype == "init",
		ev.Type == "assistant", ev.Type == "user":
		return Event{Kind: EventTurnStart}, nil
	}
	return Event{Kind: EventOther}, nil
}
