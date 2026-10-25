package userui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

// fakeFrames records what it was asked to mint.
type fakeFrames struct {
	calls []frameCall
	err   error
}

type frameCall struct {
	p                      collab.Principal
	display, actorID, name string
	right                  collab.Right
}

func (f *fakeFrames) FrameURL(p collab.Principal, display, actorID, name string, right collab.Right) (string, error) {
	f.calls = append(f.calls, frameCall{p, display, actorID, name, right})
	if f.err != nil {
		return "", f.err
	}
	return "https://" + name + "--" + actorID + ".preview.test/__jam/frame?t=tk", nil
}

// frameEnv is sessionEnv with previews and a fake Frames on; the personal
// session (alice's: preview and ssh) exposes app, and the standing one's
// role grants members preview only.
func frameEnv(t *testing.T) (*env, jam.Participant, jam.Instance, jam.Instance, *fakeFrames) {
	t.Helper()
	e, p, personal, standing, _ := sessionEnv(t)
	ff := &fakeFrames{}
	e.Deps.Frames = ff
	e.Deps.PreviewURL = func(actorID, name string) (string, error) {
		return "https://" + name + "--" + actorID + ".preview.test/", nil
	}
	personal.Exposures = []jam.Exposure{{Name: "app", Port: 5173}, {Name: "ide", Port: 4000}}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	proj, _ := e.st.GetProject("proj")
	if err := e.st.PutRole(string(proj.ID), jam.Role{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	return e, p, personal, standing, ff
}

func postFrame(h http.Handler, p jam.Participant, id, name string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/app/user/session/"+id+"/frame", strings.NewReader(url.Values{"name": {name}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, asPrincipal(r, p, false))
	return rec
}

func TestFrameRoute(t *testing.T) {
	e, p, personal, standing, ff := frameEnv(t)
	h := Handler(e.Deps, nil)

	// An SSH holder gets the IDE under the ssh right, and the plain URL.
	rec := postFrame(h, p, personal.ActorID, "ide")
	if rec.Code != http.StatusOK {
		t.Fatalf("ide by the owner = %d %s", rec.Code, rec.Body)
	}
	var got struct{ URL, Open string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://ide--"+personal.ActorID+".preview.test/__jam/frame?t=tk" || got.Open != "https://ide--"+personal.ActorID+".preview.test/" {
		t.Errorf("ide = %+v", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}
	if len(ff.calls) != 1 || ff.calls[0].right != collab.SSH || ff.calls[0].name != "ide" || ff.calls[0].actorID != personal.ActorID ||
		ff.calls[0].p.UserID != p.UserID || ff.calls[0].display != "alice" {
		t.Errorf("minted %+v", ff.calls)
	}

	// An exposed name is minted under the preview right.
	if rec := postFrame(h, p, personal.ActorID, "app"); rec.Code != http.StatusOK {
		t.Fatalf("app = %d", rec.Code)
	}
	if c := ff.calls[len(ff.calls)-1]; c.right != collab.Preview || c.name != "app" {
		t.Errorf("app minted %+v", c)
	}

	// A name the session doesn't expose is not found.
	n := len(ff.calls)
	if rec := postFrame(h, p, personal.ActorID, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unexposed = %d", rec.Code)
	}
	if rec := postFrame(h, p, personal.ActorID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("no name = %d", rec.Code)
	}

	// A member without ssh may not have the IDE.
	if rec := postFrame(h, p, standing.ActorID, "ide"); rec.Code != http.StatusForbidden {
		t.Errorf("ide without ssh = %d", rec.Code)
	}
	if len(ff.calls) != n {
		t.Errorf("minted on a refusal: %+v", ff.calls[n:])
	}

	// An outsider sees nothing.
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	if rec := postFrame(h, bob, personal.ActorID, "app"); rec.Code != http.StatusNotFound {
		t.Errorf("outsider = %d", rec.Code)
	}

	// A failed mint is an error, not a URL.
	ff.err = errors.New("boom")
	if rec := postFrame(h, p, personal.ActorID, "app"); rec.Code != http.StatusInternalServerError {
		t.Errorf("mint failure = %d", rec.Code)
	}
}

func TestFrameRouteOff(t *testing.T) {
	e, p, personal, _, _ := frameEnv(t)
	e.Deps.Frames = nil
	if rec := postFrame(Handler(e.Deps, nil), p, personal.ActorID, "ide"); rec.Code != http.StatusNotFound {
		t.Errorf("frames off = %d", rec.Code)
	}
}

// Minting is capped per user: the 31st in a minute is refused.
func TestFrameRouteRateLimited(t *testing.T) {
	e, p, personal, _, ff := frameEnv(t)
	h := Handler(e.Deps, nil)
	for i := range maxFrameMints {
		if rec := postFrame(h, p, personal.ActorID, "app"); rec.Code != http.StatusOK {
			t.Fatalf("mint %d = %d", i+1, rec.Code)
		}
	}
	if rec := postFrame(h, p, personal.ActorID, "app"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("mint %d = %d, want 429", maxFrameMints+1, rec.Code)
	}
	if len(ff.calls) != maxFrameMints {
		t.Errorf("minted %d", len(ff.calls))
	}
}

func TestMintLimiterSlides(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newMintLimiter(func() time.Time { return now })
	alice, bob := ident.New(ident.User), ident.New(ident.User)
	for range maxFrameMints {
		if !l.allow(alice) {
			t.Fatal("refused under the cap")
		}
		now = now.Add(time.Second)
	}
	if l.allow(alice) {
		t.Fatal("allowed over the cap")
	}
	if !l.allow(bob) {
		t.Fatal("one user's mints count against another")
	}
	// The oldest falls out of the window: one more is allowed, then not.
	now = time.Unix(1000, 0).Add(frameMintWindow)
	if !l.allow(alice) {
		t.Fatal("still refused once the oldest left the window")
	}
	if l.allow(alice) {
		t.Fatal("allowed two after one left the window")
	}
}

func TestStudioTab(t *testing.T) {
	e, p, personal, standing, _ := frameEnv(t)
	head := func(inst jam.Instance) string {
		rec := httptest.NewRecorder()
		Handler(e.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/session/"+inst.ActorID+"/head", nil), p, false))
		return rec.Body.String()
	}
	body := head(personal)
	if !strings.Contains(body, `data-tab="studio"`) {
		t.Errorf("an ssh holder has no Studio tab:\n%s", body)
	}
	if strings.Index(body, `data-tab="studio"`) > strings.Index(body, `data-tab="thinking"`) {
		t.Error("Studio isn't the first tab")
	}
	if strings.Contains(head(standing), `data-tab="studio"`) {
		t.Error("a member without ssh has a Studio tab")
	}
	e.Deps.Frames = nil
	if strings.Contains(head(personal), `data-tab="studio"`) {
		t.Error("a Studio tab with framing off")
	}
	e.Deps.Frames = &fakeFrames{}

	code, body := e.paneTab(t, p, personal.ActorID, "studio", false)
	if code != http.StatusOK || !strings.Contains(body, `id="studio"`) || !strings.Contains(body, `data-session="`+personal.ActorID+`"`) ||
		!strings.Contains(body, `class="st-reload"`) || !strings.Contains(body, `class="st-open"`) {
		t.Errorf("studio pane = %d:\n%s", code, body)
	}
	if _, body := e.paneTab(t, p, standing.ActorID, "studio", false); strings.Contains(body, `id="studio"`) {
		t.Error("a studio pane without ssh")
	}
}

func TestPreviewsViewHere(t *testing.T) {
	e, p, personal, _, _ := frameEnv(t)
	_, body := e.paneTab(t, p, personal.ActorID, "previews", false)
	if !strings.Contains(body, `class="pv-here" data-name="app"`) {
		t.Errorf("no View here:\n%s", body)
	}
	if strings.Contains(body, "ide--") || strings.Contains(body, `data-name="ide"`) {
		t.Errorf("previews list the IDE:\n%s", body)
	}
	e.Deps.Frames = nil
	if _, body := e.paneTab(t, p, personal.ActorID, "previews", false); strings.Contains(body, "pv-here") {
		t.Error("View here with framing off")
	}
}
