package intercom

import (
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
)

// MentionsPartType is the squawk part listing the participants a message
// mentions (conversations.adoc#wake): {"ids": […]}. Jam resolves it at post
// time, from send's mentions and the body's @<label>s, so a later rename
// can't change who a message named.
const MentionsPartType = "application/vnd.jam.mentions+json"

type mentionsData struct {
	IDs []ident.ID `json:"ids"`
}

// MentionsPart is the mentions part naming ids.
func MentionsPart(ids []ident.ID) Part {
	data, _ := json.Marshal(mentionsData{IDs: ids}) // a list of strings always encodes
	return Part{Type: MentionsPartType, Data: data}
}

// Mentions are the participants m mentions (nil: none).
func Mentions(m Squawk) []ident.ID {
	for _, p := range m.Parts {
		if p.Type == MentionsPartType && p.Ref == "" {
			var d mentionsData
			if json.Unmarshal(p.Data, &d) == nil {
				return d.IDs
			}
		}
	}
	return nil
}

func validateMentions(p Part) error {
	var d mentionsData
	if err := json.Unmarshal(p.Data, &d); err != nil {
		return fmt.Errorf("%w: mentions part: %v", ErrInvalid, err)
	}
	return nil
}
