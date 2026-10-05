package agentrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/kit"
)

// claudeMCPConfigPath is the per-run MCP config the harness generates
// (messaging + the kit's servers) and passes as --mcp-config. tmpfs, like the
// prompt/context cove-master is launched with; it holds no secret values —
// kit header values are ${VAR} references Claude Code expands at start.
const claudeMCPConfigPath = "/dev/shm/cove-agent-mcp.json"

// claudeMessaging is the guaranteed Jam messaging server entry (exactly the
// entry the image's /etc/claude-code/mcp.json carried before COV-240).
var claudeMessaging = struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}{"cove-master", []string{"mcp"}}

// Claude is the Claude Code harness: claude -p with stream-json in and out
// (see docs/usage/jam/session-events.md). The zero value uses the production
// paths.
type Claude struct {
	// MCPConfigPath is the generated --mcp-config file; empty = claudeMCPConfigPath.
	MCPConfigPath string
	// KitMCPServersPath is the kit's baked mcp-servers JSON (name → server);
	// empty = kit.MCPServersImagePath.
	KitMCPServersPath string
}

var _ Harness = Claude{}

func (c Claude) mcpConfig() string {
	if c.MCPConfigPath == "" {
		return claudeMCPConfigPath
	}
	return c.MCPConfigPath
}

func (c Claude) kitMCPServers() string {
	if c.KitMCPServersPath == "" {
		return kit.MCPServersImagePath
	}
	return c.KitMCPServersPath
}

// Validate generates the run's one MCP config — the guaranteed messaging
// server plus the kit's servers — and fails loud if it can't (COV-190): claude
// with --mcp-config pointing at a missing file registers no servers, so the
// agent would have no intercom read/send tools and flail. --strict-mcp-config
// (see Command) then loads only these. A missing kit file (a stale image built
// before COV-240), a kit entry named messaging, or a literal header value is
// refused too — re-checked here as defense in depth over kit validation.
func (c Claude) Validate() error {
	raw, err := os.ReadFile(c.kitMCPServers())
	if err != nil {
		return fmt.Errorf("agentrun: kit MCP servers %q missing or unreadable: %w", c.kitMCPServers(), err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var servers map[string]kit.MCPServer
	if err := dec.Decode(&servers); err != nil {
		return fmt.Errorf("agentrun: kit MCP servers %q: %w", c.kitMCPServers(), err)
	}
	if err := kit.ValidateMCPServers("agentrun: kit MCP servers", servers); err != nil {
		return err
	}
	all := map[string]any{}
	for name, s := range servers {
		all[name] = s
	}
	all[kit.MCPReservedName] = claudeMessaging // last: never kit-overridable
	b, err := json.Marshal(map[string]any{"mcpServers": all})
	if err != nil {
		return fmt.Errorf("agentrun: MCP config: %w", err)
	}
	if err := writeFileAtomic(c.mcpConfig(), append(b, '\n')); err != nil {
		return fmt.Errorf("agentrun: MCP config %q could not be written: %w", c.mcpConfig(), err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file beside path and renames it over
// path, so claude never reads a partial config and a pre-planted symlink at
// path is replaced rather than followed.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
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
