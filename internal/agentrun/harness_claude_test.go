package agentrun

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClaudeCommandUsesConfiguredMCPConfig(t *testing.T) {
	bin, args := Claude{MCPConfigPath: "/x/mcp.json"}.Command(false, "")
	if bin != "claude" {
		t.Fatalf("bin = %q", bin)
	}
	i := slices.Index(args, "--mcp-config")
	if i < 0 || args[i+1] != "/x/mcp.json" {
		t.Fatalf("--mcp-config not /x/mcp.json: %q", args)
	}
}

func TestClaudeZeroValueDefaultsMCPConfig(t *testing.T) {
	_, args := Claude{}.Command(false, "")
	i := slices.Index(args, "--mcp-config")
	if i < 0 || args[i+1] != "/etc/claude-code/mcp.json" {
		t.Fatalf("zero-value Claude must use the baked MCP config: %q", args)
	}
}

func TestClaudeValidate(t *testing.T) {
	dir := t.TempDir()
	if err := (Claude{MCPConfigPath: mcpConfigFile(t, dir)}).Validate(); err != nil {
		t.Fatalf("present config: %v", err)
	}
	missing := filepath.Join(dir, "nope.json")
	err := Claude{MCPConfigPath: missing}.Validate()
	if err == nil || !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing config: want a not-exist error naming the path, got %v", err)
	}
}

func TestClaudeEncodeInputIsOneStreamJSONLine(t *testing.T) {
	got := string(Claude{}.EncodeInput("hi"))
	if want := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestClaudeParseEvent(t *testing.T) {
	cases := []struct {
		name, line string
		want       Event
	}{
		{"init", `{"type":"system","subtype":"init","session_id":"s"}`, Event{Kind: EventTurnStart}},
		{"assistant", `{"type":"assistant","message":{}}`, Event{Kind: EventTurnStart}},
		{"user", `{"type":"user","message":{}}`, Event{Kind: EventTurnStart}},
		{"result idle", `{"type":"result","subtype":"success","queued_turn_count":0}`, Event{Kind: EventTurnEnd, QueuedEmpty: true}},
		{"result no count", `{"type":"result","subtype":"success"}`, Event{Kind: EventTurnEnd, QueuedEmpty: true}},
		{"result queued", `{"type":"result","queued_turn_count":2}`, Event{Kind: EventTurnEnd}},
		{"bg snapshot", `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a","description":"A"},{"task_id":"b","description":"B"}]}`,
			Event{Kind: EventBackgroundTasks, Tasks: []BackgroundTask{{ID: "a", Description: "A"}, {ID: "b", Description: "B"}}}},
		{"bg empty", `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`, Event{Kind: EventBackgroundTasks, Tasks: []BackgroundTask{}}},
		{"notification", `{"type":"system","subtype":"task_notification","task_id":"a"}`, Event{Kind: EventBackgroundDone, TaskID: "a"}},
		{"other system", `{"type":"system","subtype":"status"}`, Event{Kind: EventOther}},
		{"other type", `{"type":"stream_event"}`, Event{Kind: EventOther}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Claude{}.ParseEvent([]byte(c.line))
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != c.want.Kind || got.QueuedEmpty != c.want.QueuedEmpty || got.TaskID != c.want.TaskID || !slices.Equal(got.Tasks, c.want.Tasks) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
	if _, err := (Claude{}).ParseEvent([]byte(`not json`)); err == nil {
		t.Fatal("unparseable line: want error")
	}
}
