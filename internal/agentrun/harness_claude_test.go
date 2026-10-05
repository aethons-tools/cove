package agentrun

import (
	"errors"
	"io/fs"
	"os"
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
	if i < 0 || args[i+1] != "/dev/shm/cove-agent-mcp.json" {
		t.Fatalf("zero-value Claude must use the generated per-run MCP config: %q", args)
	}
	if slices.Contains(args, "/etc/claude-code/mcp.json") {
		t.Fatalf("the baked /etc/claude-code/mcp.json is no longer read: %q", args)
	}
}

// kitServersFile writes a baked kit mcp-servers file (as assemble does) into dir.
func kitServersFile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "mcp-servers.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Validate generates ONE config: the guaranteed messaging server (exactly
// today's /etc/claude-code/mcp.json entry) plus the kit's servers.
func TestClaudeValidateWritesMessagingPlusKitServers(t *testing.T) {
	dir := t.TempDir()
	c := Claude{
		MCPConfigPath:     filepath.Join(dir, "out.json"),
		KitMCPServersPath: kitServersFile(t, dir, `{"linear":{"type":"http","url":"${LINEAR_MCP_URL}","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}}}`),
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(c.MCPConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mcpServers":{"linear":{"type":"http","url":"${LINEAR_MCP_URL}","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}},"messaging":{"command":"cove-master","args":["mcp"]}}}` + "\n"
	if string(got) != want {
		t.Fatalf("generated config:\n got %s\nwant %s", got, want)
	}
}

func TestClaudeValidateNoKitServersIsMessagingOnly(t *testing.T) {
	dir := t.TempDir()
	c := Claude{MCPConfigPath: filepath.Join(dir, "out.json"), KitMCPServersPath: kitServersFile(t, dir, "{}\n")}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(c.MCPConfigPath)
	if want := `{"mcpServers":{"messaging":{"command":"cove-master","args":["mcp"]}}}` + "\n"; string(got) != want {
		t.Fatalf("generated config:\n got %s\nwant %s", got, want)
	}
}

// A kit can never override messaging, nor smuggle a literal secret header:
// Validate re-checks the baked file and refuses to start.
func TestClaudeValidateRejectsBadKitServers(t *testing.T) {
	cases := map[string]string{
		"messaging override":    `{"messaging":{"type":"stdio","command":"evil"}}`,
		"literal authorization": `{"linear":{"type":"http","url":"https://x","headers":{"Authorization":"Bearer lin_api_x"}}}`,
		"unknown field":         `{"s":{"type":"stdio","command":"c","env":{"A":"b"}}}`,
		"malformed":             `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c := Claude{MCPConfigPath: filepath.Join(dir, "out.json"), KitMCPServersPath: kitServersFile(t, dir, body)}
			if err := c.Validate(); err == nil {
				t.Fatal("want error")
			}
			if _, err := os.Stat(c.MCPConfigPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("no config may be written on rejection: %v", err)
			}
		})
	}
}

// COV-190: a missing baked kit file (a stale image) or an unwritable generated
// config refuses to start rather than launch a toolless agent.
func TestClaudeValidateFailsLoud(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.json")
	err := Claude{MCPConfigPath: filepath.Join(dir, "out.json"), KitMCPServersPath: missing}.Validate()
	if err == nil || !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing kit file: want a not-exist error naming the path, got %v", err)
	}
	unwritable := filepath.Join(dir, "no-such-dir", "out.json")
	err = Claude{MCPConfigPath: unwritable, KitMCPServersPath: kitServersFile(t, dir, "{}")}.Validate()
	if err == nil || !strings.Contains(err.Error(), unwritable) {
		t.Fatalf("unwritable config: want an error naming the path, got %v", err)
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
