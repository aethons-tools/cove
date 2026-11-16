package userui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// The open conversation counts as read in the rail (and so the app badge) as
// soon as it's shown, not after the page's mark-read lands and the user leaves.
func TestChannelRowSelectedIsRead(t *testing.T) {
	chs := []jam.ChannelView{
		{ID: "named:eng", Kind: jam.ChannelNamed, Unread: 3, LastSeq: 30},
		{ID: "named:ops", Kind: jam.ChannelNamed, Unread: 2, LastSeq: 20},
	}
	var rows []ChannelRow
	for _, ch := range chs {
		rows = append(rows, channelRow(ch, "named:eng"))
	}
	if !rows[0].Selected || rows[0].Unread != 0 || rows[1].Selected || rows[1].Unread != 2 {
		t.Fatalf("rows = %+v, want named:eng selected and read, named:ops unselected with 2 unread", rows)
	}
	if n := unreadTotal(rows); n != 2 {
		t.Fatalf("unread total = %d, want 2 (the open conversation is read)", n)
	}
	if chs[0].Unread != 3 {
		t.Fatal("channelRow mutated its input")
	}
}

// A session's rail row carries its mission; a row without one has none.
func TestRailMission(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	personal.Mission = "fixing the rail & header"
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	var missions []string
	for _, row := range railJSON(t, Handler(e.Deps, nil), p).Me {
		if row.Mission != "" {
			missions = append(missions, row.Mission)
		}
	}
	if len(missions) != 1 || missions[0] != personal.Mission {
		t.Fatalf("missions = %q, want just the personal session's (the standing session has none)", missions)
	}
}
