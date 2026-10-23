package push

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// Payload limits (spec §8).
const (
	MaxPayload = 3072 // bytes of JSON, before encryption
	bodyRunes  = 140
	titleRunes = 120
)

// Payload is a push notification's JSON (spec §8).
type Payload struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Channel string `json:"channel"`
	ID      string `json:"id"`
	Unread  int    `json:"unread"`
}

// Build is the notification of squawk m for rc: title "<sender> ·
// <conversation>", body the markdown-stripped first 140 runes (an ask "❓ "
// + its question, a call-in "📣 " + the notice, an escalation "⚠️ " + the
// notice), or "New message" without preview; unread is rc's total. Its JSON
// is at most MaxPayload bytes.
func Build(r Reader, m intercom.Squawk, rc Recipient, unread int, preview bool) Payload {
	conv := ""
	if ch, ok := r.Channel(m.Channel); ok {
		conv = r.ChannelLabel(ch, rc.User)
	}
	p := Payload{
		Title:   cut(r.PartyLabel(m.From)+" · "+conv, titleRunes),
		Body:    "New message",
		Channel: string(m.Channel),
		ID:      m.ID,
		Unread:  unread,
	}
	if preview {
		p.Body = body(m)
	}
	// Labels and bodies are capped above; only an outsized id is left to
	// overflow, and the notification works without it.
	if data, _ := json.Marshal(p); len(data) > MaxPayload {
		p.ID = ""
	}
	return p
}

// body is m's notification text, prefixed by what it is.
func body(m intercom.Squawk) string {
	if ev, ok := intercom.AskOf(m); ok && ev.Event == intercom.AskEventAsk {
		return "❓ " + cut(StripMarkdown(ev.Question), bodyRunes)
	}
	text := m.Body
	if m.ContentType == intercom.ContentPlain {
		text = collapse(text)
	} else {
		text = StripMarkdown(text)
	}
	text = cut(text, bodyRunes)
	switch {
	case jam.IsCallInNotice(m.ID):
		return "📣 " + text
	case jam.IsEscalationNotice(m.ID):
		return "⚠️ " + text
	}
	return text
}

// cut is s's first n runes, the last an ellipsis when s is longer.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

var (
	mdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdCode     = regexp.MustCompile("`([^`]*)`")
	mdStrong   = regexp.MustCompile(`(\*\*|__|~~)(.+?)(\*\*|__|~~)`)
	mdStar     = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
	mdUnder    = regexp.MustCompile(`(^|[^\pL\pN_])_([^_\s][^_]*)_([^\pL\pN_]|$)`)
	mdLineHead = regexp.MustCompile(`^\s*(#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)`)
)

// StripMarkdown is markdown s as one line of plain text: fenced code blocks
// become "[code]"; headings, quotes and list markers, emphasis, inline code
// ticks, links and images (to their text) are dropped; whitespace collapses.
// Small and lossy by design — it only feeds a notification.
func StripMarkdown(s string) string {
	var lines []string
	fenced := false
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			if !fenced {
				lines = append(lines, "[code]")
			}
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		lines = append(lines, mdLineHead.ReplaceAllString(line, ""))
	}
	out := strings.Join(lines, "\n")
	out = mdImage.ReplaceAllString(out, "$1")
	out = mdLink.ReplaceAllString(out, "$1")
	out = mdCode.ReplaceAllString(out, "$1")
	out = mdStrong.ReplaceAllString(out, "$2")
	out = mdStar.ReplaceAllString(out, "$1")
	out = mdUnder.ReplaceAllString(out, "$1$2$3")
	return collapse(out)
}
