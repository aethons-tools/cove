package jam

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Mentions (conversations.adoc#wake): send's mentions field and the body's
// @<label>s name members of the conversation. A post records them as a
// mentions part, resolved when it is posted; wake-on reads it to wake a
// room's named sessions, and push to notify a room's or work log's named
// people.

// ErrMentionUnresolved refuses a send mention that names no session member
// of the conversation.
var ErrMentionUnresolved = errors.New("intercom: a mention names no session member of the conversation")

// atLabel is an @<label> that doesn't follow a word character (an email
// address isn't a mention).
var atLabel = regexp.MustCompile(`(?:^|[^\w@])@([\w][\w.-]*)`)

// ResolveMentions resolves send's mentions — session:<label|id> targets — to
// the session members of ch they name.
func (ic *Intercom) ResolveMentions(ch Channel, targets []string) ([]ident.ID, error) {
	var out []ident.ID
	for _, t := range targets {
		ref, ok := strings.CutPrefix(t, "session:")
		if !ok || ref == "" {
			return nil, ErrMentionUnresolved
		}
		id, ok := ic.memberNamed(ch, ref, true)
		if !ok {
			return nil, ErrMentionUnresolved
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// withMentions is m's parts with one mentions part: the ids its parts
// already mention, and the members its body's @<label>s name, but its
// author.
func (ic *Intercom) withMentions(ch Channel, m intercom.Squawk) []intercom.Part {
	named := intercom.Mentions(m)
	for _, sub := range atLabel.FindAllStringSubmatch(m.Body, -1) {
		label := strings.TrimRight(sub[1], ".")
		if id, ok := ic.memberNamed(ch, label, false); ok && id != m.From {
			named = append(named, id)
		}
	}
	parts := slices.DeleteFunc(slices.Clone(m.Parts), func(p intercom.Part) bool { return p.Type == intercom.MentionsPartType })
	if len(named) == 0 {
		return parts
	}
	slices.Sort(named)
	return append(parts, intercom.MentionsPart(slices.Compact(named)))
}

// memberNamed is the current member of ch that ref names: a session by id
// or label, or, unless sessionsOnly, a person by name.
func (ic *Intercom) memberNamed(ch Channel, ref string, sessionsOnly bool) (ident.ID, bool) {
	for _, mem := range ic.store.ChannelMembers(ch.ID) {
		p := mem.ParticipantID
		if !ic.reachable(ch, p) {
			continue
		}
		switch {
		case isSessionID(p):
			if string(p) == ref {
				return p, true
			}
			if inst, ok := ic.store.GetInstance(string(p)); ok && sessionLabel(inst) == ref {
				return p, true
			}
		case !sessionsOnly && p.Kind() == ident.User:
			if u, ok := ic.store.GetUser(p); ok && u.Name == ref {
				return p, true
			}
		}
	}
	return "", false
}
