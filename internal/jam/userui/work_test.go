package userui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// A unit of work's record (docs/modules/ROOT/pages/usage/jam/work-units.adoc):
// GET api/work/{key}, and the rail's work row's state.

func workAs(c *convEnv, p jam.Participant, admin bool, key string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Handler(c.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/api/work/"+key, nil), p, admin))
	return rec
}

// redispatched gives COV-1 a past worker that ended, spider as its current
// worker, and a reported PR.
func redispatched(t *testing.T, c *convEnv) (t0, t1, t2 time.Time) {
	t.Helper()
	t0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	t1, t2 = t0.Add(time.Hour), t0.Add(2*time.Hour)
	issue := jam.UnitIssue{Title: "Fix the flaky login test", URL: "https://linear.app/acme/issue/COV-1"}
	if err := c.st.StartUnitWorker(c.work.ID, jam.UnitWorker{Session: "ses_01j9q3oldoldoldoldoldoldold", Label: "ses_01j9q3oldoldoldoldoldoldold", Started: t0}, issue); err != nil {
		t.Fatal(err)
	}
	if err := c.st.StartUnitWorker(c.work.ID, jam.UnitWorker{Session: ident.ID(c.spider.ActorID), Label: "spider", Started: t1}, jam.UnitIssue{}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.ReportUnit(c.work.ID, jam.ReportInReview, "https://github.com/acme/app/pull/9", t2); err != nil {
		t.Fatal(err)
	}
	return t0, t1, t2
}

func TestWorkUnitAPI(t *testing.T) {
	c := newConvEnv(t)
	t0, t1, t2 := redispatched(t, c)
	want := WorkUnit{
		Key: "COV-1", Title: "Fix the flaky login test", URL: "https://linear.app/acme/issue/COV-1", Log: string(c.work.ID), State: "in-review",
		Workers: []UnitWorker{
			{Session: "ses_01j9q3oldoldoldoldoldoldold", Label: "ses_01j9q3oldoldoldoldoldoldold", Started: &t0, Stopped: &t1},
			{Session: c.spider.ActorID, Label: "spider", Started: &t1, Current: true},
		},
		PullRequests: []UnitPR{{URL: "https://github.com/acme/app/pull/9", Number: 9, State: "open", ReportedAt: t2}},
	}
	// A project member may read the log, so the unit; spider is a standing
	// session, so only an operator may open its view.
	if got := decodeAs[WorkUnit](t, workAs(c, c.p, false, "COV-1")); !reflect.DeepEqual(got, want) {
		t.Errorf("work unit = %+v\nwant %+v", got, want)
	}
	want.Workers[1].View = "/app/user/s/" + c.spider.ActorID
	if got := decodeAs[WorkUnit](t, workAs(c, c.p, true, "COV-1")); !reflect.DeepEqual(got, want) {
		t.Errorf("work unit as an operator = %+v\nwant %+v", got, want)
	}
	// The rail's work row says the unit's state.
	if r := railAs(t, c, false, ""); len(r.Work) != 1 || r.Work[0].State != "in-review" {
		t.Errorf("work rows = %+v, want COV-1 in review", r.Work)
	}
}

// Someone who may not read the log may not read the unit: the same 404 as a
// key with no unit.
func TestWorkUnitAPIRefusals(t *testing.T) {
	c := newConvEnv(t)
	redispatched(t, c)
	carol := jam.Participant{UserID: ident.New(ident.User), Name: "carol", Projects: []string{"proj"}}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"outsider": workAs(c, carol, false, "COV-1"),
		"no unit":  workAs(c, c.p, false, "COV-404"),
	} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, rec.Code)
		}
	}
	// A log with no record yet is a unit with no workers, state or PRs.
	c2 := newConvEnv(t)
	got := decodeAs[WorkUnit](t, workAs(c2, c2.p, false, "COV-1"))
	if got.Key != "COV-1" || got.State != "" || got.Workers == nil || len(got.Workers) != 0 || got.PullRequests == nil {
		t.Errorf("unrecorded unit = %#v", got)
	}
}
