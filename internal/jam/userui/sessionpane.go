package userui

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/preview"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// The session pane (docs/usage/jam/intercom-ui.md#the-session-view): the
// tabbed pane beside a session's messages. Each tab is an htmx fragment.

// SessionPane is one tab of a session's pane.
type SessionPane struct {
	ID, Tab  string
	Previews []PreviewLink
	// PreviewsOff: this jam serves no previews (or the viewer may not open them).
	PreviewsOff bool
	Boards      []PaneBoard
	Files       []FileChip
	Asks        []PaneAsk
	Links       []PaneLink
	Status      []StatusRow
	// Streams are the session's recorded event streams (Thinking tab);
	// EventsOff: this jam records no session events.
	Streams   []sessionevents.StreamInfo
	EventsOff bool
	// Studio: the Studio tab's IDE may be framed here (ssh and framing on).
	Studio bool
}

// PreviewLink is one exposed port, opened in a new tab; Frame: View here
// (framed in the pane) is offered too.
type PreviewLink struct {
	Name, URL string
	Port      int
	Frame     bool
}

// PaneBoard is a whiteboard started in the session's home channel.
type PaneBoard struct{ ID, Title, Channel string }

// PaneAsk is an ask posted in the home channel, with its state.
type PaneAsk struct{ ID, Question, State string }

// PaneLink is a URL from the home channel's messages.
type PaneLink struct{ URL, Label string }

// StatusRow is one line of the Status tab.
type StatusRow struct{ Name, Value string }

// paneTabs are the tabs a session's pane has.
var paneTabs = []string{"studio", "thinking", "previews", "artifacts", "status"}

// maxPaneLinks bounds the Links list (the newest win).
const maxPaneLinks = 50

func (h *handler) sessionPaneRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	inst, home, ok := h.sessionFor(p, r.PathValue("id"))
	tab := r.URL.Query().Get("tab")
	if !ok || !slices.Contains(paneTabs, tab) {
		http.NotFound(w, r)
		return
	}
	pane := SessionPane{ID: inst.ActorID, Tab: tab}
	switch tab {
	case "studio":
		_, ok := collab.Decide(h.d.Store, time.Now(), collabPrincipal(r, p), inst, collab.SSH)
		pane.Studio = ok && h.d.Frames != nil
	case "thinking":
		if h.d.Events == nil {
			pane.EventsOff = true
		} else {
			pane.Streams, _ = h.d.Events.Streams(inst.ActorID)
		}
	case "previews":
		h.panePreviews(r, p, inst, &pane)
	case "artifacts":
		h.paneArtifacts(home, &pane)
	case "status":
		h.paneStatus(inst, &pane)
	}
	h.render(w, "sessionpane", inboxPage{Pane: &pane})
}

func (h *handler) panePreviews(r *http.Request, p jam.Participant, inst jam.Instance, pane *SessionPane) {
	if h.d.PreviewURL == nil {
		pane.PreviewsOff = true
		return
	}
	if _, ok := collab.Decide(h.d.Store, time.Now(), collabPrincipal(r, p), inst, collab.Preview); !ok {
		pane.PreviewsOff = true
		return
	}
	for _, e := range inst.Exposures {
		if e.Name == preview.IDEName {
			continue // the Studio tab's
		}
		u, err := h.d.PreviewURL(inst.ActorID, e.Name)
		if err != nil {
			continue
		}
		pane.Previews = append(pane.Previews, PreviewLink{Name: e.Name, URL: u, Port: e.Port, Frame: h.d.Frames != nil})
	}
}

