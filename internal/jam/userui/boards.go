package userui

import (
	"encoding/json"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Whiteboards in the user area (docs/usage/jam/intercom-ui.md#whiteboards):
// a board's card in its conversation (the app opens the editor).

// boardCard is m's board card, if it carries a board part.
func boardCard(d Deps, m intercom.Squawk) *BoardCard {
	if d.Boards == nil {
		return nil
	}
	for _, p := range m.Parts {
		if p.Type != intercom.BoardPartType {
			continue
		}
		var data struct {
			BoardID ident.ID `json:"board_id"`
		}
		if json.Unmarshal(p.Data, &data) != nil {
			return nil
		}
		title := ""
		if b, ok := d.Log.GetBoard(data.BoardID); ok && b.Channel == m.Channel {
			title = b.Title
		} else {
			return nil
		}
		return &BoardCard{ID: string(data.BoardID), Title: title}
	}
	return nil
}
