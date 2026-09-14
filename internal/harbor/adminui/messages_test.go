package adminui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor/adminui"
	"github.com/aethons-tools/cove/internal/msglog"
)

// newMsgLog opens a hermetic Log in a temp dir and appends the given messages.
func newMsgLog(t *testing.T, msgs ...msglog.Message) *msglog.Log {
	t.Helper()
	l, err := msglog.Open(t.TempDir()+"/messages.jsonl", testLogger())
	if err != nil {
		t.Fatalf("msglog.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	for _, m := range msgs {
		if _, err := l.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return l
}

func msgHandler(t *testing.T, l adminui.MessageReader) http.Handler {
	t.Helper()
	return adminui.Handler(newStore(t), testLogger(), nil, anyCred, l)
}

func actor(ref string) msglog.Target   { return msglog.Target{Kind: "actor", Ref: ref} }
func human(ref string) msglog.Target   { return msglog.Target{Kind: "human", Ref: ref} }
func channel(ref string) msglog.Target { return msglog.Target{Kind: "channel", Ref: ref} }

func TestMessagesRendersNewestFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	l := newMsgLog(t,
		msglog.Message{From: actor("cove-1"), To: []msglog.Target{human("alice")}, Body: "older ping", At: t0, Project: "acme"},
		msglog.Message{From: human("alice"), To: []msglog.Target{actor("cove-1")}, Body: "newer reply", At: t0.Add(time.Hour), Project: "acme"},
	)
	body := get(t, msgHandler(t, l), "/ui/messages").Body.String()
	for _, want := range []string{"<nav", "Messages", "older ping", "newer reply", "actor:cove-1", "human:alice", "acme"} {
		if !strings.Contains(body, want) {
			t.Errorf("messages page missing %q; got:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, "newer reply"), strings.Index(body, "older ping"); i > j {
		t.Errorf("newest message must render first: newer@%d older@%d", i, j)
	}
}

func TestMessagesReachBadges(t *testing.T) {
	l := newMsgLog(t,
		msglog.Message{From: actor("cove-1"), To: []msglog.Target{actor("cove-2"), human("alice")}, Body: "hi"},
	)
	body := get(t, msgHandler(t, l), "/ui/messages").Body.String()
	// actor:cove-2 → internal, human:alice → external (msglog.Classify).
	if !strings.Contains(body, "internal") || !strings.Contains(body, "external") {
		t.Errorf("expected internal+external reach badges; got:\n%s", body)
	}
}

func TestMessagesEmptyLog(t *testing.T) {
	body := get(t, msgHandler(t, newMsgLog(t)), "/ui/messages").Body.String()
	if !strings.Contains(body, "No messages logged yet") {
		t.Errorf("empty log should say so; got:\n%s", body)
	}
}

func TestMessagesNotConfigured(t *testing.T) {
	rec := get(t, msgHandler(t, nil), "/ui/messages")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/messages (nil reader) = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("nil reader should render a not-configured notice; got:\n%s", rec.Body.String())
	}
}

func TestMessagesNavLinkPresentOnOtherPages(t *testing.T) {
	body := get(t, msgHandler(t, newMsgLog(t)), "/ui/coves").Body.String()
	if !strings.Contains(body, `href="/ui/messages"`) {
		t.Errorf("nav should link to /ui/messages; got:\n%s", body)
	}
}
