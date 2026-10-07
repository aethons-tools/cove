package adminui

import (
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/squawkrender"
)

// Logged is one squawk as the admin UI shows it, from the channel log or the
// frozen legacy log (Legacy: it has recipients instead of a channel).
type Logged struct {
	At          time.Time
	From        string // label
	FromID      string // participant id; for a legacy squawk its kind:ref
	AgentID     string // the author's agent id when it is one (links its page), else ""
	Channel     string // the channel's label and kind ("" for a legacy squawk)
	ChannelID   string
	Project     string
	To          []toCell // a legacy squawk's recipients
	Body        string
	ContentType string
	Legacy      bool
}

// SquawkReader is the admin UI's read-only view of the intercom: every squawk
// in a log, in append order. The UI never holds an append path.
type SquawkReader interface {
	Squawks(legacy bool) []Logged
}

// channelLog and legacyLog are the read sides the reader needs.
type (
	channelLog interface {
		ListSince(afterSeq int64, limit int) []intercom.Squawk
	}
	legacyLog interface {
		List(intercom.LegacyFilter) []intercom.LegacySquawk
	}
)

// logReader renders both logs for people: channel log entries with their
// author and channel resolved (ic), legacy ones as they were.
type logReader struct {
	store  jam.Store
	ic     *jam.Intercom
	log    channelLog // nil: none
	legacy legacyLog  // nil: none
}

// NewSquawkReader is the reader serve hands the UI: the channel log (and the
// intercom to name its participants and channels) and the legacy log. Either
// log may be nil.
func NewSquawkReader(store jam.Store, ic *jam.Intercom, log channelLog, legacy legacyLog) SquawkReader {
	return logReader{store: store, ic: ic, log: log, legacy: legacy}
}

func (r logReader) Squawks(legacy bool) []Logged {
	var out []Logged
	if legacy {
		if r.legacy == nil {
			return nil
		}
		for _, m := range r.legacy.List(intercom.LegacyFilter{}) {
			to := make([]toCell, 0, len(m.To))
			for _, t := range m.To {
				to = append(to, toCell{Target: t.String(), External: intercom.Classify(t) == intercom.External})
			}
			out = append(out, Logged{At: m.At, From: m.From.String(), FromID: m.From.String(), AgentID: legacyAgentID(m.From), Project: m.Project, To: to,
				Body: m.Body, ContentType: m.ContentType, Legacy: true})
		}
		return out
	}
	if r.log == nil || r.ic == nil {
		return nil
	}
	projects := map[ident.ID]string{}
	agents := map[string]bool{} // actor and instance ids: the store's agents
	for _, a := range r.store.ListActors() {
		agents[a.ID] = true
	}
	for _, m := range r.log.ListSince(0, 0) {
		ch := r.ic.ChannelParty(m.Channel)
		l := Logged{At: m.At, From: r.ic.PartyOf(m.From).Label, FromID: string(m.From), Channel: ch.Label + " · " + ch.Kind,
			ChannelID: string(m.Channel), Body: m.Body, ContentType: m.ContentType}
		if _, live := r.store.GetInstance(string(m.From)); live || agents[string(m.From)] {
			l.AgentID = string(m.From)
		}
		if c, ok := r.store.GetChannel(m.Channel); ok {
			if _, ok := projects[c.ProjectID]; !ok {
				if e, ok := r.store.Resolve(c.ProjectID); ok {
					projects[c.ProjectID] = e.Name
				}
			}
			l.Project = projects[c.ProjectID]
		}
		out = append(out, l)
	}
	return out
}

// legacyAgentID is the agent id of a legacy author actor:<id>, else "".
func legacyAgentID(t intercom.Target) string {
	if t.Kind == "actor" {
		return t.Ref
	}
	return ""
}

// dateLayout is the format of the since/until date inputs.
const dateLayout = "2006-01-02"

// toCell is one legacy recipient target rendered with its reach.
type toCell struct {
	Target   string // "kind:ref"
	External bool
}

// squawkRow is one message flattened for the template.
type squawkRow struct {
	At        string
	From      string
	FromID    string
	AgentID   string // the author's agent id when it is one
	Channel   string
	ChannelID string
	To        []toCell
	Project   string
	Body      template.HTML // rendered per the squawk's content type (squawkrender)
	Plain     bool          // text/plain: shown literally, badged
}

// squawkFilter selects log entries; a zero field matches everything. Until
// is exclusive.
type squawkFilter struct {
	Legacy       bool
	Project      string
	Participant  string
	Q            string
	Since, Until time.Time
}

