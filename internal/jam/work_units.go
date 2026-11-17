package jam

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// A unit of work's record (docs/modules/ROOT/pages/usage/jam/work-units.adoc):
// the sessions that have been its worker, the pull requests reported for it,
// and its state as last reported. It is keyed by its work log's channel id.
// The intercom records worker swaps (ownTicketChannel, Ended), and the
// supervisor the reports (SetReport).

// UnitIssue is a unit's tracker issue, as its workers were raised with:
// its title and URL ("" = not known).
type UnitIssue struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// UnitWorker is one session's turn as a unit's worker. Started is zero when
// it is not known (a worker from before the record, backfilled); Stopped
// reports that the turn ended, at StoppedAt (zero when not known).
type UnitWorker struct {
	Session   ident.ID  `json:"session"`
	Label     string    `json:"label"`
	Started   time.Time `json:"started,omitzero"`
	Stopped   bool      `json:"stopped,omitempty"`
	StoppedAt time.Time `json:"stopped_at,omitzero"`
}

// PRState is a unit's pull request's state.
type PRState string

const (
	PROpen   PRState = "open"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed"
)

// UnitPR is a pull request reported for a unit: Number is parsed from the
// URL (0 when it has none), Title is "" until something reads it.
type UnitPR struct {
	URL        string    `json:"url"`
	Number     int       `json:"number,omitempty"`
	Title      string    `json:"title,omitempty"`
	State      PRState   `json:"state"`
	ReportedAt time.Time `json:"reported_at"`
}

// WorkUnit is a unit of work's record. State is its last reported state
// (a Report* constant; "" = none), at StateAt. Workers are in the order they
// started, PRs in the order they were first reported.
type WorkUnit struct {
	Log     ident.ID     `json:"log"`
	Issue   UnitIssue    `json:"issue,omitzero"`
	State   string       `json:"state,omitempty"`
	StateAt time.Time    `json:"state_at,omitzero"`
	Workers []UnitWorker `json:"workers,omitempty"`
	PRs     []UnitPR     `json:"prs,omitempty"`
}

// CurrentWorker is the unit's current worker (ok=false: none).
func (u WorkUnit) CurrentWorker() (UnitWorker, bool) {
	for _, w := range u.Workers {
		if !w.Stopped {
			return w, true
		}
	}
	return UnitWorker{}, false
}

// WorkUnitStore is the Store's work_units tables. Every write needs log to
// be a work log; the record exists from its first write.
type WorkUnitStore interface {
	// WorkUnit is log's record (ok=false: none yet).
	WorkUnit(log ident.ID) (WorkUnit, bool)
	// StartUnitWorker makes w log's current worker from w.Started, stopping
	// any other current worker then (a no-op while w.Session is current).
	// issue's non-empty fields replace the unit's.
	StartUnitWorker(log ident.ID, w UnitWorker, issue UnitIssue) error
	// StopUnitWorker stops session's turn as log's worker at at (a no-op
	// when it is not the current worker).
	StopUnitWorker(log, session ident.ID, at time.Time) error
	// ReportUnit records a report of log's unit: its state, and pr (a URL;
	// "" = none), recorded open when it is new.
	ReportUnit(log ident.ID, state, pr string, at time.Time) error
}

// prNumber matches a pull (or merge) request's number in its URL.
var prNumber = regexp.MustCompile(`/(?:pull|pulls|merge_requests)/(\d+)(?:[/?#]|$)`)

