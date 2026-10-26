package userui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// sessAPI calls /app/user/api/sessions/<path> as p (an operator when admin);
// a body is sent as JSON.
func sessAPI(t *testing.T, e *env, p jam.Participant, method, path, body string, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/app/user/api/sessions/"+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, asPrincipal(req, p, admin))
	return rec
}

func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAPISessionHead(t *testing.T) {
	e, p, personal, standing, _ := sessionEnv(t)
	rec := sessAPI(t, e, p, "GET", personal.ActorID, "", false)
	hd := decodeAs[SessionHead](t, rec)
	if hd.ID != personal.ActorID || hd.Kind != "personal" || hd.Role != "pair" || hd.Project != "proj" || hd.Cost != "$0.42" ||
		hd.Context != "ctx 84k / 200k · 42%" || hd.Uptime != "1h 30m" || hd.State.Text != "is idle" ||
		!hd.Ops || !hd.CanRelease || hd.CanRestart || hd.CanUpgrade {
		t.Errorf("personal head (member) = %+v", hd)
	}
	for _, k := range []string{`"canRelease":`, `"restartLabel":`, `"contextTip":`, `"state":{`, `"openAsks":`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("head JSON lacks %s: %s", k, rec.Body)
		}
	}
	op := decodeAs[SessionHead](t, sessAPI(t, e, p, "GET", standing.ActorID, "", true))
	if op.Label != "spider" || op.Kind != "standing" || !op.CanRestart || !op.CanUpgrade || op.RestartLabel != "Restart" || op.CanRelease {
		t.Errorf("standing head (operator) = %+v", op)
	}
}

// A session the viewer may not see answers exactly as a missing one, on
// every route.
func TestAPISessionHiddenIsUniform(t *testing.T) {
	e, _, personal, _, _ := sessionEnv(t)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	missing := string(ident.New(ident.Session))
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "", ""},
		{"GET", "/pane/status", ""},
		{"POST", "/release", "{}"},
		{"POST", "/interrupt", "{}"},
		{"GET", "/pane/thinking", ""},
		{"GET", "/thinking?stream=" + strings.Repeat("a", 32) + "&before=3", ""},
		{"GET", "/thinking/raw?stream=" + strings.Repeat("a", 32) + "&seq=1", ""},
	} {
		hidden := sessAPI(t, e, bob, tc.method, personal.ActorID+tc.path, tc.body, false)
		gone := sessAPI(t, e, bob, tc.method, missing+tc.path, tc.body, false)
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != gone.Body.String() || !strings.Contains(hidden.Body.String(), `"code":"not_found"`) ||
			!strings.Contains(hidden.Body.String(), "no such session") {
			t.Errorf("%s %s: hidden %d %s, missing %d %s", tc.method, tc.path, hidden.Code, hidden.Body, gone.Code, gone.Body)
		}
	}
}

func TestAPISessionPane(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	st := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", personal.ActorID+"/pane/status", "", false))
	want := map[string]string{}
	for _, r := range st.Status {
		want[r.Name] = r.Value
	}
	if st.Tab != "status" || want["Turns"] != "1 (1 episode)" || want["Cost"] != "$0.42" || want["Up"] != "1h 30m" {
		t.Errorf("status = %+v", st.Status)
	}
	// Previews off, and an empty Artifacts: every list is [] (types.ts says T[]).
	rec := sessAPI(t, e, p, "GET", personal.ActorID+"/pane/previews", "", false)
	if pv := decodeAs[SessionPane](t, rec); !pv.PreviewsOff {
		t.Errorf("previews = %+v, want off", pv)
	}
	for _, k := range []string{`"previews":[]`, `"boards":[]`, `"files":[]`, `"asks":[]`, `"links":[]`, `"status":[]`, `"streams":[]`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("previews JSON lacks %s: %s", k, rec.Body)
		}
	}
	for _, k := range []string{"Streams", "studio", "ideOpen", "IDEOpen"} {
		if strings.Contains(rec.Body.String(), `"`+k+`"`) {
			t.Errorf("pane JSON carries Studio (6b) or template-only field %q", k)
		}
	}
	// Artifacts: a link from the home channel, labelled.
	home, err := e.Intercom.HomeChannel(personal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: ident.ID(personal.ActorID), Body: "Opened https://github.com/aethons-tools/cove/pull/472"}); err != nil {
		t.Fatal(err)
	}
	art := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", personal.ActorID+"/pane/artifacts", "", false))
	if len(art.Links) != 1 || art.Links[0].Label != "cove PR #472" || art.Links[0].URL != "https://github.com/aethons-tools/cove/pull/472" {
		t.Errorf("artifacts links = %+v", art.Links)
	}
	// Studio comes in 6b; an unknown tab is a 404 too.
	for _, tab := range []string{"studio", "bogus"} {
		if rec := sessAPI(t, e, p, "GET", personal.ActorID+"/pane/"+tab, "", false); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Errorf("pane/%s = %d %s", tab, rec.Code, rec.Body)
		}
	}
}