// squawkTable is what the squawk-table template renders.
type squawkTable struct {
	Legacy   bool
	Filtered bool
	Rows     []squawkRow
}

// filterSquawks returns the log entries f selects, newest first, at most
// limit of them (0 = all).
func filterSquawks(msgs SquawkReader, f squawkFilter, limit int) squawkTable {
	t := squawkTable{Legacy: f.Legacy, Filtered: f.Project != "" || f.Participant != "" || f.Q != "" || !f.Since.IsZero() || !f.Until.IsZero()}
	needle := strings.ToLower(f.Q)
	var kept []Logged
	for _, m := range msgs.Squawks(f.Legacy) {
		switch {
		case f.Project != "" && m.Project != f.Project,
			!f.Since.IsZero() && m.At.Before(f.Since),
			!f.Until.IsZero() && !m.At.Before(f.Until),
			f.Participant != "" && !matchesParticipant(m, f.Participant),
			needle != "" && !strings.Contains(strings.ToLower(m.Body), needle):
			continue
		}
		kept = append(kept, m)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At.After(kept[j].At) })
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}
	t.Rows = make([]squawkRow, 0, len(kept))
	for _, m := range kept {
		t.Rows = append(t.Rows, toRow(m))
	}
	return t
}

// squawksData is the Intercom page payload. The filter fields are echoed back
// into the form so a filtered view round-trips; RawQuery drives the Refresh
// link; the embedded table's Filtered distinguishes the two empty states and
// Legacy selects the frozen legacy log's tab.
type squawksData struct {
	Title      string
	Configured bool
	squawkTable
	Project     string
	Participant string
	Q           string
	Since       string
	Until       string
	RawQuery    string
	BadDate     bool // a non-empty since/until failed to parse (surfaced, treated as unbounded)
}

// handleIntercom renders the read-only, newest-first message view of the
// channel log (or, ?log=legacy, the frozen legacy log). participant matches
// a squawk's author or channel id (a legacy squawk's kind:ref author or
// recipient). A nil reader means no log is configured.
func handleIntercom(w http.ResponseWriter, r *http.Request, msgs SquawkReader) {
	q := r.URL.Query()
	data := squawksData{Title: "Intercom", Configured: msgs != nil}
	data.Legacy = q.Get("log") == "legacy"
	if msgs == nil {
		render(w, "intercom", data)
		return
	}
	data.Project = strings.TrimSpace(q.Get("project"))
	data.Participant = strings.TrimSpace(q.Get("participant"))
	data.Q = q.Get("q")
	data.Since = strings.TrimSpace(q.Get("since"))
	data.Until = strings.TrimSpace(q.Get("until"))
	data.RawQuery = r.URL.RawQuery

	// Date bounds are UTC day boundaries; an unparseable one is surfaced and
	// left unbounded.
	f := squawkFilter{Legacy: data.Legacy, Project: data.Project, Participant: data.Participant, Q: data.Q}
	if data.Since != "" {
		if t, err := time.Parse(dateLayout, data.Since); err == nil {
			f.Since = t
		} else {
			data.BadDate = true
		}
	}
	if data.Until != "" {
		if t, err := time.Parse(dateLayout, data.Until); err == nil {
			f.Until = t.AddDate(0, 0, 1) // inclusive day → exclusive next midnight
		} else {
			data.BadDate = true
		}
	}
	data.squawkTable = filterSquawks(msgs, f, 0)
	// A bad date still counts as filtering (the old view did): the "no
	// match" empty state.
	data.Filtered = data.Filtered || data.Since != "" || data.Until != ""
	render(w, "intercom", data)
}

// matchesParticipant reports whether p is m's author or channel (a legacy
// squawk: its author or one of its recipients, as kind:ref).
func matchesParticipant(m Logged, p string) bool {
	if m.FromID == p || m.ChannelID == p {
		return true
	}
	for _, t := range m.To {
		if t.Target == p {
			return true
		}
	}
	return false
}

func toRow(m Logged) squawkRow {
	return squawkRow{
		At:        m.At.Format("2006-01-02 15:04:05"),
		From:      m.From,
		FromID:    m.FromID,
		AgentID:   m.AgentID,
		Channel:   m.Channel,
		ChannelID: m.ChannelID,
		To:        m.To,
		Project:   m.Project,
		Body:      squawkrender.Body(m.ContentType, m.Body),
		Plain:     squawkrender.IsPlain(m.ContentType),
	}
}
