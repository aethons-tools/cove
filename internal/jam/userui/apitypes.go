package userui

import (
	"html/template"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// This file holds every type the /app/user/api JSON carries. tygo reads only
// this file (tygo.yaml) to generate web/app/src/api/types.ts: keep JSON types
// here, give every field a json tag, and run `go generate ./internal/jam/userui/`
// after a change. Lists are never nil in a response (types.ts says T[]).

// APIError is every /app/user/api error body.
type APIError struct {
	Error string `json:"error"` // for people
	Code  string `json:"code"`  // for code: forbidden, not_found, bad_request, internal
}

// Bootstrap is what the app needs before its first render: injected into
// index.html by webapp, and served at GET /app/user/api/bootstrap (the dev
// server's index has none).
type Bootstrap struct {
	Brand     string   `json:"brand"`
	User      BootUser `json:"user"`
	Roles     string   `json:"roles"` // "member" or "member · admin"
	Admin     bool     `json:"admin"`
	SignOut   bool     `json:"signOut"`   // offer Sign out (/app/auth/logout)
	Tabs      []NavTab `json:"tabs"`      // the title bar's links
	AttachMax int64    `json:"attachMax"` // per-file cap; 0 = attachments off
	PushKey   string   `json:"pushKey"`   // VAPID public key; "" = Web Push off
	Boards    bool     `json:"boards"`    // whiteboards on and the editor built
}

// BootUser is the signed-in member.
type BootUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Initial string `json:"initial"`
}

// NavTab is one title-bar link (appui.Tab, flattened).
type NavTab struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Href       string `json:"href"`
	Badge      int    `json:"badge"`
	BadgeRed   bool   `json:"badgeRed"`
	BadgeTitle string `json:"badgeTitle"`
}

// RailResponse is GET /app/user/api/rail.
type RailResponse struct {
	Groups      []RailGroup `json:"groups"`
	UnreadTotal int         `json:"unreadTotal"`
}

// ChannelRow is one channel in the left rail, from the viewer's perspective.
type ChannelRow struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Project  string          `json:"project"`
	Kind     jam.ChannelKind `json:"kind"`
	NeedsYou bool            `json:"needsYou"`
	Phase    string          `json:"phase"`
	Unread   int             `json:"unread"`
	OpenAsks int             `json:"openAsks"`
	LastSeq  int64           `json:"lastSeq"`
	Selected bool            `json:"selected"`
	Mission  string          `json:"mission"` // the backing session's one-line focus; "" = none
}

// RailGroup is one attention section of the rail (Needs you / Sessions /
// Channels / History), non-empty.
type RailGroup struct {
	Bucket jam.AttentionBucket `json:"bucket"`
	Title  string              `json:"title"`
	Rows   []ChannelRow        `json:"rows"`
}

// MessageRow is one squawk in the conversation pane, from the viewer's side.
type MessageRow struct {
	From string        `json:"from"`
	Body template.HTML `json:"body"` // rendered per the squawk's content type (squawkrender): the app inserts it as HTML
	Raw  string        `json:"raw"`  // the body as sent, for the Markdown copy (escaped by the template)
	// Plain marks a text/plain squawk, shown literally with its whitespace.
	Plain bool      `json:"plain"`
	At    string    `json:"at"`   // server-local "15:04" (the templates)
	Time  time.Time `json:"time"` // when it was sent (the app's day dividers and local times)
	Mine  bool      `json:"mine"`
	Seq   int64     `json:"seq"`
	// Ask is the card of an ask's own squawk; System marks an ask's later
	// event (a one-line note, not a bubble); ReAsk a reply about an ask.
	Ask     *AskCard `json:"ask,omitempty"` // the ask's card (Card == "ask")
	System  bool     `json:"system"`
	SysText string   `json:"sysText"` // a System row's line
	ReAsk   *AskRef  `json:"reAsk,omitempty"`
	// Files are the message's attachments.
	Files []FileChip `json:"files,omitempty"`
	// Board is a board's card (the squawk that started it).
	Board *BoardCard `json:"-"` // the app shows boards from slice 7 (Card says which rows)
	// Channel is the conversation's id (a board card links into it).
	Channel string `json:"-"`
	// Card names the card this row is ("ask" or "board"; "" = none), so the
	// app can place what it doesn't render yet.
	Card string `json:"card"`
}

