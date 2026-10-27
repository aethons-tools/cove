package userui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
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
	if len(b.Tabs) != 1 || b.Tabs[0].Key != "inbox" || b.Tabs[0].Href != "/app/next/" {
		t.Errorf("tabs = %+v, want the Inbox pointing at /app/next/", b.Tabs)
	}
	// Whiteboards on: the app bundles its own editor, so the old bundle
	// (uiassets.WhiteboardBuilt) doesn't gate it.
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	rec = apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/bootstrap")
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || !b.Boards {
		t.Errorf("boards on: bootstrap = %+v (%v)", b, err)
	}
}

func TestNextServesTheAppWithBootstrap(t *testing.T) {
	e, p := fixture()
	h := Next(e.Deps, nil, nil)
	rec := apiGet(t, h, &p, "/app/next/c/"+engID)
	// Without a built bundle, 503 (webapp's not-built page); with one, the
	// index carrying alice's bootstrap. Either way, never a 404.
	switch rec.Code {
	case 503:
	case 200:
		if !strings.Contains(rec.Body.String(), `"name":"alice"`) {
			t.Errorf("index lacks alice's bootstrap: %s", rec.Body)
		}
	default:
		t.Errorf("GET /app/next/c/… = %d", rec.Code)
	}
}
