package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/intercom"
)

// participantSendStore is the narrow slice of Store the participant send path
// reads: the per-project roster (to resolve the sender's roster identity and any
// named-channel target) and the live instances (to resolve a studio/session
// recipient to its session actor). jam.Store satisfies it.
type participantSendStore interface {
	GetRoster(project string) (Roster, bool)
	ListInstances() []Instance
}

// ParticipantSendHandler is the participant intercom send endpoint (POST under
// /me): the human analog of the agent `send` tool (COV-196 slice 1). It resolves
// the sender from the gate-injected Participant (COV-199) — never the request
// body — resolves the target recipient to a Log target via the channel model
// (COV-198), and appends an external-origin message to the SAME squawk Log the
// agent `send` tool and the relay ingress write. Because the append is
// external-origin and addressed to the studio's session actor, wake-on resumes a
// waiting/idled studio exactly as a relayed reply does (see internal/wakeon).
//
// Open addressing to start: any active recipient is allowed (no comms
// access-graph check this slice). The UI is an in-process Log writer, not a
// msgport egress engine — there is no EgressMark; a separate egress engine
// renders and delivers any external target.
type ParticipantSendHandler struct {
	store participantSendStore
	lg    appender
	log   *slog.Logger
}

// NewParticipantSendHandler constructs a ParticipantSendHandler. lg may be nil:
// a nil lg makes a send fail 503 (messaging unconfigured), mirroring /squawks.
func NewParticipantSendHandler(store participantSendStore, lg appender, log *slog.Logger) *ParticipantSendHandler {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &ParticipantSendHandler{store: store, lg: lg, log: log}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (h *ParticipantSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Identity comes solely from the gate-injected participant, never the body.
	// Fail closed: no participant (a gate misconfiguration) denies.
	p, ok := ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSquawkBodyBytes)
	var req struct {
		To   string `json:"to"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	if req.To == "" {
		http.Error(w, "empty to", http.StatusBadRequest)
		return
	}

	// Resolve the recipient (open addressing — no access-graph check). A
	// participant is a global person; the recipient may live in any of their
	// projects. Try each project in ListProjects order (the order recorded in
	// Participant.Projects) and take the first that resolves — deterministic when
	// a bare ref is ambiguous across projects. The outgoing `from` ref is the
	// sender's roster name in the TARGET's project (names may differ per project).
	var (
		to      intercom.Target
		from    intercom.Target
		project string
		found   bool
	)
	for _, proj := range p.Projects {
		roster, ok := h.store.GetRoster(proj)
		if !ok {
			continue
		}
		sender, ok := roster.HumanByIdentity(p.Issuer, p.Subject)
		if !ok || sender.Name == "" {
			continue // not bound (or unnamed) in this project — cannot attribute
		}
		self := intercom.Target{Kind: "human", Ref: sender.Name}
		t, ok := ResolveSendTarget(req.To, self, roster, h.instancesFor(proj))
		if !ok {
			continue
		}
		to, from, project, found = t, self, proj, true
		break
	}
	if !found {
		http.Error(w, "recipient not found", http.StatusNotFound)
		return
	}

	// The Log is the authoritative delivery path (the same append the agent send
	// tool uses). A send requires a configured Log; an append failure fails it.
	if h.lg == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	if _, err := h.lg.Append(intercom.Squawk{
		From:    from,
		To:      []intercom.Target{to},
		Body:    req.Body,
		Project: project,
	}); err != nil {
		h.log.Error("participant send: append failed", "from", from.String(), "to", to.String(), "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("participant send", "from", from.String(), "to", to.String(), "project", project, "bytes", len(req.Body))
	w.WriteHeader(http.StatusNoContent)
}

// instancesFor returns the live instances belonging to project.
func (h *ParticipantSendHandler) instancesFor(project string) []Instance {
	var out []Instance
	for _, i := range h.store.ListInstances() {
		if i.Project == project {
			out = append(out, i)
		}
	}
	return out
}
