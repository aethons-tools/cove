package jam

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// The channel registry (intercom slice 2a): a channel is a conversation,
// owned by the intercom as a row — id, project, kind, key, status, bindings,
// members — whose meaning its source (the kind) supplies. Rooms (kind "room")
// are what roster channels became; chats and ticket channels arrive with the
// sources. See docs/superpowers/specs/2026-10-06-intercom-slice2-channels-design.md.

// SourceKind is a channel's kind: what the conversation is anchored to, and
// so the channel source that owns its policy.
type SourceKind string

const (
	SourceChat   SourceKind = "chat"   // a fixed member set
	SourceTicket SourceKind = "ticket" // a work item: "<connection id>/<issue key>"
	SourceRoom   SourceKind = "room"   // a topic, named per project
)

var sourceKinds = []SourceKind{SourceChat, SourceTicket, SourceRoom}

// StatusArchived is a channel that ended: its history stays readable, it
// accepts no posts or joins, and its key and bindings are free again.
const StatusArchived Status = "archived"

// BindingMode says which ways a binding carries a channel: BindBoth renders
// to the surface and ingests from it (so a (connection, ref) has at most one
// live BindBoth binding); BindEgress only renders (e.g. a chat shown as an
// @-mention on a ticket that belongs to another channel).
type BindingMode string

const (
	BindBoth   BindingMode = "both"
	BindEgress BindingMode = "egress"
)

// Binding is a surface a channel is rendered on: a ref (a Linear issue key, a
// Discord channel id) on a connection.
type Binding struct {
	ConnectionID ident.ID    `json:"connection_id"`
	Ref          string      `json:"ref"`
	Mode         BindingMode `json:"mode"`
}

// Channel is one conversation. Key identifies it within its project and kind
// (live channels only); Label is how it is shown.
type Channel struct {
	ID        ident.ID   `json:"id"`
	ProjectID ident.ID   `json:"project_id"`
	Kind      SourceKind `json:"kind"`
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	Status    Status     `json:"status"`
	Bindings  []Binding  `json:"bindings,omitempty"`
}

// ChannelMember is one membership of a participant (a session, user or
// account id; grandfathered session ids included) in a channel, from the log
// position it joined at. LeftSeq 0 = still a member.
type ChannelMember struct {
	ParticipantID ident.ID `json:"participant_id"`
	JoinedSeq     int64    `json:"joined_seq"`
	LeftSeq       int64    `json:"left_seq,omitempty"`
}

var (
	ErrChannelNotFound = errors.New("channel not found")
	ErrChannelExists   = errors.New("a live channel with that key already exists")
	ErrBindingTaken    = errors.New("binding already belongs to another channel")
)

// ChannelStore is the Store's channel registry. CreateChannel mints the id
// when it is empty; Get* include archived channels; By* lookups and List*
// see live ones only. Returned values are copies.
type ChannelStore interface {
	CreateChannel(c Channel) (Channel, error)
	GetChannel(id ident.ID) (Channel, bool)
	ChannelByKey(project ident.ID, kind SourceKind, key string) (Channel, bool)
	// ChannelByBinding is the live channel holding the BindBoth binding
	// (conn, ref): where ingress from that surface goes.
	ChannelByBinding(conn ident.ID, ref string) (Channel, bool)
	// ListChannels returns a project's live channels of kind, sorted by key.
	ListChannels(project ident.ID, kind SourceKind) []Channel
	RenameChannel(id ident.ID, key, label string) error
	SetChannelBindings(id ident.ID, bs []Binding) error
	// ArchiveChannel ends a channel (ErrRemoved if it already has).
	ArchiveChannel(id ident.ID) error

	// JoinChannel makes p a member from seq on (a no-op while it is one);
	// LeaveChannel ends its membership at seq (a no-op when it is none).
	JoinChannel(ch, p ident.ID, seq int64) error
	LeaveChannel(ch, p ident.ID, seq int64) error
	// ChannelMembers returns ch's current members, sorted by participant.
	ChannelMembers(ch ident.ID) []ChannelMember
	// ChannelsOf returns the channels p is currently a member of, sorted.
	ChannelsOf(p ident.ID) []ident.ID
}

// ---- reads ----

func (m *memState) GetChannel(id ident.ID) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.channels[id]
	return copyChannel(c), ok
}

func (m *memState) ChannelByKey(project ident.ID, kind SourceKind, key string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.liveChannelByKey(project, kind, key)
	return copyChannel(c), ok
}

func (m *memState) ChannelByBinding(conn ident.ID, ref string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.ingressHolder(Binding{ConnectionID: conn, Ref: ref, Mode: BindBoth})
	return copyChannel(c), ok
}

