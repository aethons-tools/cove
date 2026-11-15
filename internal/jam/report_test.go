package jam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

type fakeReporter struct {
	got []TicketReport
	err error
}

func (f *fakeReporter) Report(_ context.Context, _ Instance, r TicketReport) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, r)
	return nil
}

type fakeReportSetter struct{ set []TicketReport }

func (f *fakeReportSetter) SetReport(_ string, r TicketReport) error {
	f.set = append(f.set, r)
	return nil
}

func reportFixture(unit string) (*ReportHandler, *fakeReporter, *fakeReportSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-AET-1"}
	st.instances["cove-AET-1"] = Instance{ActorID: "cove-AET-1", Unit: unit}
	rep, set := &fakeReporter{}, &fakeReportSetter{}
	return NewReportHandler(st, rep, set, func() time.Time { return time.Unix(5000, 0) }, testLogger()), rep, set
}

func TestReportTransitionsAndStamps(t *testing.T) {
	h, rep, set := reportFixture("AET-1")
	rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"in-review","summary":" PR is up ","pr":"https://github.com/o/r/pull/7"}`)
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := TicketReport{State: "in-review", Summary: "PR is up", PR: "https://github.com/o/r/pull/7", At: time.Unix(5000, 0)}
	if len(rep.got) != 1 || rep.got[0] != want || len(set.set) != 1 || set.set[0] != want {
		t.Fatalf("reported %+v stamped %+v", rep.got, set.set)
	}
}

func TestReportRejects(t *testing.T) {
	h, rep, _ := reportFixture("AET-1")
	for _, body := range []string{
		`{"state":"merged","summary":"x"}`,
		`{"state":"done","summary":"  "}`,
		`{"state":"in-review","summary":"x"}`,
		`{"state":"done","summary":"x","pr":"ftp://x"}`,
		`{"state":"done","summary":"` + strings.Repeat("x", 4001) + `"}`,
	} {
		if rec := doTurnEnd(h, "POST", "/report", "tok", body); rec.Code != 400 {
			t.Errorf("%.60s → %d, want 400", body, rec.Code)
		}
	}
	if rec := doTurnEnd(h, "GET", "/report", "tok", ""); rec.Code != 405 {
		t.Errorf("GET → %d, want 405", rec.Code)
	}
	if len(rep.got) != 0 {
		t.Fatalf("reported %+v", rep.got)
	}
}

func TestReportWithoutTicketIs400(t *testing.T) {
	h, rep, _ := reportFixture("")
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "no ticket") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(rep.got) != 0 {
		t.Fatal("reported without a ticket")
	}
}

func TestReportTrackerFailureIs502(t *testing.T) {
	h, rep, set := reportFixture("AET-1")
	rep.err = errors.New("linear down")
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 502 {
		t.Fatalf("%d", rec.Code)
	}
	if len(set.set) != 0 {
		t.Fatal("stamped a report the tracker never took")
	}
}

func TestReportNoTrackerIs503(t *testing.T) {
	h, rep, _ := reportFixture("AET-1")
	rep.err = ErrNoTracker
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 503 {
		t.Fatalf("%d", rec.Code)
	}
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "c"}
	st.instances["c"] = Instance{ActorID: "c", Unit: "AET-1"}
	if rec := doTurnEnd(NewReportHandler(st, nil, &fakeReportSetter{}, time.Now, testLogger()), "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 503 {
		t.Fatalf("nil reporter: %d", rec.Code)
	}
}

func TestReportTerminal(t *testing.T) {
	for s, want := range map[string]bool{ReportInProgress: false, ReportInReview: false, ReportNeedsInput: false, ReportBlocked: true, ReportDone: true} {
		if got := (TicketReport{State: s}).Terminal(); got != want {
			t.Errorf("%s terminal = %v", s, got)
		}
	}
}

// The summary is posted to the unit's work log as the session (the tracker
// relay mirrors it to the issue); the tracker only moves the ticket.
func TestReportPostsSummaryToWorkLog(t *testing.T) {
	f := newSqFixture(t)
	rep, set := &fakeReporter{}, &fakeReportSetter{}
	h := NewReportHandler(f.store, rep, set, func() time.Time { return f.now }, testLogger())
	h.SetIntercom(f.ic)
	req := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(`{"state":"in-review","summary":"PR is up","pr":"https://github.com/o/r/pull/7"}`))
	req.Header.Set("Authorization", "Bearer tok-"+f.ticket.ActorID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || len(rep.got) != 1 || len(set.set) != 1 {
		t.Fatalf("%d %s; reported %+v", rec.Code, rec.Body, rep.got)
	}
	ch, err := f.ic.resolveTicket(f.poster(f.ticket), f.project, f.ticket.Unit, anyGlobs)
	if err != nil || ch.Kind != SourceWork {
		t.Fatalf("work log = %+v, %v", ch, err)
	}
	got := f.log.ChannelBefore(ch.ID, 0, 10)
	if len(got) != 1 || got[0].From != ident.ID(f.ticket.ActorID) || got[0].Body != "**in-review** — PR is up\n\nPR: https://github.com/o/r/pull/7" {
		t.Fatalf("work log entries = %+v", got)
	}
}
