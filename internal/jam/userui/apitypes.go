package userui

import (
	"html/template"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
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
	PushFocus bool     `json:"pushFocus"` // report the open conversation (POST /app/user/push/focus)
	Boards    bool     `json:"boards"`    // whiteboards on (the app bundles its editor)
	// NewSession offers New session (personal sessions can be started here).
	NewSession bool `json:"newSession"`
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
	Mission  string          `json:"mission"` // the backing session's mission (its end goal); "" = none
	// Session is the actor id of the session whose home this channel is,
	// when the viewer may see it ("" = none), as api/conversations reports it.
	Session string `json:"session"`
	// SSH reports whether the viewer may SSH into Session's studio.
	SSH bool `json:"ssh"`
	// Card is a session channel's status card; nil for rooms, chats, and a
	// channel with no active session.
	Card *SessionCard `json:"card,omitempty"`
	// cardSession is the actor whose card stands for the row (jam.ChannelView.CardSession).
	cardSession string
}

// CardState is a status card's state: its sparkline level and pill colour.
type CardState string

const (
	CardThinking CardState = "thinking" // the turn is in a model call
	CardWorking  CardState = "working"  // the turn is in a tool call
	CardWaiting  CardState = "waiting"  // the turn ended with local waits (background tasks, kept processes, alarms)
	CardIdle     CardState = "idle"     // the turn ended; it waits only on messages (or a person)
	CardSleeping CardState = "sleeping" // the studio is paused
)

// SessionCard is a session's status card (docs/superpowers/specs/
// 2026-10-30-session-status-card-design.md): the rail row of a session
// channel, and the extension's Session Status pane.
type SessionCard struct {
	Session string      `json:"session"` // actor id
	Name    string      `json:"name"`
	State   CardState   `json:"state"`
	Pill    string      `json:"pill"`    // the pill's text: "working", "needs you", "paused"…
	Tool    string      `json:"tool"`    // the tool in progress while working; "" otherwise
	Mission string      `json:"mission"` // the end goal or standing order; "" = none
	Focus   string      `json:"focus"`   // what the session is on right now; "" = none
	Counts  CardCounts  `json:"counts"`
	Context CardContext `json:"context"`
	// Inits is how many system/init events Jam has seen for the session since
	// it started; the app marks its sparkline where this goes up.
	Inits int `json:"inits"`
	// Color is the session's color (#rrggbb; "" = none), filling its icon.
	Color string `json:"color,omitempty"`
}

// CardCounts are a card's count icons, each hidden at 0.
type CardCounts struct {
	Asks      int `json:"asks"`
	Alarms    int `json:"alarms"`
	Previews  int `json:"previews"`
	Processes int `json:"processes"` // kept processes
	Subagents int `json:"subagents"` // running now (sessionevents.Presence.Subagents)
}

// CardContext is how full the main thread's context window is, in tokens;
// Window 0 = not reported yet (no bar).
type CardContext struct {
	Used   int64 `json:"used"`
	Window int64 `json:"window"`
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
	Raw  string        `json:"raw"`  // the body as sent, for the Markdown copy (text, never markup)
	// Plain marks a text/plain squawk, shown literally with its whitespace.
	Plain bool      `json:"plain"`
	At    string    `json:"at"`   // server-local "15:04"
	Time  time.Time `json:"time"` // when it was sent (the app's day dividers and local times)
	Mine  bool      `json:"mine"`
	Seq   int64     `json:"seq"`
	// Color is the sending session's color, its bubble's border ("" = a
	// person, or a session with none).
	Color string `json:"color,omitempty"`
	// Ask is the card of an ask's own squawk; System marks an ask's later
	// event (a one-line note, not a bubble); ReAsk a reply about an ask.
	Ask     *AskCard `json:"ask,omitempty"` // the ask's card (Card == "ask")
	System  bool     `json:"system"`
	SysText string   `json:"sysText"` // a System row's line
	ReAsk   *AskRef  `json:"reAsk,omitempty"`
	// Files are the message's attachments.
	Files []FileChip `json:"files,omitempty"`
	// Board is a board's card (the squawk that started it).
	Board *BoardCard `json:"board,omitempty"` // the board's card (Card == "board")
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
	ChannelID string          `json:"channelId"`
	Label     string          `json:"label"`
	Kind      jam.ChannelKind `json:"kind"`
	Project   string          `json:"project"`
	Phase     string          `json:"phase"`
	NeedsYou  bool            `json:"needsYou"`
	SendTo    string          `json:"sendTo"` // the composer's POST api/messages "to" ("" = read-only History)
	Messages  []MessageRow    `json:"messages"`
	LastSeq   int64           `json:"lastSeq"`
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
	// NewBoard offers New whiteboard (the viewer may start one here).
	NewBoard bool `json:"newBoard"`
	// Session is the actor id of the session whose home this channel is,
	// when the viewer may see it ("" = none): the app's session header and
	// pane read api/sessions/{session}.
	Session string `json:"session"`
	// Color is that session's color, the composer's accent ("" = none).
	Color string `json:"color,omitempty"`
}