// FileChip is one attachment of a message: an inline image or a download.
type FileChip struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  string `json:"size"`
	Image bool   `json:"image"`
}

// AskRef points a reply at the ask it discusses.
type AskRef struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

// Conversation is the right-pane view of one open channel.
type Conversation struct {
	ChannelID   string          `json:"channelId"`
	Label       string          `json:"label"`
	Kind        jam.ChannelKind `json:"kind"`
	Project     string          `json:"project"`
	Phase       string          `json:"phase"`
	NeedsYou    bool            `json:"needsYou"`
	SendTo      string          `json:"sendTo"` // what the composer POSTs to /app/user/send ("" = read-only History)
	Messages    []MessageRow    `json:"messages"`
	HasMessages bool            `json:"-"`
	LastSeq     int64           `json:"lastSeq"`
	// Earlier: the API page has older messages before its first (GET
	// api/conversations/{c}?before=<first seq>).
	Earlier bool `json:"earlier"`
	// SessionIDs are the sessions taking part; Sessions is their presence
	// strip (filled by the handler, which holds the Presence source).
	SessionIDs []string     `json:"sessionIds"`
	Sessions   []SessionRow `json:"sessions"`
	OpenAsks   []AskCard    `json:"openAsks"` // the channel's open asks (the panel above the stream)
	// Membership controls: Join (a channel they can see but aren't in),
	// Leave (one they're in; never a chat), and who they may call in.
	CanJoin  bool               `json:"canJoin"`
	CanLeave bool               `json:"canLeave"`
	CallIn   []NewMessageOption `json:"callIn"`
	// Whiteboards: NewBoard offers New whiteboard (the viewer may post here
	// and the editor is built); Board is the board ?board= opened.
	NewBoard bool         `json:"-"`
	Board    *BoardEditor `json:"-"`
	// Session is the actor id of the session whose home this channel is,
	// when the viewer may see it ("" = none): the app's session header and
	// pane read api/sessions/{session}.
	Session string `json:"session"`
	// Head is the session header when this is a session's home channel.
	Head *SessionHead `json:"-"`
}

// NewMessageOption is one recipient offered in the New Message picker.
type NewMessageOption struct {
	To      string `json:"to"` // what to POST to /app/user/send: user:<id>, session:<id>, or a channel id
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Project string `json:"project"`
}