// PRNumber is the pull request number in url (0 when it has none).
func PRNumber(url string) int {
	m := prNumber.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func copyWorkUnit(u WorkUnit) WorkUnit {
	u.Workers, u.PRs = slices.Clone(u.Workers), slices.Clone(u.PRs)
	return u
}

func (m *memState) WorkUnit(log ident.ID) (WorkUnit, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.units[log]
	return copyWorkUnit(u), ok
}

// unitFor is log's record to change (a new one when none), once log is
// checked to be a work log. Caller holds mu.
func (m *memState) unitFor(log ident.ID) (WorkUnit, error) {
	c, ok := m.channels[log]
	if !ok {
		return WorkUnit{}, fmt.Errorf("%w: %s", ErrChannelNotFound, log)
	}
	if c.Kind != SourceWork {
		return WorkUnit{}, fmt.Errorf("work unit: %s is a %s, not a work log", log, c.Kind)
	}
	if u, ok := m.units[log]; ok {
		return copyWorkUnit(u), nil
	}
	return WorkUnit{Log: log}, nil
}

// prepareStartUnitWorker is the record after StartUnitWorker; changed is
// false for a no-op. Caller holds mu.
func (m *memState) prepareStartUnitWorker(log ident.ID, w UnitWorker, issue UnitIssue) (u WorkUnit, changed bool, err error) {
	if w.Session == "" {
		return WorkUnit{}, false, fmt.Errorf("work unit: worker session is required")
	}
	if u, err = m.unitFor(log); err != nil {
		return WorkUnit{}, false, err
	}
	if issue.Title != "" && issue.Title != u.Issue.Title {
		u.Issue.Title, changed = issue.Title, true
	}
	if issue.URL != "" && issue.URL != u.Issue.URL {
		u.Issue.URL, changed = issue.URL, true
	}
	if cur, ok := u.CurrentWorker(); ok && cur.Session == w.Session {
		return u, changed, nil
	}
	for i := range u.Workers {
		if !u.Workers[i].Stopped {
			u.Workers[i].Stopped, u.Workers[i].StoppedAt = true, w.Started
		}
	}
	w.Stopped, w.StoppedAt = false, time.Time{}
	u.Workers = append(u.Workers, w)
	return u, true, nil
}

func (m *memState) prepareStopUnitWorker(log, session ident.ID, at time.Time) (u WorkUnit, changed bool, err error) {
	if u, err = m.unitFor(log); err != nil {
		return WorkUnit{}, false, err
	}
	for i, w := range u.Workers {
		if w.Session == session && !w.Stopped {
			u.Workers[i].Stopped, u.Workers[i].StoppedAt = true, at
			changed = true
		}
	}
	return u, changed, nil
}

func (m *memState) prepareReportUnit(log ident.ID, state, pr string, at time.Time) (WorkUnit, error) {
	switch state {
	case ReportInProgress, ReportInReview, ReportNeedsInput, ReportBlocked, ReportDone:
	default:
		return WorkUnit{}, fmt.Errorf("work unit: unknown state %q", state)
	}
	u, err := m.unitFor(log)
	if err != nil {
		return WorkUnit{}, err
	}
	u.State, u.StateAt = state, at
	if pr != "" && !slices.ContainsFunc(u.PRs, func(p UnitPR) bool { return p.URL == pr }) {
		u.PRs = append(u.PRs, UnitPR{URL: pr, Number: PRNumber(pr), State: PROpen, ReportedAt: at})
	}
	return u, nil
}

func (m *memState) applyPutWorkUnit(u WorkUnit) { m.units[u.Log] = copyWorkUnit(u) }

func (fs *MemStore) StartUnitWorker(log ident.ID, w UnitWorker, issue UnitIssue) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, changed, err := fs.prepareStartUnitWorker(log, w, issue)
	if err != nil || !changed {
		return err
	}
	fs.applyPutWorkUnit(u)
	return nil
}

func (fs *MemStore) StopUnitWorker(log, session ident.ID, at time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, changed, err := fs.prepareStopUnitWorker(log, session, at)
	if err != nil || !changed {
		return err
	}
	fs.applyPutWorkUnit(u)
	return nil
}

func (fs *MemStore) ReportUnit(log ident.ID, state, pr string, at time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, err := fs.prepareReportUnit(log, state, pr, at)
	if err != nil {
		return err
	}
	fs.applyPutWorkUnit(u)
	return nil
}
