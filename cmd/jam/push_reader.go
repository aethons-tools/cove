package main

import (
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/push"
)

// pushReader is push.Reader over the store and the intercom: what the push
// relay reads to pick a squawk's recipients and word its notification.
type pushReader struct {
	store jam.Store
	ic    *jam.Intercom
}

var _ push.Reader = pushReader{}

// Channel is a live channel (an archived one notifies nobody).
func (r pushReader) Channel(id ident.ID) (jam.Channel, bool) {
	ch, ok := r.store.GetChannel(id)
	return ch, ok && ch.Status == jam.StatusLive
}

// Members are ch's current members.
func (r pushReader) Members(ch ident.ID) []ident.ID {
	var out []ident.ID
	for _, m := range r.store.ChannelMembers(ch) {
		out = append(out, m.ParticipantID)
	}
	return out
}

// CanSee mirrors the live audience a post is delivered to (Intercom's
// audience): user u is a live user, still a member of the channel's
// project, and the channel's kind lets them see it.
func (r pushReader) CanSee(u ident.ID, m intercom.Squawk) bool {
	ch, ok := r.Channel(m.Channel)
	if !ok || u.Kind() != ident.User {
		return false
	}
	usr, ok := r.store.GetUser(u)
	return ok && usr.Status == jam.StatusLive && r.store.IsMember(ch.ProjectID, u) && r.ic.CanSee(u, ch)
}

// ReadThrough is how far u has read ch (the /app/user unread cursor).
func (r pushReader) ReadThrough(u, ch ident.ID) int64 { return r.store.ChannelReads(u)[ch] }

// PartyLabel is a participant's display label.
func (r pushReader) PartyLabel(id ident.ID) string { return r.ic.PartyOf(id).Label }

// ChannelLabel is ch's label as forUser's rail shows it: a chat's other
// members' labels, else the channel's own.
func (r pushReader) ChannelLabel(ch jam.Channel, forUser ident.ID) string {
	if ch.Kind != jam.SourceChat {
		return ch.Label
	}
	var others []string
	for _, m := range r.store.ChannelMembers(ch.ID) {
		if m.ParticipantID != forUser {
			others = append(others, r.ic.PartyOf(m.ParticipantID).Label)
		}
	}
	if len(others) == 0 {
		return ch.Label
	}
	return strings.Join(others, ", ")
}

// pushPayload is the push surface's Payload: m's notification for user,
// re-checking at send time that user is still one of its recipients (still
// a member who can see it, not focused on its channel, not already read on
// another device). unread is user's
// total unread (the app badge).
func pushPayload(r pushReader, focus *push.Focus, unread func(ident.ID) int, preview bool) func(intercom.Squawk, ident.ID) (push.Payload, bool) {
	return func(m intercom.Squawk, user ident.ID) (push.Payload, bool) {
		var focused func(user, channel ident.ID) bool
		if focus != nil {
			focused = focus.Focused
		}
		rcs := push.Recipients(r, m, focused)
		i := slices.IndexFunc(rcs, func(rc push.Recipient) bool { return rc.User == user })
		if i < 0 {
			return push.Payload{}, false
		}
		return push.Build(r, m, rcs[i], unread(user), preview), true
	}
}
