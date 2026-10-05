package jam

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeAlarmSetter struct {
	set      []string // "name|schedule|note"
	cleared  []string
	setErr   error
	clearErr error
}

func (f *fakeAlarmSetter) SetAlarm(_, n, s, note string) (Alarm, error) {
	if f.setErr != nil {
		return Alarm{}, f.setErr
	}
	f.set = append(f.set, n+"|"+s+"|"+note)
	return Alarm{Name: n, Schedule: s, Note: note, NextAt: time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeAlarmSetter) ClearAlarm(_, n string) error {
	f.cleared = append(f.cleared, n)
	return f.clearErr
}

func alarmFixture() (*AlarmHandler, *fakeAlarmSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Alarms: []Alarm{{Name: "nightly", Schedule: "0 2 * * *", Note: "backup", NextAt: time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)}}}
	set := &fakeAlarmSetter{}
	return NewAlarmHandler(st, set, testLogger()), set
}

func TestAlarmsList(t *testing.T) {
	h, _ := alarmFixture()
	rec := doTurnEnd(h, "GET", "/alarms", "tok", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"nightly"`) || !strings.Contains(rec.Body.String(), `"next_at":"2026-10-06T02:00:00Z"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAlarmsSetAndClear(t *testing.T) {
	h, set := alarmFixture()
	if rec := doTurnEnd(h, "PUT", "/alarms/pr-watch", "tok", `{"schedule":"*/5 * * * *","note":"check the PR"}`); rec.Code != 200 {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body)
	}
	if len(set.set) != 1 || set.set[0] != "pr-watch|*/5 * * * *|check the PR" {
		t.Fatalf("set = %v", set.set)
	}
	if rec := doTurnEnd(h, "DELETE", "/alarms/pr-watch", "tok", ""); rec.Code != 204 {
		t.Fatalf("DELETE %d", rec.Code)
	}
	set.clearErr = ErrNoSuchAlarm
	if rec := doTurnEnd(h, "DELETE", "/alarms/gone", "tok", ""); rec.Code != 404 {
		t.Fatalf("DELETE absent %d, want 404", rec.Code)
	}
}

func TestAlarmsHandlerRejects(t *testing.T) {
	h, set := alarmFixture()
	set.setErr = ErrAlarmLimit
	if rec := doTurnEnd(h, "PUT", "/alarms/x", "tok", `{"schedule":"@hourly"}`); rec.Code != 400 {
		t.Fatalf("limit → %d, want 400", rec.Code)
	}
	set.setErr = errors.New("a recurring alarm may fire at most once a minute")
	if rec := doTurnEnd(h, "PUT", "/alarms/x", "tok", `{"schedule":"@every 10s"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "once a minute") {
		t.Fatalf("invalid → %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		m, p, tok string
		want      int
	}{
		{"GET", "/alarms", "", 401}, {"POST", "/alarms", "tok", 405}, {"PATCH", "/alarms/x", "tok", 405},
		{"PUT", "/alarms/", "tok", 404}, {"PUT", "/alarms/a/b", "tok", 404},
	} {
		if rec := doTurnEnd(h, c.m, c.p, c.tok, `{}`); rec.Code != c.want {
			t.Errorf("%s %s → %d, want %d", c.m, c.p, rec.Code, c.want)
		}
	}
}

// Through the real supervisor: every invalid schedule and the 21st alarm are
// 400s that store nothing.
func TestAlarmsRejects(t *testing.T) {
	sup, store, now := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewAlarmHandler(store, sup, testLogger())
	for i := 0; i < MaxAlarms; i++ {
		if rec := doTurnEnd(h, "PUT", "/alarms/a"+string(rune('a'+i)), tok, `{"schedule":"@hourly"}`); rec.Code != 200 {
			t.Fatalf("alarm %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"schedule":"@every 10s"}`, `{"schedule":"* * * * * *"}`, `{"schedule":"2026-10-05T11:00:00Z"}`, `{"schedule":"@hourly"}`} {
		if rec := doTurnEnd(h, "PUT", "/alarms/new-one", tok, body); rec.Code != 400 {
			t.Errorf("%s → %d, want 400", body, rec.Code)
		}
	}
	if inst, _ := store.GetInstance("w1"); len(inst.Alarms) != MaxAlarms {
		t.Fatalf("rejected sets changed the alarms: %d", len(inst.Alarms))
	}
}