func (m *memState) ListChannels(project ident.ID, kind SourceKind) []Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Channel
	for _, c := range m.channels {
		if c.ProjectID == project && c.Kind == kind && c.Status == StatusLive {
			out = append(out, copyChannel(c))
		}
	}
	slices.SortFunc(out, func(a, b Channel) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

func (m *memState) ChannelMembers(ch ident.ID) []ChannelMember {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ChannelMember
	for _, ms := range m.chanMembers[ch] {
		if ms.LeftSeq == 0 {
			out = append(out, ms)
		}
	}
	slices.SortFunc(out, func(a, b ChannelMember) int {
		return cmp.Or(cmp.Compare(a.ParticipantID, b.ParticipantID), cmp.Compare(a.JoinedSeq, b.JoinedSeq))
	})
	return out
}

func (m *memState) ChannelsOf(p ident.ID) []ident.ID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ident.ID
	for ch := range m.chanMembers {
		if _, ok := m.currentMember(ch, p); ok {
			out = append(out, ch)
		}
	}
	slices.Sort(out)
	return out
}

// liveChannelByKey finds the live channel (project, kind, key). Caller holds mu.
func (m *memState) liveChannelByKey(project ident.ID, kind SourceKind, key string) (Channel, bool) {
	for _, c := range m.channels {
		if c.ProjectID == project && c.Kind == kind && c.Key == key && c.Status == StatusLive {
			return c, true
		}
	}
	return Channel{}, false
}

// ingressHolder finds the live channel holding BindBoth binding b. Caller holds mu.
func (m *memState) ingressHolder(b Binding) (Channel, bool) {
	for _, c := range m.channels {
		if c.Status != StatusLive {
			continue
		}
		for _, x := range c.Bindings {
			if x.Mode == BindBoth && x.ConnectionID == b.ConnectionID && x.Ref == b.Ref {
				return c, true
			}
		}
	}
	return Channel{}, false
}

// currentMember is p's open membership row in ch (its index). Caller holds mu.
func (m *memState) currentMember(ch, p ident.ID) (int, bool) {
	for i, ms := range m.chanMembers[ch] {
		if ms.ParticipantID == p && ms.LeftSeq == 0 {
			return i, true
		}
	}
	return 0, false
}

// boundConnection reports whether a live channel binds conn. Caller holds mu.
func (m *memState) boundConnection(conn ident.ID) (Channel, bool) {
	for _, c := range m.channels {
		if c.Status == StatusLive && slices.ContainsFunc(c.Bindings, func(b Binding) bool { return b.ConnectionID == conn }) {
			return c, true
		}
	}
	return Channel{}, false
}

// ---- prepare (validate; never mutate) ----

// prepareCreateChannel validates c as a new live channel and mints its id
// when empty. Caller holds mu.
func (m *memState) prepareCreateChannel(c Channel) (Channel, error) {
	c = copyChannel(c)
	id, err := m.newEntityID(c.ID, ident.Channel)
	if err != nil {
		return Channel{}, err
	}
	if _, ok := m.channels[id]; ok {
		return Channel{}, fmt.Errorf("%w: id %s", ErrChannelExists, id)
	}
	c.ID, c.Status = id, StatusLive
	if _, ok := m.projectByID(c.ProjectID); !ok {
		return Channel{}, fmt.Errorf("%w: %s", ErrProjectNotFound, c.ProjectID)
	}
	if !slices.Contains(sourceKinds, c.Kind) {
		return Channel{}, fmt.Errorf("channel: unknown kind %q", c.Kind)
	}
	if err := m.checkChannelKey(c.ProjectID, c.Kind, c.Key, c.ID); err != nil {
		return Channel{}, err
	}
	if err := m.checkBindings(c.Bindings, c.ID); err != nil {
		return Channel{}, err
	}
	return c, nil
}

func (m *memState) checkChannelKey(project ident.ID, kind SourceKind, key string, self ident.ID) error {
	if key == "" {
		return fmt.Errorf("channel: key is required")
	}
	if o, ok := m.liveChannelByKey(project, kind, key); ok && o.ID != self {
		return fmt.Errorf("%w: %s %q", ErrChannelExists, kind, key)
	}
	return nil
}

func (m *memState) checkBindings(bs []Binding, self ident.ID) error {
	for i, b := range bs {
		if b.Ref == "" {
			return fmt.Errorf("channel: binding ref is required")
		}
		if b.Mode != BindBoth && b.Mode != BindEgress {
			return fmt.Errorf("channel: unknown binding mode %q", b.Mode)
		}
		if _, err := m.liveConnection(b.ConnectionID); err != nil {
			return err
		}
		if b.Mode != BindBoth {
			continue
		}
		if o, ok := m.ingressHolder(b); ok && o.ID != self {
			return fmt.Errorf("%w: %s on %s is %s %q's", ErrBindingTaken, b.Ref, b.ConnectionID, o.Kind, o.Key)
		}
		if slices.ContainsFunc(bs[:i], func(x Binding) bool { return x == b }) {
			return fmt.Errorf("channel: binding %s on %s given twice", b.Ref, b.ConnectionID)
		}
	}
	return nil
}

