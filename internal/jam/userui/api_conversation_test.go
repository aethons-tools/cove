package userui

import (
	"encoding/json"
	"testing"
	"time"
)

// The view models' JSON names are the app's contract (types.ts): pin them,
// and that template-only fields stay out.
func TestConversationJSONShape(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC)
	c := Conversation{ChannelID: "chn_x", Label: "eng", SendTo: "chn_x", LastSeq: 3, Earlier: true,
		Messages:   []MessageRow{{From: "bob", Body: "<p>hi</p>", Raw: "hi", At: "09:05", Time: at, Seq: 3, Card: "ask", Ask: &AskCard{ID: "a1"}}},
		Sessions:   []SessionRow{{Label: "spider", Text: "is running", Tool: "Bash", Class: "busy"}},
		CallIn:     []NewMessageOption{{To: "user:usr_1", Label: "bob", Kind: "person"}},
		SessionIDs: []string{"ses_1"}, HasMessages: true,
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"channelId", "label", "kind", "project", "phase", "needsYou", "sendTo", "messages", "lastSeq", "earlier", "sessionIds", "sessions", "canJoin", "canLeave", "callIn"} {
		if _, ok := got[k]; !ok {
			t.Errorf("conversation JSON lacks %q: %s", k, b)
		}
	}
	for _, k := range []string{"HasMessages", "hasMessages", "OpenAsks", "openAsks", "Board", "board", "Head", "head", "NewBoard", "newBoard"} {
		if _, ok := got[k]; ok {
			t.Errorf("conversation JSON carries template-only %q", k)
		}
	}
	m := got["messages"].([]any)[0].(map[string]any)
	for k, want := range map[string]any{"from": "bob", "body": "<p>hi</p>", "raw": "hi", "at": "09:05", "time": "2026-10-26T09:05:00Z", "seq": float64(3), "card": "ask", "mine": false, "plain": false, "system": false} {
		if m[k] != want {
			t.Errorf("message[%q] = %v, want %v", k, m[k], want)
		}
	}
	for _, k := range []string{"Ask", "ask", "files", "reAsk", "Board", "board", "Channel", "channel"} {
		if _, ok := m[k]; ok {
			t.Errorf("message JSON carries %q (template-only, or omitempty when empty): %s", k, b)
		}
	}
}
