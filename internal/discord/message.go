// Package discord is Jam's Discord REST client: it polls, seeds and posts to a
// fixed set of channels for the Discord relay (cmd/jam relay_discord.go).
package discord

// Message is one inbound Discord message. ID is the Discord snowflake, used as
// the per-channel poll cursor.
type Message struct {
	ID      string
	Channel string
	Author  string // display name (global_name, else username) — user-editable, never an identity
	Content string
	// AuthorID is Discord's immutable author user id (snowflake); "" if absent.
	AuthorID string
	// AuthorBot is true when the author is a bot (Discord's author.bot).
	AuthorBot bool
	// ReferencedID is the id of the message this replies to; empty if not a reply.
	ReferencedID string
}