// SessionRow is one session's line in the presence strip: "<Label> is <Text>
// <Tool>". Class is busy (animated dots), wait (needs a human), or dim.
type SessionRow struct {
	Label string `json:"label"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	Class string `json:"class"`
}

// PresenceResponse is GET api/conversations/{c}/presence.
type PresenceResponse struct {
	Sessions []SessionRow `json:"sessions"`
}

// ReadRequest is POST api/conversations/{c}/read: the newest seq seen.
type ReadRequest struct {
	Seq int64 `json:"seq"`
}

// CallInRequest is POST api/conversations/{c}/call-in: whom (user:<id> or
// session:<actor>, one of Conversation.callIn's "to").
type CallInRequest struct {
	Who string `json:"who"`
}

// SendMessageRequest is POST api/messages: to a channel id, user:<id>, or
// session:<actor> (a NewMessageOption's "to").
type SendMessageRequest struct {
	To          string   `json:"to"`
	Body        string   `json:"body"`
	ContentType string   `json:"contentType,omitempty"` // "" = markdown; "text/plain" shown literally
	ReplyTo     string   `json:"replyTo,omitempty"`     // an ask in the same channel (Discuss)
	Attachments []string `json:"attachments,omitempty"` // refs from POST /app/user/uploads
}

// MessageSent is POST api/messages's answer: where the message went (a new
// message's new conversation) and its seq.
type MessageSent struct {
	Channel string `json:"channel"`
	Seq     int64  `json:"seq"`
}

// RecipientsResponse is GET api/recipients: whom the viewer may start a
// conversation with (the New message picker).
type RecipientsResponse struct {
	Options []NewMessageOption `json:"options"`
}

// AskCard is one ask as the viewer sees it.
type AskCard struct {
	ID         string               `json:"id"`
	Question   string               `json:"question"`
	Options    []intercom.AskOption `json:"options"`
	Version    int                  `json:"version"`
	State      string               `json:"state"` // open | answered | withdrawn
	AnsweredBy string               `json:"answeredBy"`
	Choice     string               `json:"choice"` // the chosen option's label ("other" for Other)
	Comment    string               `json:"comment"`
	Reason     string               `json:"reason"`
	Edited     bool                 `json:"edited"`
	CanAnswer  bool                 `json:"canAnswer"`
	For        string               `json:"for"` // who it is for, when Of names others than the viewer
}

// AnswerRequest is POST api/asks/{id}/answer: the version answered (a
// stale one is refused), an option id or "other", and a comment (needed
// for "other").
type AnswerRequest struct {
	Version int    `json:"version"`
	Choice  string `json:"choice"`
	Comment string `json:"comment,omitempty"`
}

// SessionHead is a session's header (GET api/sessions/{id}).
type SessionHead struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Kind     string     `json:"kind"` // standing | personal | ticket | session
	Ticket   string     `json:"ticket"`
	Role     string     `json:"role"`
	Project  string     `json:"project"`
	Owner    string     `json:"owner"`
	State    SessionRow `json:"state"`
	Since    string     `json:"since"` // time in the current state
	OpenAsks int        `json:"openAsks"`
	Cost     string     `json:"cost"` // "" = no turn yet
	TurnCost string     `json:"turnCost"`
	Tokens   string     `json:"tokens"`
	// Context is the context window's use, short; ContextTip exact; "" = no model call yet.
	Context    string `json:"context"`
	ContextTip string `json:"contextTip"`
	Uptime     string `json:"uptime"`
	Mission    string `json:"mission"` // the agent's one-line focus; "" = none
	SSH        string `json:"ssh"`     // the copyable command; "" = not allowed
	Studio     bool   `json:"studio"`  // the Studio tab: ssh allowed and framing on
	CanRelease bool   `json:"canRelease"`
	CanRestart bool   `json:"canRestart"`
	CanUpgrade bool   `json:"canUpgrade"`
	Ops        bool   `json:"ops"` // the session actions are wired
	// CanInterrupt, CanCompact, CanClear: shown whenever Ops is set; disabled when false.
	CanInterrupt bool   `json:"canInterrupt"`
	CanCompact   bool   `json:"canCompact"`
	CanClear     bool   `json:"canClear"`
	RestartLabel string `json:"restartLabel"` // Restart (standing) or End
	UpgradeState string `json:"upgradeState"`
}

// SessionPane is one tab of a session's pane (GET api/sessions/{id}/pane/{tab}).
type SessionPane struct {
	ID       string        `json:"id"`
	Tab      string        `json:"tab"`
	Previews []PreviewLink `json:"previews"`
	// PreviewsOff: this jam serves no previews (or the viewer may not open them).
	PreviewsOff bool        `json:"previewsOff"`
	Boards      []PaneBoard `json:"boards"`
	Files       []FileChip  `json:"files"`
	Asks        []PaneAsk   `json:"asks"`
	Links       []PaneLink  `json:"links"`
	Status      []StatusRow `json:"status"`
	// Streams are the session's recorded event streams (Thinking tab);
	// EventsOff: this jam records no session events. The app's Thinking
	// tab comes in slice 6.
	Streams   []sessionevents.StreamInfo `json:"-"`
	EventsOff bool                       `json:"-"`
	// Studio: the Studio tab's IDE may be framed here (ssh and framing on);
	// IDEOpen: its plain URL, for Open in new tab when a mint fails (slice 6).
	Studio  bool   `json:"-"`
	IDEOpen string `json:"-"`
}

// PreviewLink is one exposed port, opened in a new tab; Frame: View here
// (framed in the pane) is offered too.
type PreviewLink struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Port  int    `json:"port"`
	Frame bool   `json:"frame"`
}

// PaneBoard is a whiteboard started in the session's home channel.
type PaneBoard struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Channel string `json:"channel"`
}

// PaneAsk is an ask posted in the home channel, with its state.
type PaneAsk struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	State    string `json:"state"` // open | answered | withdrawn
}

// PaneLink is a URL from the home channel's messages.
type PaneLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// StatusRow is one line of the Status tab.
type StatusRow struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
