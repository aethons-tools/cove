// Package sessionview turns stored session events into the display model
// shared by the admin timeline and the member "Thinking" view, and serves
// them over SSE (Serve). The SSE event names:
//
//   - stream: data = the stream id being followed.
//   - ev: one event's fragment; id "<stream>:<seq>" (the Last-Event-ID to resume).
//   - totals: the totals fragment, whole-stream (only when Source.Totals is set).
//   - more: tail mode, fresh connect; data = the lowest seq sent, when older events exist.
//   - skipped: tail-mode resume; data = "<from>-<to>", the seqs not sent.
//   - newstream: FollowNewest, unpinned; data = the newest stream's id; the response ends.
package sessionview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Kind classifies an event for the member view.
type Kind string

const (
	KindThinking   Kind = "thinking"
	KindText       Kind = "text"
	KindPrompt     Kind = "prompt" // a user message that isn't a tool result: what started the turn
	KindToolCall   Kind = "tool_call"
	KindToolResult Kind = "tool_result"
	KindResult     Kind = "result"
	KindProgress   Kind = "progress"
	KindGap        Kind = "gap"
	KindOther      Kind = "other"
)

// DetailMax caps EventView.Detail (bytes); DetailTruncated counts the rest.
const DetailMax = 64 << 10

// EventView is one event as displayed.
type EventView struct {
	StreamID        string
	Seq             uint64
	Turn            uint32
	At              time.Time
	Kind            Kind
	Label           string // admin label: "thinking", "tool_use Bash", "system/init", …
	Tool            string // tool name (tool_call; tool_result when known from Index.ToolName)
	ToolUseID       string // tool_use id / tool_result tool_use_id
	Narrative       string // one line for the member view
	Summary         string // admin summary
	Detail          string // ≤ DetailMax bytes
	DetailTruncated int    // bytes cut from Detail
	Raw             string // pretty JSON (admin renders it; members fetch raw separately)
	IsError         bool
	Progress        bool // system/thinking_tokens — hidden unless toggled
	Gap             bool
	TruncatedBytes  uint64
	CostUSD         float64 // result: the claude session's cumulative cost (Index.CostUSD)
	DurationMS      int64   // result
	ClaudeSession   string  // result: the claude session CostUSD accumulates over
}

// Clip collapses whitespace and cuts s to n runes, appending "…" when cut.
func Clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func pretty(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") == nil {
		return b.String()
	}
	return string(raw)
}

type viewBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// blockText renders a tool_result content field: a string, or an array of
// {type:text,text} blocks.
func blockText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []viewBlock
	if json.Unmarshal(c, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return string(c)
}

var keyFields = map[string][]string{
	"Bash": {"command"}, "Read": {"file_path"}, "Write": {"file_path"}, "Edit": {"file_path"}, "MultiEdit": {"file_path"},
	"NotebookEdit": {"notebook_path"}, "Grep": {"pattern"}, "Glob": {"pattern"}, "WebFetch": {"url"}, "WebSearch": {"query"},
	"Task": {"description"}, "Agent": {"description"},
}

// keyInput is the one-line gist of a tool call's input.
func keyInput(tool string, input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, f := range keyFields[tool] {
			if s, ok := m[f].(string); ok && s != "" {
				return Clip(s, 160)
			}
		}
	}
	return Clip(string(input), 120)
}

