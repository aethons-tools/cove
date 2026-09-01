// Package switchboard is the in-sandbox Discord conductor: it polls Discord,
// hands a batched, channel/author-tagged inbox to a headless claude turn, posts
// the agent's replies, and obeys an agent-returned exit/wait/get action.
package switchboard

// Action is the agent's per-turn instruction to the conductor loop.
type Action string

const (
	ActionExit Action = "exit" // stop the loop
	ActionWait Action = "wait" // poll until a message arrives, then re-enter
	ActionGet  Action = "get"  // re-enter immediately (empty inbox is fine)
)

// Message is one inbound Discord message. ID is the Discord snowflake, used as
// the per-channel poll cursor.
type Message struct {
	ID      string
	Channel string
	Author  string
	Content string
}

// Outbound is one message the agent asked the conductor to post.
type Outbound struct {
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// TurnResult is the agent's per-turn output, parsed from its JSON result file.
type TurnResult struct {
	Messages []Outbound `json:"messages"`
	Action   Action     `json:"action"`
}
