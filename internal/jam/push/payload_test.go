package push

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func TestBuildTitleAndFields(t *testing.T) {
	f := newFake()
	m := intercom.Squawk{ID: "sq-1", Channel: sess, From: ses, Body: "Review done: 3 comments"}
	got := Build(f, m, Recipient{User: u1, Reason: ReasonMessage}, 7, true)
	want := Payload{Title: "Code-review agent · PR #812 review", Body: "Review done: 3 comments", Channel: string(sess), ID: "sq-1", Unread: 7}
	if got != want {
		t.Fatalf("Build = %+v, want %+v", got, want)
	}
	// A chat's conversation label is the other members' labels.
	m = intercom.Squawk{ID: "sq-2", Channel: chat, From: u1, Body: "hi"}
	if got := Build(f, m, Recipient{User: u2, Reason: ReasonMessage}, 1, true); got.Title != "Alice · Alice, Code-review agent, ext" {
		t.Fatalf("chat title = %q", got.Title)
	}
	data, _ := json.Marshal(got)
	for _, k := range []string{`"title"`, `"body"`, `"channel"`, `"id"`, `"unread"`} {
		if !strings.Contains(string(data), k) {
			t.Fatalf("payload JSON %s lacks %s", data, k)
		}
	}
}

func TestBuildBody(t *testing.T) {
	f := newFake()
	ask := askMsg(room, intercom.AskEvent{Event: intercom.AskEventAsk, Question: "Merge **now**?", Of: []ident.ID{u1}})
	tests := []struct {
		name string
		m    intercom.Squawk
		want string
	}{
		{"bold", intercom.Squawk{Body: "this is **bold** and *it* and __u__ and ~~s~~"}, "this is bold and it and u and s"},
		{"snake case kept", intercom.Squawk{Body: "see my_var_name"}, "see my_var_name"},
		{"link", intercom.Squawk{Body: "see [the PR](https://x.test/pr/1) now"}, "see the PR now"},
		{"image", intercom.Squawk{Body: "![diagram](https://x.test/a.png) attached"}, "diagram attached"},
		{"inline code", intercom.Squawk{Body: "run `just test` first"}, "run just test first"},
		{"code fence", intercom.Squawk{Body: "fix:\n```go\nfunc x() {}\n```\nthen ship"}, "fix: [code] then ship"},
		{"heading and bullets", intercom.Squawk{Body: "## Summary\n- one\n* two\n1. three\n> quoted"}, "Summary one two three quoted"},
		{"plain text untouched", intercom.Squawk{Body: "a **b**", ContentType: intercom.ContentPlain}, "a **b**"},
		{"ask", ask, "❓ Merge now?"},
		{"call-in", intercom.Squawk{ID: "callin:chn_x:" + string(u2) + ":1", Body: "Alice called in Bob", ContentType: intercom.ContentPlain}, "📣 Alice called in Bob"},
		{"escalation", intercom.Squawk{ID: "escalate:" + string(ses) + ":1:2:" + string(u1), Body: "needs a person", ContentType: intercom.ContentPlain}, "⚠️ needs a person"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.m.Channel == "" {
				tt.m.Channel = sess
				tt.m.From = ses
			}
			if got := Build(f, tt.m, Recipient{User: u1, Reason: ReasonMessage}, 0, true).Body; got != tt.want {
				t.Fatalf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCutsAt140Runes(t *testing.T) {
	f := newFake()
	body := strings.Repeat("ü日", 200)
	got := Build(f, intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: body}, Recipient{User: u1}, 0, true).Body
	if n := utf8.RuneCountInString(got); n != 140 {
		t.Fatalf("body runes = %d, want 140", n)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || !strings.HasPrefix(body, strings.TrimSuffix(got, "…")) {
		t.Fatalf("body = %q", got)
	}
	short := strings.Repeat("ü", 140)
	if got := Build(f, intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: short}, Recipient{User: u1}, 0, true).Body; got != short {
		t.Fatalf("140 runes exactly was cut: %q", got)
	}
}

func TestBuildNoPreview(t *testing.T) {
	f := newFake()
	ask := askMsg(room, intercom.AskEvent{Event: intercom.AskEventAsk, Question: "secret?", Of: []ident.ID{u1}})
	for _, m := range []intercom.Squawk{{ID: "a", Channel: sess, From: ses, Body: "secret"}, ask} {
		if got := Build(f, m, Recipient{User: u1}, 0, false).Body; got != "New message" {
			t.Fatalf("preview off body = %q", got)
		}
	}
}

func TestBuildFitsIn3KB(t *testing.T) {
	f := newFake()
	f.labels[ses] = strings.Repeat("<&>", 5000)
	c := f.channels[sess]
	c.Label = strings.Repeat("\"日", 5000)
	f.channels[sess] = c
	m := intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: strings.Repeat("<日>", 100_000/5)}
	data, err := json.Marshal(Build(f, m, Recipient{User: u1}, 12345, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxPayload {
		t.Fatalf("payload = %d bytes, want ≤ %d", len(data), MaxPayload)
	}
	if MaxPayload != 3072 {
		t.Fatalf("MaxPayload = %d", MaxPayload)
	}
}
