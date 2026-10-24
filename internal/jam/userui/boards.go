package userui

import (
	"encoding/json"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// Whiteboards in the user area (docs/usage/jam/intercom-ui.md#whiteboards):
// a board's card in its conversation, New whiteboard in the header, and the
// editor (the Excalidraw bundle) that ?board= opens in place of the stream.

// whiteboardBuilt reports whether the editor bundle is in this binary (a
// var, so tests can toggle it).
var whiteboardBuilt = uiassets.WhiteboardBuilt

// BoardCard is a board's card in its conversation.
type BoardCard struct {
	ID, Title string
	Built     bool
}

// BoardEditor is the open board.
type BoardEditor struct {
	ID, Title, Script, Styles, AssetBase string
	ReadOnly                             bool
}

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
		return &BoardCard{ID: string(data.BoardID), Title: title, Built: whiteboardBuilt()}
	}
	return nil
}

// boardEditor is board id opened by p, if p may read it and the editor is built.
func boardEditor(p jam.Participant, d Deps, chID, id string) *BoardEditor {
	if d.Boards == nil || !whiteboardBuilt() {
		return nil
	}
	b, _, ok := d.Boards.CanRead(p.UserID, ident.ID(id))
	if !ok || string(b.Channel) != chID {
		return nil
	}
	_, writable := d.Boards.CanWrite(p.UserID, ident.ID(id))
	const prefix = "/app/static/"
	return &BoardEditor{ID: id, Title: b.Title, Script: uiassets.WhiteboardScript(prefix), Styles: uiassets.WhiteboardStyles(prefix),
		AssetBase: uiassets.WhiteboardBase(prefix), ReadOnly: !writable}
}
