package adminui

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// studioSquawkLimit caps how many squawks the agent page shows; the Intercom
// page (linked, pre-filtered) has the rest.
const studioSquawkLimit = 50

// agentDetail is the agent page payload: the identity (an enrolled actor and
// its grants), its studio while one runs, and its session's audit trail. Inst
// is the registry record while the studio runs; a torn-down studio is gone
// from the registry, so Running is false and only its audit trail (session
// streams, squawks) is shown.
type agentDetail struct {
	Title    string
	ID       string
	Running  bool
	Inst     jam.Instance
	CanEdit  bool
	Kind     string // standing | personal | ticket | manual | enrolled (agentKind)
	EgressID string // short egress fingerprint

	Actor    jam.ActorSummary // the enrolled identity, when HasActor
	HasActor bool

	SessionsEnabled bool
	Streams         []sessionevents.StreamInfo

	SquawksConfigured bool
	Squawks           []squawkRow
	SquawksMore       bool
	IntercomURL       string

	NotFound bool
}

// studioSquawks returns the session's squawks newest first, capped, and
// whether more exist: in the channel log, every squawk in a channel it is in
// or has posted to; in the legacy log, those to or from actor:<id>.
func studioSquawks(store jam.Store, msgs SquawkReader, id string) ([]squawkRow, bool) {
	all := msgs.Squawks(false)
	channels := map[string]bool{}
	for _, ch := range store.ChannelsOf(ident.ID(id)) {
		channels[string(ch)] = true
	}
	for _, m := range all {
		if m.FromID == id {
			channels[m.ChannelID] = true
		}
	}
	var mine []Logged
	for _, m := range all {
		if channels[m.ChannelID] {
			mine = append(mine, m)
		}
	}
	for _, m := range msgs.Squawks(true) {
		if matchesParticipant(m, "actor:"+id) {
			mine = append(mine, m)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].At.After(mine[j].At) })
	more := len(mine) > studioSquawkLimit
	if more {
		mine = mine[:studioSquawkLimit]
	}
	rows := make([]squawkRow, 0, len(mine))
	for _, m := range mine {
		rows = append(rows, toRow(m))
	}
	return rows, more
}

// buildAgentDetail gathers an agent's page; false when nothing at all is
// known about id (no actor, not running, no session streams, no squawks).
func buildAgentDetail(store jam.Store, msgs SquawkReader, sess sessionevents.Store, id string, canEdit bool) (agentDetail, bool) {
	participant := id // the session: its squawks in the channel log, and as actor:<id> in the legacy log
	d := agentDetail{
		Title: id, ID: id, CanEdit: canEdit,
		SessionsEnabled: sess != nil, SquawksConfigured: msgs != nil,
		IntercomURL: "/ui/intercom?participant=" + url.QueryEscape(participant),
	}
	d.Inst, d.Running = store.GetInstance(id)
	d.Inst.Project = jam.ProjectName(store, d.Inst.Project) // the page names it
	if d.Running {
		d.EgressID = shortDigest(d.Inst.Egress)
	}
	for _, a := range jam.RosterSummaries(store) {
		if a.ID == id {
			d.Actor, d.HasActor = a, true
		}
	}
	if d.Running || d.HasActor { // a gone studio with no identity is neither
		d.Kind = agentKind(d.Inst, d.Running)
	}
	if sess != nil {
		d.Streams, _ = sess.Streams(id)
	}
	if msgs != nil {
		d.Squawks, d.SquawksMore = studioSquawks(store, msgs, id)
	}
	return d, d.HasActor || d.Running || len(d.Streams) > 0 || len(d.Squawks) > 0
}

func registerAgent(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, canEdit bool) {
	mux.HandleFunc("GET /ui/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildAgentDetail(store, msgs, sess, r.PathValue("id"), canEdit)
		if !ok {
			d.NotFound = true
			renderStatus(w, http.StatusNotFound, "agent", d)
			return
		}
		render(w, "agent", d)
	})
}
