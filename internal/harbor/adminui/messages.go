package adminui

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

// MessageReader is the read-only slice of the msglog.Log the Messages page
// needs. *msglog.Log satisfies it; the UI never holds an append path.
type MessageReader interface {
	List(msglog.Filter) []msglog.Message
}

// dateLayout is the format of the since/until date inputs.
const dateLayout = "2006-01-02"

// toCell is one recipient target rendered with its reach classification.
type toCell struct {
	Target   string // "kind:ref"
	External bool
}

// msgRow is one message flattened for the template.
type msgRow struct {
	At      string
	From    string
	To      []toCell
	Project string
	Body    string
}

// messagesData is the Messages page payload. The filter fields are echoed back
// into the form so a filtered view round-trips; RawQuery drives the Refresh
// link; Filtered distinguishes the two empty states.
type messagesData struct {
	Title       string
	Configured  bool
	Filtered    bool
	Rows        []msgRow
	Project     string
	Participant string
	Q           string
	Since       string
	Until       string
	RawQuery    string
}

// handleMessages renders the read-only, newest-first message view. A nil reader
// means the log is not configured.
func handleMessages(w http.ResponseWriter, r *http.Request, msgs MessageReader) {
	data := messagesData{Title: "Messages", Configured: msgs != nil}
	if msgs == nil {
		render(w, "messages", data)
		return
	}

	q := r.URL.Query()
	data.Project = strings.TrimSpace(q.Get("project"))
	data.Participant = strings.TrimSpace(q.Get("participant"))
	data.Q = q.Get("q")
	data.Since = strings.TrimSpace(q.Get("since"))
	data.Until = strings.TrimSpace(q.Get("until"))
	data.RawQuery = r.URL.RawQuery
	data.Filtered = data.Project != "" || data.Participant != "" || data.Q != "" || data.Since != "" || data.Until != ""

	f := msglog.Filter{Project: data.Project}
	if t, err := time.Parse(dateLayout, data.Since); err == nil {
		f.Since = t
	}
	if t, err := time.Parse(dateLayout, data.Until); err == nil {
		f.Until = t.AddDate(0, 0, 1) // until is an inclusive day → exclusive next-midnight bound
	}

	msgList := msgs.List(f)

	needle := strings.ToLower(data.Q)
	filtered := msgList[:0:0]
	for _, m := range msgList {
		if data.Participant != "" && !matchesParticipant(m, data.Participant) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(m.Body), needle) {
			continue
		}
		filtered = append(filtered, m)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].At.After(filtered[j].At) })

	data.Rows = make([]msgRow, 0, len(filtered))
	for _, m := range filtered {
		data.Rows = append(data.Rows, toRow(m))
	}
	render(w, "messages", data)
}

// matchesParticipant reports whether p ("kind:ref") is the sender or one of the
// recipients of m.
func matchesParticipant(m msglog.Message, p string) bool {
	if m.From.String() == p {
		return true
	}
	for _, t := range m.To {
		if t.String() == p {
			return true
		}
	}
	return false
}

func toRow(m msglog.Message) msgRow {
	to := make([]toCell, 0, len(m.To))
	for _, t := range m.To {
		to = append(to, toCell{Target: t.String(), External: msglog.Classify(t) == msglog.External})
	}
	return msgRow{
		At:      m.At.Format("2006-01-02 15:04:05"),
		From:    m.From.String(),
		To:      to,
		Project: m.Project,
		Body:    m.Body,
	}
}