func TestAPISessionActions(t *testing.T) {
	e, p, personal, standing, ops := sessionEnv(t)
	for _, tc := range []struct {
		inst   jam.Instance
		action string
		admin  bool
		code   int
		want   string
	}{
		{standing, "restart", false, 403, `"code":"forbidden"`},
		{standing, "upgrade", false, 403, "operators only, standing sessions"},
		{standing, "release", true, 403, "only the session's owner (or an operator) may release it"},
		{personal, "interrupt", false, 409, "is not available while the session is"},
		{personal, "explode", false, 404, `"code":"not_found"`},
		{personal, "release", false, 204, ""},
		{standing, "upgrade", true, 204, ""},
	} {
		rec := sessAPI(t, e, p, "POST", tc.inst.ActorID+"/"+tc.action, "{}", tc.admin)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s %s (admin %v) = %d %s, want %d %s", tc.inst.Name, tc.action, tc.admin, rec.Code, rec.Body, tc.code, tc.want)
		}
		if rec.Code >= 400 {
			checkJSONHeaders(t, rec)
		}
	}
	if len(ops.torn) != 1 || ops.torn[0] != personal.ActorID || len(ops.upgraded) != 1 {
		t.Errorf("ops: torn %v, upgraded %v", ops.torn, ops.upgraded)
	}
	// A command that fails upstream says so; a form post is refused.
	ops.cmdErr = errNoStream
	if rec := sessAPI(t, e, p, "POST", personal.ActorID+"/compact", "{}", false); rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), `"code":"action_failed"`) || !strings.Contains(rec.Body.String(), "the compact failed; see the jam log") {
		t.Errorf("failed compact = %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("POST", "/app/user/api/sessions/"+personal.ActorID+"/compact", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, asPrincipal(req, p, false))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post = %d", rec.Code)
	}
	// Session ops not wired: there are no actions.
	e.Deps.Ops = nil
	if rec := sessAPI(t, e, p, "POST", personal.ActorID+"/compact", "{}", false); rec.Code != http.StatusNotFound {
		t.Errorf("no ops = %d %s", rec.Code, rec.Body)
	}
}

var errNoStream = errors.New("no live stream")

func TestAPIThinking(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	actor := personal.ActorID
	old := thinkingPageSize
	thinkingPageSize = 3
	t.Cleanup(func() { thinkingPageSize = old })
	appendText(t, store, actor, 1, 5)

	pane := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", actor+"/pane/thinking", "", false))
	if pane.EventsOff || len(pane.ThinkingStreams) != 1 || pane.ThinkingStreams[0].ID != thinkStream || pane.ThinkingStreams[0].Events != 5 {
		t.Errorf("pane/thinking = %+v", pane)
	}
	pg := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=6", "", false))
	if len(pg.Events) != 3 || pg.Events[0].Seq != 3 || pg.Events[2].Seq != 5 || pg.Before != 3 || pg.Events[0].Kind != "text" {
		t.Errorf("page below 6 = %+v", pg)
	}
	last := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=3", "", false))
	if len(last.Events) != 2 || last.Before != 0 {
		t.Errorf("first page = %+v", last)
	}
	if rec := sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=1", "", false); !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("empty page = %s", rec.Body)
	}
	raw := decodeAs[ThinkingRaw](t, sessAPI(t, e, p, "GET", actor+"/thinking/raw?stream="+thinkStream+"&seq=2", "", false))
	if !strings.Contains(raw.Raw, "said 2") || raw.Truncated != 0 {
		t.Errorf("raw = %+v", raw)
	}
	for _, tc := range []struct {
		path string
		code int
		want string
	}{
		{"/thinking?stream=bogus&before=3", 400, `"code":"bad_request"`},
		{"/thinking?stream=" + thinkStream + "&before=0", 400, `"code":"bad_request"`},
		{"/thinking/raw?stream=" + thinkStream + "&seq=99", 404, "no such event"},
		{"/thinking/raw?stream=" + thinkStream, 400, `"code":"bad_request"`},
	} {
		if rec := sessAPI(t, e, p, "GET", actor+tc.path, "", false); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d %s, want %d %s", tc.path, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	// No events store: the pane says so; the page and raw are 404.
	e.Deps.Events, e.Deps.Hub = nil, nil
	if off := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", actor+"/pane/thinking", "", false)); !off.EventsOff || off.ThinkingStreams == nil {
		t.Errorf("events off: pane = %+v", off)
	}
	if rec := sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=3", "", false); rec.Code != 404 || !strings.Contains(rec.Body.String(), "aren't recorded") {
		t.Errorf("events off: page = %d %s", rec.Code, rec.Body)
	}
}

func TestAPIThinkingRawCapped(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	big := strings.Repeat("x", rawMax+10)
	if err := store.Append(textEvent(personal.ActorID, 1, big)); err != nil {
		t.Fatal(err)
	}
	raw := decodeAs[ThinkingRaw](t, sessAPI(t, e, p, "GET", personal.ActorID+"/thinking/raw?stream="+thinkStream+"&seq=1", "", false))
	if len(raw.Raw) != rawMax || raw.Truncated <= 0 {
		t.Errorf("raw len %d, truncated %d", len(raw.Raw), raw.Truncated)
	}
}
