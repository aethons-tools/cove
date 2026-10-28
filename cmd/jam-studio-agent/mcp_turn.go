package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aethons-tools/cove/internal/agentrun"
)

// agentSocket is the agent socket's path: JAM_AGENT_SOCKET (tests), else
// agentrun.AgentSocketPath. The socket is served by this cove's long-lived
// jam-studio-agent run, not by Jam.
func agentSocket(getenv func(string) string) string {
	if p := getenv("JAM_AGENT_SOCKET"); p != "" {
		return p
	}
	return agentrun.AgentSocketPath
}

// turnEndIn is the "turn_end" tool's typed input.
type turnEndIn struct {
	To            string           `json:"to,omitempty" jsonschema:"post your final message here instead of your own channel (same targets as send)"`
	Post          *bool            `json:"post,omitempty" jsonschema:"false = post nothing this turn, even if you end with text"`
	Commit        *bool            `json:"commit,omitempty" jsonschema:"false = the messages you were shown are NOT marked handled; they come back with your next wake"`
	Next          string           `json:"next,omitempty" jsonschema:"what happens after this turn: wait (default), compact (shrink your conversation, then wait) or end (never wake again; needs reason)"`
	Reason        string           `json:"reason,omitempty" jsonschema:"with next end: why the session is ending"`
	WakeOnMessage *bool            `json:"wake_on_message,omitempty" jsonschema:"false = new messages don't wake you until you next run (alarms, idle timeout and kept processes still do)"`
	WakeAt        *agentrun.WakeAt `json:"wake_at,omitempty" jsonschema:"wake me at this RFC 3339 time: {at, note?, name?} (a one-shot alarm)"`
	IdleTimeout   string           `json:"idle_timeout,omitempty" jsonschema:"override the idle timeout for this turn end, e.g. 2h, or off"`
}

// addTurnTools registers turn_end, served by the agent socket.
func addTurnTools(s *mcp.Server, getenv func(string) string) {
	ac := agentrun.DialAgentSocket(agentSocket(getenv))
	mcp.AddTool(s, &mcp.Tool{
		Name: "turn_end",
		Description: "Change how this turn ends; call it before your final message, only when the defaults don't fit. " +
			"Defaults: your final message is posted to your own channel, the messages you were shown are marked handled, and you wait until a new message (or an alarm, the idle timeout, a kept process exiting) wakes you. " +
			"The last call in a turn wins; it applies to this turn only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in turnEndIn) (*mcp.CallToolResult, any, error) {
		return nil, nil, ac.SetTurnEnd(ctx, agentrun.Directives{To: in.To, Post: in.Post, Commit: in.Commit, Next: in.Next,
			Reason: in.Reason, WakeOnMessage: in.WakeOnMessage, WakeAt: in.WakeAt, IdleTimeout: in.IdleTimeout})
	})
}

// keepStartIn is the "keep_start" tool's typed input.
type keepStartIn struct {
	Name    string `json:"name" jsonschema:"a short name: lowercase letters, digits, - and _ (at most 32)"`
	Command string `json:"command" jsonschema:"the shell command to keep running (sh -c), e.g. npm run dev -- --port 5173"`
	Workdir string `json:"workdir,omitempty" jsonschema:"optional working directory; default your workspace"`
}

// keepStopIn is the "keep_stop" tool's typed input.
type keepStopIn struct {
	Name string `json:"name" jsonschema:"the kept process to stop"`
}

// keepListOut is the "keep_list" tool's typed output.
type keepListOut struct {
	Processes []agentrun.KeptProcess `json:"processes"`
}

// addKeepTools registers keep_start, keep_stop and keep_list, served by the
// agent socket.
func addKeepTools(s *mcp.Server, getenv func(string) string) {
	ac := agentrun.DialAgentSocket(agentSocket(getenv))
	mcp.AddTool(s, &mcp.Tool{
		Name: "keep_start",
		Description: "Start a process that keeps running after your turn ends (shell background jobs die with the turn), e.g. a preview server. " +
			"Its output goes to the log it returns; if it exits on its own, you are woken with its last lines. It stops with your session or with keep_stop. " +
			"Agree with the person how long it should run, and set a wake (turn_end wake_at) to ask them then.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in keepStartIn) (*mcp.CallToolResult, agentrun.KeptProcess, error) {
		p, err := ac.KeepStart(ctx, in.Name, in.Command, in.Workdir)
		return nil, p, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "keep_stop",
		Description: "Stop a process started with keep_start (SIGTERM to its process group, SIGKILL after 10s).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in keepStopIn) (*mcp.CallToolResult, any, error) {
		return nil, nil, ac.KeepStop(ctx, in.Name)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "keep_list",
		Description: "List the processes kept running with keep_start.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, keepListOut, error) {
		ps, err := ac.KeepList(ctx)
		return nil, keepListOut{Processes: ps}, err
	})
}
