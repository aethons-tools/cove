package push

import (
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeReader is a Reader over fixed channels, members and labels; hidden
// users fail CanSee (they left the project).
type fakeReader struct {
	channels map[ident.ID]jam.Channel
	members  map[ident.ID][]ident.ID
	labels   map[ident.ID]string
	hidden   map[ident.ID]bool
}

func (f *fakeReader) Channel(id ident.ID) (jam.Channel, bool) {
	ch, ok := f.channels[id]
	return ch, ok
}

func (f *fakeReader) Members(ch ident.ID) []ident.ID { return f.members[ch] }

func (f *fakeReader) CanSee(u ident.ID, _ intercom.Squawk) bool { return !f.hidden[u] }

func (f *fakeReader) PartyLabel(id ident.ID) string { return f.labels[id] }

func (f *fakeReader) ChannelLabel(ch jam.Channel, forUser ident.ID) string {
	if ch.Kind != jam.SourceChat {
		return ch.Label
	}
	var out []string
	for _, m := range f.members[ch.ID] {
		if m != forUser {
			out = append(out, f.labels[m])
		}
	}
	return strings.Join(out, ", ")
}

var (
	u1   = ident.New(ident.User)
	u2   = ident.New(ident.User)
	u3   = ident.New(ident.User)
	ses  = ident.New(ident.Session)
	acc  = ident.New(ident.Account)
	chat = ident.ID("chn_chat")
	sess = ident.ID("chn_sess")
	room = ident.ID("chn_room")
	tick = ident.ID("chn_tick")
)

func newFake() *fakeReader {
	return &fakeReader{
		channels: map[ident.ID]jam.Channel{
			chat: {ID: chat, Kind: jam.SourceChat},
			sess: {ID: sess, Kind: jam.SourceSession, Label: "PR #812 review"},
			room: {ID: room, Kind: jam.SourceRoom, Label: "eng"},
			tick: {ID: tick, Kind: jam.SourceTicket, Label: "ACME-7"},
		},
		members: map[ident.ID][]ident.ID{
			chat: {u1, u2, ses, acc},
			sess: {u1, u2, ses},
			room: {u1, u2, u3, ses},
			tick: {u1, u2, ses},
		},
		labels: map[ident.ID]string{u1: "Alice", u2: "Bob", u3: "Carol", ses: "Code-review agent", acc: "ext"},
		hidden: map[ident.ID]bool{},
	}
}

func askMsg(ch ident.ID, ev intercom.AskEvent) intercom.Squawk {
	return intercom.Squawk{ID: "ask-1", Channel: ch, From: ses, Body: ev.Question, Parts: []intercom.Part{intercom.AskPart(ev)}}
}