// liveChannel is the live channel id. Caller holds mu.
func (m *memState) liveChannel(id ident.ID) (Channel, error) {
	c, ok := m.channels[id]
	switch {
	case !ok:
		return Channel{}, fmt.Errorf("%w: %s", ErrChannelNotFound, id)
	case c.Status != StatusLive:
		return Channel{}, fmt.Errorf("%w: channel %s", ErrRemoved, id)
	}
	return copyChannel(c), nil
}

func (m *memState) prepareRenameChannel(id ident.ID, key, label string) (Channel, error) {
	c, err := m.liveChannel(id)
	if err != nil {
		return Channel{}, err
	}
	if err := m.checkChannelKey(c.ProjectID, c.Kind, key, id); err != nil {
		return Channel{}, err
	}
	c.Key, c.Label = key, label
	return c, nil
}

func (m *memState) prepareSetChannelBindings(id ident.ID, bs []Binding) (Channel, error) {
	c, err := m.liveChannel(id)
	if err != nil {
		return Channel{}, err
	}
	if err := m.checkBindings(bs, id); err != nil {
		return Channel{}, err
	}
	c.Bindings = slices.Clone(bs)
	return c, nil
}

func (m *memState) prepareArchiveChannel(id ident.ID) (Channel, error) {
	c, err := m.liveChannel(id)
	if err != nil {
		return Channel{}, err
	}
	c.Status = StatusArchived
	return c, nil
}

// prepareJoinChannel validates a join; join is false when p already is a
// member (a no-op). Caller holds mu.
func (m *memState) prepareJoinChannel(ch, p ident.ID) (join bool, err error) {
	if _, err := m.liveChannel(ch); err != nil {
		return false, err
	}
	if p == "" {
		return false, fmt.Errorf("channel: participant is required")
	}
	_, member := m.currentMember(ch, p)
	return !member, nil
}

// prepareLeaveChannel reports whether p has a membership in ch to end.
func (m *memState) prepareLeaveChannel(ch, p ident.ID) (bool, error) {
	if _, ok := m.channels[ch]; !ok {
		return false, fmt.Errorf("%w: %s", ErrChannelNotFound, ch)
	}
	_, member := m.currentMember(ch, p)
	return member, nil
}

// ---- apply ----

func (m *memState) applyPutChannel(c Channel) { m.channels[c.ID] = copyChannel(c) }

// applyJoinChannel opens a membership; a row that joined at the same seq
// (left and rejoined with nothing between) is reopened instead.
func (m *memState) applyJoinChannel(ch, p ident.ID, seq int64) {
	for i, ms := range m.chanMembers[ch] {
		if ms.ParticipantID == p && ms.JoinedSeq == seq {
			m.chanMembers[ch][i].LeftSeq = 0
			return
		}
	}
	m.chanMembers[ch] = append(m.chanMembers[ch], ChannelMember{ParticipantID: p, JoinedSeq: seq})
}

func (m *memState) applyLeaveChannel(ch, p ident.ID, seq int64) {
	if i, ok := m.currentMember(ch, p); ok {
		m.chanMembers[ch][i].LeftSeq = max(seq, m.chanMembers[ch][i].JoinedSeq)
	}
}

// applyDropProjectChannels removes a removed project's channels and their
// memberships (the project's rows cascade the same way in Postgres).
func (m *memState) applyDropProjectChannels(project ident.ID) {
	for id, c := range m.channels {
		if c.ProjectID == project {
			delete(m.channels, id)
			delete(m.chanMembers, id)
		}
	}
}

func copyChannel(c Channel) Channel {
	c.Bindings = slices.Clone(c.Bindings)
	return c
}

// ---- MemStore ----

func (fs *MemStore) CreateChannel(c Channel) (Channel, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareCreateChannel(c)
	if err != nil {
		return Channel{}, err
	}
	fs.applyPutChannel(c)
	return copyChannel(c), nil
}

func (fs *MemStore) RenameChannel(id ident.ID, key, label string) error {
	return fs.putChannelWith(func() (Channel, error) { return fs.prepareRenameChannel(id, key, label) })
}

func (fs *MemStore) SetChannelBindings(id ident.ID, bs []Binding) error {
	return fs.putChannelWith(func() (Channel, error) { return fs.prepareSetChannelBindings(id, bs) })
}

func (fs *MemStore) ArchiveChannel(id ident.ID) error {
	return fs.putChannelWith(func() (Channel, error) { return fs.prepareArchiveChannel(id) })
}

func (fs *MemStore) putChannelWith(prepare func() (Channel, error)) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := prepare()
	if err != nil {
		return err
	}
	fs.applyPutChannel(c)
	return nil
}

func (fs *MemStore) JoinChannel(ch, p ident.ID, seq int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	join, err := fs.prepareJoinChannel(ch, p)
	if err != nil || !join {
		return err
	}
	fs.applyJoinChannel(ch, p, seq)
	return nil
}

func (fs *MemStore) LeaveChannel(ch, p ident.ID, seq int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	leave, err := fs.prepareLeaveChannel(ch, p)
	if err != nil || !leave {
		return err
	}
	fs.applyLeaveChannel(ch, p, seq)
	return nil
}
