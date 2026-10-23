package push

import (
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func TestRecipients(t *testing.T) {
	never := func(_, _ ident.ID) bool { return false }
	tests := []struct {
		name    string
		m       intercom.Squawk
		setup   func(*fakeReader)
		focused func(user, channel ident.ID) bool
		want    []Recipient
	}{
		{name: "chat message: other users, not author, sessions or accounts",
			m:    intercom.Squawk{ID: "a", Channel: chat, From: u1, Body: "hi"},
			want: []Recipient{{u2, ReasonMessage}}},
		{name: "session channel: its users",
			m:    intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: "done"},
			want: []Recipient{{u1, ReasonMessage}, {u2, ReasonMessage}}},
		{name: "room plain message: none",
			m: intercom.Squawk{ID: "a", Channel: room, From: ses, Body: "x"}},
		{name: "ticket plain message: none",
			m: intercom.Squawk{ID: "a", Channel: tick, From: u1, Body: "x"}},
		{name: "unknown channel: none",
			m: intercom.Squawk{ID: "a", Channel: "chn_gone", From: u1, Body: "x"}},
		{name: "ask of u1 in a room: u1 only",
			m:    askMsg(room, intercom.AskEvent{Event: intercom.AskEventAsk, Question: "ok?", Of: []ident.ID{u1}}),
			want: []Recipient{{u1, ReasonAsk}}},
		{name: "ask of a non-member: none",
			m: askMsg(tick, intercom.AskEvent{Event: intercom.AskEventAsk, Question: "ok?", Of: []ident.ID{u3}})},
		{name: "ask of u1 in a session channel: u1 ask, u2 message",
			m:    askMsg(sess, intercom.AskEvent{Event: intercom.AskEventAsk, Question: "ok?", Of: []ident.ID{u1}}),
			want: []Recipient{{u1, ReasonAsk}, {u2, ReasonMessage}}},
		{name: "ask amend: none",
			m: askMsg(sess, intercom.AskEvent{Event: intercom.AskEventAmend, AskID: "x", Question: "ok?", Of: []ident.ID{u1}})},
		{name: "ask withdraw: none",
			m: askMsg(room, intercom.AskEvent{Event: intercom.AskEventWithdraw, AskID: "x", Of: []ident.ID{u1}})},
		{name: "ask answer: none",
			m: askMsg(chat, intercom.AskEvent{Event: intercom.AskEventAnswer, AskID: "x", Choice: "yes"})},
		{name: "call-in notice in a ticket: the invitee",
			m:    intercom.Squawk{ID: "callin:" + string(tick) + ":" + string(u2) + ":123", Channel: tick, From: u1, Body: "Alice called in Bob"},
			want: []Recipient{{u2, ReasonCallIn}}},
		{name: "call-in of a session: none",
			m: intercom.Squawk{ID: "callin:" + string(tick) + ":" + string(ses) + ":123", Channel: tick, From: u1, Body: "x"}},
		{name: "escalation notice in a room: its targets",
			m:    intercom.Squawk{ID: "escalate:" + string(ses) + ":1:123:" + string(u1) + "," + string(u3), Channel: room, From: ses, Body: "help"},
			want: []Recipient{{u1, ReasonEscalation}, {u3, ReasonEscalation}}},
		{name: "a member who left the project is excluded",
			m:     intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: "x"},
			setup: func(f *fakeReader) { f.hidden[u2] = true },
			want:  []Recipient{{u1, ReasonMessage}}},
		{name: "already read through it: excluded",
			m: intercom.Squawk{ID: "a", Seq: 7, Channel: sess, From: ses, Body: "x"},
			setup: func(f *fakeReader) {
				f.reads[u1] = map[ident.ID]int64{sess: 7}
				f.reads[u2] = map[ident.ID]int64{sess: 9}
			},
			want: nil},
		{name: "read only up to before it, or another channel: included",
			m: intercom.Squawk{ID: "a", Seq: 7, Channel: sess, From: ses, Body: "x"},
			setup: func(f *fakeReader) {
				f.reads[u1] = map[ident.ID]int64{sess: 6}
				f.reads[u2] = map[ident.ID]int64{chat: 99}
			},
			want: []Recipient{{u1, ReasonMessage}, {u2, ReasonMessage}}},
		{name: "focused on that channel: excluded",
			m:       intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: "x"},
			focused: func(u, ch ident.ID) bool { return u == u1 && ch == sess },
			want:    []Recipient{{u2, ReasonMessage}}},
		{name: "focused on another channel: included",
			m:       intercom.Squawk{ID: "a", Channel: sess, From: ses, Body: "x"},
			focused: func(u, ch ident.ID) bool { return u == u1 && ch == chat },
			want:    []Recipient{{u1, ReasonMessage}, {u2, ReasonMessage}}},
		{name: "nil focus: nobody focused",
			m:       intercom.Squawk{ID: "a", Channel: chat, From: u2, Body: "x"},
			focused: nil,
			want:    []Recipient{{u1, ReasonMessage}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake()
			if tt.setup != nil {
				tt.setup(f)
			}
			focused := tt.focused
			if focused == nil && tt.name != "nil focus: nobody focused" {
				focused = never
			}
			got := Recipients(f, tt.m, focused)
			slices.SortFunc(tt.want, func(a, b Recipient) int { return strings.Compare(string(a.User), string(b.User)) })
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Recipients = %+v, want %+v", got, tt.want)
			}
		})
	}
}
