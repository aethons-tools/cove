package jam

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTurnEndSetter struct {
	endActor, endReason string
	idleActor           string
	idle                IdleOverride
	err                 error
}

func (f *fakeTurnEndSetter) SetEndRequested(a, r string) error {
	f.endActor, f.endReason = a, r
	return f.err
}
func (f *fakeTurnEndSetter) SetIdleOverride(a string, o IdleOverride) error {
	f.idleActor, f.idle = a, o
	return f.err
}

func turnEndFixture() (*TurnEndHandler, *fakeTurnEndSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	set := &fakeTurnEndSetter{}
	return NewTurnEndHandler(st, set, testLogger()), set
}

func doTurnEnd(h http.Handler, method, path, tok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEndRecordsReason(t *testing.T) {
	h, set := turnEndFixture()
	if rec := doTurnEnd(h, "POST", "/end", "tok", `{"reason":"merged"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if set.endActor != "cove-1" || set.endReason != "merged" {
		t.Fatalf("got %q %q", set.endActor, set.endReason)
	}
}

func TestEndRejects(t *testing.T) {
	h, _ := turnEndFixture()
	for _, c := range []struct {
		method, tok, body string
		want              int
	}{
		{"POST", "", `{"reason":"x"}`, 401},
		{"POST", "nope", `{"reason":"x"}`, 401},
		{"POST", "tok", `{"reason":""}`, 400},
		{"POST", "tok", `{"reason":"` + strings.Repeat("x", 1001) + `"}`, 400},
		{"POST", "tok", `{"reason":"` + strings.Repeat("x", 3000) + `"}`, 413},
		{"GET", "tok", ``, 405},
	} {
		if rec := doTurnEnd(h, c.method, "/end", c.tok, c.body); rec.Code != c.want {
			t.Errorf("%s %q… → %d, want %d", c.method, c.body[:min(len(c.body), 20)], rec.Code, c.want)
		}
	}
}

func TestTurnEndNoInstanceIs403(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "ghost"}
	h := NewTurnEndHandler(st, &fakeTurnEndSetter{}, testLogger())
	if rec := doTurnEnd(h, "POST", "/end", "tok", `{"reason":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestIdleSetsOverride(t *testing.T) {
	h, set := turnEndFixture()
	if rec := doTurnEnd(h, "PUT", "/idle", "tok", `{"duration":"45m","scope":"next"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if set.idleActor != "cove-1" || set.idle != (IdleOverride{Duration: 45 * time.Minute, Scope: IdleScopeNext}) {
		t.Fatalf("override = %q %+v", set.idleActor, set.idle)
	}
	if rec := doTurnEnd(h, "PUT", "/idle", "tok", `{"duration":"off","scope":"always"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("off: status %d", rec.Code)
	}
	if set.idle != (IdleOverride{Duration: 0, Scope: IdleScopeAlways}) {
		t.Fatalf("override = %+v", set.idle)
	}
}

func TestIdleRejects(t *testing.T) {
	h, _ := turnEndFixture()
	for _, body := range []string{
		`{"duration":"45m","scope":"sometimes"}`,
		`{"duration":"soon","scope":"next"}`,
		`{"duration":"-5m","scope":"next"}`,
		`{"duration":"0s","scope":"next"}`,
		`{"duration":"800h","scope":"next"}`,
	} {
		if rec := doTurnEnd(h, "PUT", "/idle", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", body, rec.Code)
		}
	}
	if rec := doTurnEnd(h, "POST", "/idle", "tok", `{"duration":"1m","scope":"next"}`); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /idle → %d, want 405", rec.Code)
	}
}

func TestTurnEndSetterErrorIs502(t *testing.T) {
	h, set := turnEndFixture()
	set.err = errors.New("store down")
	if rec := doTurnEnd(h, "POST", "/end", "tok", `{"reason":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
}