// paneArtifacts collects what the home channel holds: its whiteboards,
// attachments, asks and links, newest first.
func (h *handler) paneArtifacts(home string, pane *SessionPane) {
	if h.d.Log == nil {
		return
	}
	ch := ident.ID(home)
	if h.d.Boards != nil {
		for _, b := range h.d.Log.ListBoards(ch) {
			pane.Boards = append(pane.Boards, PaneBoard{ID: string(b.ID), Title: b.Title, Channel: home})
		}
		slices.Reverse(pane.Boards)
	}
	seen := map[string]bool{}
	msgs := h.d.Log.ChannelSince(ch, 0, 0)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		chips, _ := fileChips(m)
		pane.Files = append(pane.Files, chips...)
		for _, u := range linksIn(m.Body) {
			if !seen[u] && len(pane.Links) < maxPaneLinks {
				seen[u] = true
				pane.Links = append(pane.Links, PaneLink{URL: u, Label: linkLabel(u)})
			}
		}
	}
	asks := h.d.Log.ListAsks(intercom.AskFilter{Channel: ch})
	for i := len(asks) - 1; i >= 0; i-- {
		pane.Asks = append(pane.Asks, PaneAsk{ID: asks[i].ID, Question: asks[i].Question, State: string(asks[i].State)})
	}
}

var urlRE = regexp.MustCompile("https?://[^\\s<>\"'`]+")

// linksIn is the http(s) URLs in body, trailing punctuation trimmed.
func linksIn(body string) []string {
	var out []string
	for _, u := range urlRE.FindAllString(body, -1) {
		u = strings.TrimRight(u, ".,;:!?*_")
		// A closing bracket belongs to the URL only when it opened one there.
		for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}} {
			for strings.HasSuffix(u, pair[1]) && strings.Count(u, pair[1]) > strings.Count(u, pair[0]) {
				u = strings.TrimSuffix(u, pair[1])
			}
		}
		if len(u) > len("https://") {
			out = append(out, u)
		}
	}
	return out
}

var ghRE = regexp.MustCompile(`^https://github\.com/[^/]+/([^/]+)/(pull|issues)/(\d+)`)

// linkLabel names a GitHub PR or issue ("cove PR #12"); else the URL itself.
func linkLabel(u string) string {
	if m := ghRE.FindStringSubmatch(u); m != nil {
		kind := "PR"
		if m[2] == "issues" {
			kind = "issue"
		}
		return fmt.Sprintf("%s %s #%s", m[1], kind, m[3])
	}
	return u
}

// paneStatus is the session's aggregates: its state and runtime, never the
// content of its events (docs/usage/jam/session-events.md).
func (h *handler) paneStatus(inst jam.Instance, pane *SessionPane) {
	now := time.Now()
	add := func(name, value string) {
		if value != "" {
			pane.Status = append(pane.Status, StatusRow{name, value})
		}
	}
	if rows := sessionRows([]string{inst.ActorID}, []jam.Instance{inst}, h.presence); len(rows) == 1 {
		add("State", strings.TrimSpace(rows[0].Text+" "+rows[0].Tool))
	}
	add("In this state", since(now, stateSince(inst)))
	add("Up", since(now, inst.RaisedAt))
	if role, ok := h.d.Store.GetRole(inst.Project, inst.Role); ok {
		add("Model spec", role.ModelSpecName())
	}
	if inst.Kit.ID != "" {
		add("Kit", fmt.Sprintf("%s@v%d", inst.Kit.ID, inst.Kit.Version))
	}
	add("Image", inst.ImageTag)
	for _, a := range inst.Alarms {
		next := "fired"
		if !a.NextAt.IsZero() {
			next = "next " + a.NextAt.Local().Format("Jan 2 15:04")
		}
		add("Alarm "+a.Name, a.Schedule+" · "+next)
	}
	if h.d.Totals == nil {
		return
	}
	t, err := h.d.Totals.Of(inst.ActorID)
	if err != nil || t.Turns == 0 {
		return
	}
	add("Turns", fmt.Sprintf("%d (%s)", t.Turns, plural(int(t.Episodes), "episode")))
	add("Tool calls", fmt.Sprintf("%d (%s)", t.ToolCalls, plural(t.Errors, "error")))
	add("Tokens", fmt.Sprintf("%d in / %d out", t.InputTokens, t.OutputTokens))
	add("Cost", usd(t.CostUSD))
	add("This turn", usd(t.TurnCostUSD))
	if !t.LastAt.IsZero() {
		ago := since(now, t.LastAt)
		if ago != "just now" {
			ago += " ago"
		}
		add("Last event", ago)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
