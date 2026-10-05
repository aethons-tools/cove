package jam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Ticket report states (the `report` tool). See
// docs/usage/jam/turn-end.md#reporting-a-ticket.
const (
	ReportInProgress = "in-progress"
	ReportInReview   = "in-review"
	ReportNeedsInput = "needs-input"
	ReportBlocked    = "blocked"
	ReportDone       = "done"
)

const (
	maxReportSummary   = 4000
	maxReportPR        = 500
	maxReportBodyBytes = 8192
)

// ErrNoTracker is a TicketReporter's answer when Jam has no tracker to move
// tickets with (no Requisitioner configured): the handler maps it to 503.
var ErrNoTracker = errors.New("no tracker configured")

// TicketReport is a session's latest report of its ticket's state.
type TicketReport struct {
	State   string    `json:"state"`
	Summary string    `json:"summary"`
	PR      string    `json:"pr,omitempty"`
	At      time.Time `json:"at"`
}

// Terminal reports whether the state ends the ticket's work (done, blocked).
func (r TicketReport) Terminal() bool { return r.State == ReportDone || r.State == ReportBlocked }

// ValidateReport trims r and checks it: a known state, a summary (≤ 4000
// bytes), an optional http(s) PR link (≤ 500 bytes) that in-review requires.
func ValidateReport(r *TicketReport) error {
	r.Summary, r.PR = strings.TrimSpace(r.Summary), strings.TrimSpace(r.PR)
	switch r.State {
	case ReportInProgress, ReportInReview, ReportNeedsInput, ReportBlocked, ReportDone:
	default:
		return fmt.Errorf("state must be one of %s, %s, %s, %s, %s", ReportInProgress, ReportInReview, ReportNeedsInput, ReportBlocked, ReportDone)
	}
	if r.Summary == "" || len(r.Summary) > maxReportSummary {
		return fmt.Errorf("summary is required, at most %d bytes", maxReportSummary)
	}
	if r.PR != "" && (len(r.PR) > maxReportPR || !(strings.HasPrefix(r.PR, "https://") || strings.HasPrefix(r.PR, "http://"))) {
		return fmt.Errorf("pr must be an http(s) URL of at most %d bytes", maxReportPR)
	}
	if r.State == ReportInReview && r.PR == "" {
		return fmt.Errorf("in-review needs the pr link")
	}
	return nil
}

// TicketReporter moves a session's ticket to the reported state and comments
// on it. Implemented in cmd/at-jam over the Requisitioner's tracker.
type TicketReporter interface {
	Report(ctx context.Context, inst Instance, r TicketReport) error
}

// reportSetter stamps the accepted report on the instance (*Supervisor).
type reportSetter interface {
	SetReport(actorID string, r TicketReport) error
}

// ReportHandler is Jam's brokered POST /report, self-scoped to the caller's
// OWN instance: it validates the report, has the tracker move the ticket and
// comment (synchronously: a failure is a 502 the agent can retry), then
// stamps Instance.Report. Implements http.Handler.
type ReportHandler struct {
	store    escalateStore
	reporter TicketReporter
	setter   reportSetter
	now      func() time.Time
	log      *slog.Logger
}

// NewReportHandler constructs a ReportHandler; a nil reporter answers 503.
func NewReportHandler(store escalateStore, reporter TicketReporter, setter reportSetter, now func() time.Time, log *slog.Logger) *ReportHandler {
	return &ReportHandler{store: store, reporter: reporter, setter: setter, now: now, log: log}
}

func (h *ReportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxReportBodyBytes)
	var rep TicketReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := ValidateReport(&rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	inst, _ := h.store.GetInstance(actor.ID)
	if inst.Unit == "" {
		http.Error(w, "no ticket: report is for ticket sessions", http.StatusBadRequest)
		return
	}
	if h.reporter == nil {
		http.Error(w, ErrNoTracker.Error(), http.StatusServiceUnavailable)
		return
	}
	rep.At = h.now()
	if err := h.reporter.Report(r.Context(), inst, rep); err != nil {
		if errors.Is(err, ErrNoTracker) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		h.log.Warn("report: tracker update failed", "actor", actor.ID, "ticket", inst.Unit, "state", rep.State, "error", err.Error())
		http.Error(w, "tracker update failed; retry", http.StatusBadGateway)
		return
	}
	if err := h.setter.SetReport(actor.ID, rep); err != nil {
		h.log.Error("report: record failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "record failed", http.StatusBadGateway)
		return
	}
	h.log.Info("report", "actor", actor.ID, "ticket", inst.Unit, "state", rep.State)
	w.WriteHeader(http.StatusNoContent)
}