func firstLine(s string) string {
	s = strings.TrimLeft(s, "\r\n")
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// View builds the display model for one event.
func View(ev sessionevents.Event) EventView {
	v := build(ev)
	if len(v.Detail) > DetailMax {
		n := len(v.Detail)
		cut := DetailMax
		for cut > 0 && !utf8.RuneStart(v.Detail[cut]) {
			cut--
		}
		v.Detail = v.Detail[:cut]
		v.DetailTruncated = n - cut
	}
	return v
}

func build(ev sessionevents.Event) EventView {
	v := EventView{StreamID: ev.StreamID, Seq: ev.Seq, Turn: ev.Turn, At: ev.ObservedAt, Kind: KindOther,
		Raw: pretty(ev.Raw), TruncatedBytes: ev.TruncatedBytes, IsError: ev.Index.IsError,
		CostUSD: ev.Index.CostUSD, DurationMS: ev.Index.DurationMS, ClaudeSession: ev.Index.ClaudeSessionID}
	if ev.Kind == sessionevents.KindGap {
		v.Kind, v.Gap, v.Label = KindGap, true, "gap"
		v.Summary = fmt.Sprintf("⚠ %d events lost (seq %d–%d)", ev.GapTo-ev.GapFrom+1, ev.GapFrom, ev.GapTo)
		v.Narrative = v.Summary
		return v
	}
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result  string `json:"result"`
		Command string `json:"command"`
		By      string `json:"by"`
		Ignored string `json:"ignored"`
	}
	if json.Unmarshal(ev.Raw, &env) != nil {
		v.Label, v.Detail = "unparsed", string(ev.Raw)
		return v
	}
	var blocks []viewBlock
	_ = json.Unmarshal(env.Message.Content, &blocks)
	switch env.Type {
	case "system":
		v.Label = "system/" + env.Subtype
		v.Progress = env.Subtype == "thinking_tokens"
		if v.Progress {
			v.Kind = KindProgress
		}
	case "assistant":
		v.Label = "assistant"
		for _, b := range blocks {
			switch b.Type {
			case "text":
				v.Kind = KindText
				v.Summary, v.Detail, v.Narrative = Clip(b.Text, 300), b.Text, Clip(b.Text, 160)
			case "thinking":
				v.Kind = KindThinking
				v.Label, v.Summary, v.Detail, v.Narrative = "thinking", Clip(b.Thinking, 160), b.Thinking, Clip(b.Thinking, 160)
			case "tool_use":
				v.Kind = KindToolCall
				v.Label, v.Summary, v.Detail = "tool_use "+b.Name, Clip(string(b.Input), 160), pretty(b.Input)
				v.Tool, v.ToolUseID, v.Narrative = b.Name, b.ID, keyInput(b.Name, b.Input)
			}
		}
	case "user":
		v.Label = "user"
		if text := promptText(env.Message.Content, blocks); strings.TrimSpace(text) != "" {
			v.Kind, v.Label = KindPrompt, "prompt"
			v.Summary, v.Detail, v.Narrative = Clip(text, 300), text, Clip(firstLine(text), 160)
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				text := blockText(b.Content)
				v.Kind = KindToolResult
				v.Label, v.Detail = "tool_result", text
				v.Summary = fmt.Sprintf("%d bytes", len(text))
				v.IsError = v.IsError || b.IsError
				v.ToolUseID, v.Tool = b.ToolUseID, ev.Index.ToolName
				v.Narrative = Clip(firstLine(text), 160)
			}
		}
	case "result":
		v.Kind = KindResult
		v.Label = "result"
		v.Summary = fmt.Sprintf("$%.4f session total · in %d / out %d tokens · %s", ev.Index.CostUSD, ev.Index.InputTokens,
			ev.Index.OutputTokens, time.Duration(ev.Index.DurationMS)*time.Millisecond)
		v.Narrative = fmt.Sprintf("$%.4f · %s", ev.Index.CostUSD, time.Duration(ev.Index.DurationMS)*time.Millisecond)
		v.Detail = env.Result
	case "cove":
		if env.Subtype == "command" && env.Command != "" {
			// A session-control press (Interrupt/Compact/Clear) by a member.
			text := strings.ToUpper(env.Command[:1]) + env.Command[1:] + " — pressed"
			if env.By != "" {
				text += " by " + env.By
			}
			if env.Ignored != "" {
				text += " (ignored: " + env.Ignored + ")"
			}
			v.Kind, v.Label = KindPrompt, "control"
			v.Summary, v.Narrative = text, text
		} else {
			v.Label = env.Type
		}
	default:
		v.Label = env.Type
	}
	if v.Kind == KindOther {
		v.Narrative = v.Label // the member view shows only the type of what it doesn't render
	}
	return v
}

// promptText is a user message's text — a string content, or its text
// blocks — when it carries no tool result; "" otherwise.
func promptText(content json.RawMessage, blocks []viewBlock) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			return ""
		case "text":
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
