package userui

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestGroupRail(t *testing.T) {
	chs := []jam.ChannelView{
		{ID: "studio:u1", Kind: jam.ChannelStudio, Label: "cove-7f3a", NeedsYou: true, Unread: 2, LastSeq: 50},
		{ID: "studio:u2", Kind: jam.ChannelStudio, Label: "cove-9e10", Phase: string(jam.PhaseLive), LastSeq: 70},
		{ID: "named:eng", Kind: jam.ChannelNamed, Label: "#eng", LastSeq: 30},
		{ID: "studio:u3", Kind: jam.ChannelStudio, Label: "cove-1c22", NeedsYou: true, Unread: 4, LastSeq: 40},
	}

	groups := groupRail(chs, "studio:u1")

	// Three non-empty groups in attention order.
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	wantTitles := []string{"Needs you", "Sessions", "Channels"}
	for i, g := range groups {
		if g.Title != wantTitles[i] {
			t.Errorf("group[%d].Title = %q, want %q", i, g.Title, wantTitles[i])
		}
	}

	// Waiting group: both waiting studios, most-recent (higher LastSeq) first.
	w := groups[0]
	if len(w.Rows) != 2 || w.Rows[0].ID != "studio:u1" || w.Rows[1].ID != "studio:u3" {
		t.Fatalf("waiting rows = %+v, want [studio:u1, studio:u3]", w.Rows)
	}
	if !w.Rows[0].Selected {
		t.Error("selected channel (studio:u1) should be marked Selected")
	}
	if w.Rows[1].Selected {
		t.Error("unselected channel (studio:u3) must not be Selected")
	}
	if w.Rows[1].Unread != 4 {
		t.Errorf("unread carried = %d, want 4", w.Rows[1].Unread)
	}

	// Active group: the live studio.
	if a := groups[1]; len(a.Rows) != 1 || a.Rows[0].ID != "studio:u2" {
		t.Errorf("active rows = %+v, want [studio:u2]", a.Rows)
	}
	// Channels group: the named channel.
	if c := groups[2]; len(c.Rows) != 1 || c.Rows[0].ID != "named:eng" {
		t.Errorf("channels rows = %+v, want [named:eng]", c.Rows)
	}
}

func TestGroupRailDropsEmptyGroups(t *testing.T) {
	chs := []jam.ChannelView{
		{ID: "named:ops", Kind: jam.ChannelNamed, Label: "#ops", LastSeq: 10},
	}
	groups := groupRail(chs, "")
	if len(groups) != 1 || groups[0].Bucket != jam.BucketChannels {
		t.Fatalf("groups = %+v, want a single Channels group", groups)
	}
	if len(groups[0].Rows) != 1 || groups[0].Rows[0].Selected {
		t.Errorf("row = %+v, want one unselected row", groups[0].Rows)
	}
}

// The open conversation counts as read in the rail (and so the app badge) as
// soon as it's shown, not after the page's mark-read lands and the user leaves.
func TestGroupRailSelectedIsRead(t *testing.T) {
	chs := []jam.ChannelView{
		{ID: "named:eng", Kind: jam.ChannelNamed, Unread: 3, LastSeq: 30},
		{ID: "named:ops", Kind: jam.ChannelNamed, Unread: 2, LastSeq: 20},
	}
	groups := groupRail(chs, "named:eng")
	if n := unreadTotal(groups); n != 2 {
		t.Fatalf("unread total = %d, want 2 (the open conversation is read)", n)
	}
	if chs[0].Unread != 3 {
		t.Fatal("groupRail mutated its input")
	}
}

// A session row shows its mission under the name; a row without one has no
// mission line.
func TestRailMission(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	personal.Mission = "fixing the rail & header"
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	rail := railFor(t, Handler(e.Deps, nil), p)
	want := `<span class="mission" title="fixing the rail &amp; header">fixing the rail &amp; header</span>`
	if !strings.Contains(rail, want) {
		t.Fatalf("rail missing %s:\n%s", want, rail)
	}
	if n := strings.Count(rail, `class="mission"`); n != 1 {
		t.Fatalf("mission lines = %d, want 1 (the standing session has none):\n%s", n, rail)
	}
	if !strings.Contains(rail, `<span class="stack">`) {
		t.Errorf("rail rows lack the name/mission stack:\n%s", rail)
	}
}
