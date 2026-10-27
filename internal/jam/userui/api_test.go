package userui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/push"
)

func apiGet(t *testing.T, h http.Handler, p *jam.Participant, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if p != nil {
		// As the gate does: the participant for userui, the principal for
		// the title bar (appui.TitlebarFor).
		req = appui.WithPrincipal(jam.WithParticipant(req, *p), appui.Principal{User: p})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func checkJSONHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("JSON headers = %v", h)
	}
}

func TestAPIRailShowsOnlyTheViewersChannels(t *testing.T) {
	e, p := fixture()
	// bob writes to carol in a chat alice is not in: never in alice's rail.
	for _, n := range []string{"bob", "carol"} {
		if err := jam.AddPerson(e.st, "proj", jam.Human{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	carol, _ := e.st.LookupName(ident.User, "carol")
	pl, err := e.Intercom.PlanPersonChat(bob, carol)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "secret"}); err != nil {
		t.Fatal(err)
	}
	// bob to alice: in her rail, unread.
	pa, err := e.Intercom.PlanPersonChat(bob, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.Post(pa, intercom.Squawk{From: bob, Body: "ping"}); err != nil {
		t.Fatal(err)
	}

	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/rail")
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var got RailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	labels := map[string]int{}
	for _, g := range got.Groups {
		for _, r := range g.Rows {
			labels[r.Label] = r.Unread
		}
	}
	if _, ok := labels["eng"]; !ok {
		t.Errorf("rail lacks eng: %s", rec.Body)
	}
	if labels["bob"] != 1 {
		t.Errorf("bob chat unread = %d, want 1: %s", labels["bob"], rec.Body)
	}
	if strings.Contains(rec.Body.String(), "carol") {
		t.Errorf("rail shows a chat alice is not in: %s", rec.Body)
	}
	if got.UnreadTotal != 1 {
		t.Errorf("unreadTotal = %d, want 1", got.UnreadTotal)
	}
}

func TestAPIRailEmptyIsAList(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("proj"); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(st, "proj", jam.Human{Name: "dana"}); err != nil {
		t.Fatal(err)
	}
	dana, _ := st.LookupName(ident.User, "dana")
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(st, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	p := jam.Participant{UserID: dana, Projects: []string{"proj"}, Name: "dana"}
	rec := apiGet(t, Handler(Deps{Store: st, Intercom: ic, Log: lg}, nil), &p, "/app/user/api/rail")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"groups":[]`) {
		t.Errorf("empty rail = %d %s, want groups:[]", rec.Code, rec.Body)
	}
}

func TestAPIWithoutParticipantIsJSON403(t *testing.T) {
	e, _ := fixture()
	for _, path := range []string{"/app/user/api/rail", "/app/user/api/bootstrap"} {
		rec := apiGet(t, Handler(e.Deps, nil), nil, path)
		if rec.Code != 403 {
			t.Errorf("%s code = %d, want 403", path, rec.Code)
		}
		checkJSONHeaders(t, rec)
		var body APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "forbidden" || body.Error == "" {
			t.Errorf("%s body = %s", path, rec.Body)
		}
	}
}

func TestAPIUnknownRouteIsJSON404(t *testing.T) {
	e, p := fixture()
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/nope")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("unknown API route = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
}

func TestAPIBootstrap(t *testing.T) {
	e, p := fixture()
	rec := apiGet(t, Handler(e.Deps, nil, WithPush("vapid-key")), &p, "/app/user/api/bootstrap")
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var b Bootstrap
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.User.ID != string(p.UserID) || b.User.Name != "alice" || b.User.Initial != "A" {
		t.Errorf("user = %+v", b.User)
	}
	if b.PushKey != "vapid-key" || b.Admin || b.Boards || b.AttachMax != 0 {
		t.Errorf("bootstrap = %+v", b)
	}
	if len(b.Tabs) != 1 || b.Tabs[0].Key != "inbox" || b.Tabs[0].Href != "/app/user/" {
		t.Errorf("tabs = %+v, want the Inbox at /app/user/", b.Tabs)
	}
	if b.PushFocus {
		t.Error("pushFocus without WithPushFocus")
	}
	rec = apiGet(t, Handler(e.Deps, nil, WithPush("k"), WithPushFocus(push.NewFocus(time.Now))), &p, "/app/user/api/bootstrap")
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || !b.PushFocus {
		t.Errorf("with push focus: bootstrap = %+v (%v)", b, err)
	}
	// Whiteboards on: the bootstrap says so (the app bundles its own editor).
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	rec = apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/bootstrap")
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || !b.Boards {
		t.Errorf("boards on: bootstrap = %+v (%v)", b, err)
	}
}

// The user area serves the Jam app at every page path, and every kept route
// still answers itself (none is shadowed by the app's catch-all).
func TestUserAreaServesTheApp(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil, WithPush("k"))
	for _, path := range []string{"/app/user/", "/app/user/c/" + engID, "/app/user/boards/brd_x", "/app/user/new"} {
		rec := apiGet(t, h, &p, path)
		switch {
		case rec.Code == 503: // the bundle isn't built: webapp's not-built page
		case rec.Code == 200 && strings.Contains(rec.Body.String(), `"name":"alice"`) && strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'"):
			// The app head (manifest, icons), never an inline registration.
			if !strings.Contains(rec.Body.String(), `rel="manifest"`) || strings.Contains(rec.Body.String(), "serviceWorker.register") {
				t.Errorf("GET %s head: %s", path, rec.Body)
			}
		default:
			t.Errorf("GET %s = %d %v", path, rec.Code, rec.Header())
		}
	}
	for _, tc := range []struct {
		method, path string
		code         int
		ctype        string
	}{
		{"GET", "/app/user/api/rail", 200, "application/json"},
		{"GET", "/app/user/api/nope", 404, "application/json"},
		{"GET", "/app/user/push/key", 200, "application/json"},
		{"GET", "/app/user/blobs/blb_01j9q3zzzzzzzzzzzzzzzzzz", 503, ""}, // no attachment store here
		{"POST", "/app/user/c/x", 405, ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req = appui.WithPrincipal(jam.WithParticipant(req, p), appui.Principal{User: &p})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || (tc.ctype != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.ctype)) || strings.Contains(rec.Body.String(), `id="jam-bootstrap"`) {
			t.Errorf("%s %s = %d %q (want %d %s, not the app's page)", tc.method, tc.path, rec.Code, rec.Header().Get("Content-Type"), tc.code, tc.ctype)
		}
	}
}

// Without a participant (mounted outside the user gate) neither the app nor
// its API serves anything.
func TestUserAreaFailsClosedWithoutParticipant(t *testing.T) {
	e, _ := fixture()
	h := Handler(e.Deps, nil)
	for _, path := range []string{"/app/user/", "/app/user/c/" + engID, "/app/user/api/bootstrap", "/app/user/api/rail", "/app/user/api/conversations/" + engID} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil)) // no WithParticipant
		if rec.Code < 400 {
			t.Errorf("no-participant %s = %d, want a refusal", path, rec.Code)
		}
	}
}
