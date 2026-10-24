package sessionview

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestView(t *testing.T) {
	at := time.Unix(1000, 0)
	mk := func(raw string) sessionevents.Event {
		return sessionevents.Event{StreamID: "s", Seq: 7, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: at, Raw: []byte(raw)}
	}
	cases := []struct {
		name, raw                         string
		kind                              Kind
		tool, tid, narrative, labelPrefix string
		isErr                             bool
	}{
		{"thinking", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Plan:\nfirst read the file"}]}}`, KindThinking, "", "", "Plan: first read the file", "thinking", false},
		{"text", `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`, KindText, "", "", "Done.", "assistant", false},
		{"bash", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./...","description":"run"}}]}}`, KindToolCall, "Bash", "tu1", "go test ./...", "tool_use Bash", false},
		{"edit", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Edit","input":{"file_path":"/w/a.go","old_string":"x","new_string":"y"}}]}}`, KindToolCall, "Edit", "tu2", "/w/a.go", "tool_use Edit", false},
		{"grep", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu3","name":"Grep","input":{"pattern":"func View"}}]}}`, KindToolCall, "Grep", "tu3", "func View", "tool_use Grep", false},
		{"unknown tool", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu4","name":"mcp__x__y","input":{"a":1}}]}}`, KindToolCall, "mcp__x__y", "tu4", `{"a":1}`, "tool_use mcp__x__y", false},
		{"result err", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"boom"}]}}`, KindToolResult, "", "tu1", "boom", "tool_result", true},
		{"result", `{"type":"result","subtype":"success","result":"ok"}`, KindResult, "", "", "", "result", false},
		{"progress", `{"type":"system","subtype":"thinking_tokens"}`, KindProgress, "", "", "", "system/thinking_tokens", false},
		{"system", `{"type":"system","subtype":"init"}`, KindOther, "", "", "", "system/init", false},
		{"unparsed", `not json`, KindOther, "", "", "", "unparsed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := View(mk(c.raw))
			if v.Kind != c.kind || v.Tool != c.tool || v.ToolUseID != c.tid || v.IsError != c.isErr || !strings.HasPrefix(v.Label, c.labelPrefix) {
				t.Fatalf("View = %+v", v)
			}
			if c.narrative != "" && v.Narrative != c.narrative {
				t.Fatalf("narrative = %q, want %q", v.Narrative, c.narrative)
			}
			if v.Seq != 7 || v.Turn != 2 || v.StreamID != "s" || !v.At.Equal(at) {
				t.Fatalf("identity = %+v", v)
			}
		})
	}
}

func TestViewGapAndClip(t *testing.T) {
	g := View(sessionevents.Event{Kind: sessionevents.KindGap, GapFrom: 3, GapTo: 5})
	if g.Kind != KindGap || !g.Gap || !strings.Contains(g.Summary, "3 events lost") {
		t.Fatalf("gap = %+v", g)
	}
	big := strings.Repeat("x", DetailMax+100)
	v := View(sessionevents.Event{Kind: sessionevents.KindEvent, Raw: []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"` + big + `"}]}}`)})
	if len(v.Detail) != DetailMax || v.DetailTruncated != 100 {
		t.Fatalf("clip: len %d, truncated %d", len(v.Detail), v.DetailTruncated)
	}
}

func TestViewToolResultNameAndResultFields(t *testing.T) {
	v := View(sessionevents.Event{Kind: sessionevents.KindEvent, Index: sessionevents.Index{ToolName: "Bash"},
		Raw: []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"line1\nline2"}]}}`)})
	if v.Tool != "Bash" || v.Narrative != "line1" {
		t.Fatalf("tool result = %+v", v)
	}
	r := View(sessionevents.Event{Kind: sessionevents.KindEvent, Index: sessionevents.Index{CostUSD: 1.5, DurationMS: 20},
		Raw: []byte(`{"type":"result","result":"ok"}`)})
	if r.CostUSD != 1.5 || r.DurationMS != 20 {
		t.Fatalf("result = %+v", r)
	}
}

func TestDetailClipRuneBoundary(t *testing.T) {
	big := strings.Repeat("é", DetailMax) // 2 bytes each
	v := View(sessionevents.Event{Kind: sessionevents.KindEvent, Raw: []byte(`{"type":"result","result":"` + big + `"}`)})
	if len(v.Detail) > DetailMax || !strings.HasSuffix(v.Detail, "é") || v.DetailTruncated != 2*DetailMax-len(v.Detail) {
		t.Fatalf("len %d truncated %d", len(v.Detail), v.DetailTruncated)
	}
}