// NewMessageOption is one recipient offered in the New Message picker.
type NewMessageOption struct {
	To      string `json:"to"` // POST api/messages' "to": user:<id>, session:<id>, or a channel id
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
	Channel  string     `json:"channel"`         // the session's home channel (/app/user/c/<channel>)
	Kind     string     `json:"kind"`            // standing | personal | ticket | session
	Color    string     `json:"color,omitempty"` // the session's color (#rrggbb; "" = none)
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
	Context    string      `json:"context"`
	ContextTip string      `json:"contextTip"`
	Uptime     string      `json:"uptime"`
	Mission    string      `json:"mission"` // the agent's mission (its end goal); "" = none
	Card       SessionCard `json:"card"`
	SSH        string      `json:"ssh"` // the copyable command; "" = not allowed
	CanRelease bool        `json:"canRelease"`
	CanRestart bool        `json:"canRestart"` // restart its studio, keeping the session (operators, every kind)
	CanEnd     bool        `json:"canEnd"`     // end it for good (operators; a personal session ends by Release)
	CanUpgrade bool        `json:"canUpgrade"`
	Ops        bool        `json:"ops"` // the session actions are wired
	// CanInterrupt, CanCompact, CanClear: shown whenever Ops is set; disabled when false.
	CanInterrupt bool   `json:"canInterrupt"`
	CanCompact   bool   `json:"canCompact"`
	CanClear     bool   `json:"canClear"`
	RestartLabel string `json:"restartLabel"` // always "Restart" (kept for older clients)
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
	// ThinkingStreams are the session's recorded event streams; EventsOff:
	// this jam records no session events.
	ThinkingStreams []ThinkingStream `json:"streams"`
	EventsOff       bool             `json:"eventsOff"`
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

// ThinkingEvent is one session event as the Thinking tab shows it: an `ev`
// of the feed, and a row of a page. Every string is agent
// text, shown as text, never as markup.
type ThinkingEvent struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Turn      uint32 `json:"turn"`
	At        int64  `json:"at"`   // unix ms; 0 = unknown
	Kind      string `json:"kind"` // thinking | text | prompt | tool_call | tool_result | result | progress | gap | other
	Tool      string `json:"tool"`
	ToolUseID string `json:"toolUseId"`
	// Text is the one line (a gap's summary); Detail the expanded body ("" for
	// kind other), DetailTruncated the bytes cut from it.
	Text            string  `json:"text"`
	Detail          string  `json:"detail"`
	DetailTruncated int     `json:"detailTruncated"`
	IsError         bool    `json:"isError"`
	TruncatedBytes  uint64  `json:"truncatedBytes"` // dropped by the recorder
	CostUSD         float64 `json:"costUsd"`        // result: the claude session's cumulative cost
	DurationMS      int64   `json:"durationMs"`     // result
	ClaudeSession   string  `json:"claudeSession"`  // result: the claude session CostUSD accumulates over
}

// ThinkingStream is one recorded event stream of a session (the Thinking
// tab's stream selector), newest first.
type ThinkingStream struct {
	ID      string    `json:"id"`
	FirstAt time.Time `json:"firstAt"`
	Events  int       `json:"events"`
}

// ThinkingPage is GET api/sessions/{id}/thinking: a stream's events below
// ?before=, in seq order; Before > 0: older ones exist below it.
type ThinkingPage struct {
	Events []ThinkingEvent `json:"events"`
	Before uint64          `json:"before"`
}

// ThinkingRaw is GET api/sessions/{id}/thinking/raw: one event's pretty JSON,
// capped; Truncated is the bytes cut.
type ThinkingRaw struct {
	Raw       string `json:"raw"`
	Truncated int    `json:"truncated"`
}

// FrameRequest is POST api/sessions/{id}/frame: the exposed preview to frame,
// by name.
type FrameRequest struct {
	Name string `json:"name"`
}

// FrameURL is a minted frame URL (one-time: never cached) and the view's
// plain URL, for Open in new tab ("" = none).
type FrameURL struct {
	URL  string `json:"url"`
	Open string `json:"open"`
}

// BoardCard is a board's card in its conversation (MessageRow.Board).
type BoardCard struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// BoardInfo is GET api/boards/{id}: the board the app's board route opens,
// its conversation, and whether the viewer may only look.
type BoardInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Channel  string `json:"channel"`
	ReadOnly bool   `json:"readOnly"`
}

// NewBoardRequest is POST api/boards: a whiteboard in a conversation the
// viewer may post to.
type NewBoardRequest struct {
	Channel string `json:"channel"`
	Title   string `json:"title"`
}

// NewBoardResponse is the new board and its conversation.
type NewBoardResponse struct {
	Board   string `json:"board"`
	Channel string `json:"channel"`
}

// SessionRolesResponse is GET api/session-roles: the roles the viewer may
// start a personal session of (the New session picker).
type SessionRolesResponse struct {
	Roles []SessionRole `json:"roles"`
}

// SessionRole is one role offered in the New session picker.
type SessionRole struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	// Full says why none can start now ("" = one can): the role's pool, or
	// the viewer's share of it, is taken.
	Full string `json:"full"`
}

// StartSessionRequest is POST api/sessions: start a personal session of
// Project's Role, owned by the viewer; Message, when set, is posted as the
// viewer's first message in its session channel.
type StartSessionRequest struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	Message string `json:"message,omitempty"`
}

// SessionStarted is POST api/sessions's answer: the new session and its
// session channel (its conversation).
type SessionStarted struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
}

// AppTokenRow is one of the viewer's app tokens (Connected apps).
type AppTokenRow struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
	Expires  time.Time `json:"expires"`
	Operator bool      `json:"operator"` // carries operator standing
}

// AppTokensResponse is GET /app/user/api/app-tokens: newest first.
type AppTokensResponse struct {
	Tokens []AppTokenRow `json:"tokens"`
}
