package adminui

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// studioSquawkLimit caps how many squawks the studio page shows; the Intercom
// page (linked, pre-filtered) has the rest.
const studioSquawkLimit = 50

// studioDetail is the studio page payload. Inst is the registry record while
// the studio runs; a torn-down studio is gone from the registry, so Running is
// false and only its audit trail (session streams, squawks) is shown.
type studioDetail struct {
	Title    string
	ID       string
	Running  bool
	Inst     jam.Instance
	CanEdit  bool
	Kind     string // ephemeral | personal | standing
	EgressID string // short egress fingerprint

	SessionsEnabled bool
	Streams         []sessionevents.StreamInfo

	SquawksConfigured bool
	Squawks           []squawkRow
	SquawksMore       bool
	IntercomURL       string

	NotFound bool
}

// studioSquawks returns participant's squawks newest first, capped, and
// whether more exist.
func studioSquawks(msgs SquawkReader, participant string) ([]squawkRow, bool) {
	var mine []intercom.Squawk
	for _, m := range msgs.List(intercom.Filter{}) {
		if matchesParticipant(m, participant) {
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

// buildStudioDetail gathers a studio's page; false when nothing at all is
// known about id (not running, no session streams, no squawks).
func buildStudioDetail(store jam.Store, msgs SquawkReader, sess sessionevents.Store, id string, canEdit bool) (studioDetail, bool) {
	participant := "actor:" + id
	d := studioDetail{
		Title: "Studios", ID: id, CanEdit: canEdit,
		SessionsEnabled: sess != nil, SquawksConfigured: msgs != nil,
		IntercomURL: "/ui/intercom?participant=" + url.QueryEscape(participant),
	}
	d.Inst, d.Running = store.GetInstance(id)
	if d.Running {
		d.Kind = d.Inst.SessionKind
		if d.Kind == "" {
			d.Kind = "ephemeral"
		}
		d.EgressID = shortDigest(d.Inst.Egress)
	}
	if sess != nil {
		d.Streams, _ = sess.Streams(id)
	}
	if msgs != nil {
		d.Squawks, d.SquawksMore = studioSquawks(msgs, participant)
	}
	return d, d.Running || len(d.Streams) > 0 || len(d.Squawks) > 0
}

func registerStudio(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, canEdit bool) {
	mux.HandleFunc("GET /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildStudioDetail(store, msgs, sess, r.PathValue("id"), canEdit)
		if !ok {
			d.NotFound = true
			renderStatus(w, http.StatusNotFound, "studio", d)
			return
		}
		render(w, "studio", d)
	})
}
