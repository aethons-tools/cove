package userui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func (e *env) paneTab(t *testing.T, p jam.Participant, id, tab string, admin bool) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/session/"+id+"/pane?tab="+tab, nil), p, admin))
	return rec.Code, rec.Body.String()
}

func TestSessionPaneArtifacts(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	home, err := e.Intercom.HomeChannel(personal)
	if err != nil {
		t.Fatal(err)
	}
	sid := ident.ID(personal.ActorID)
	for _, body := range []string{
		"Opened https://github.com/aethons-tools/cove/pull/472, see (https://example.com/docs).",
		"Again: https://github.com/aethons-tools/cove/pull/472 and https://github.com/aethons-tools/cove/issues/9",
	} {
		if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: sid, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: sid, Body: "asked", ContentType: intercom.ContentPlain,
		Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAsk, Question: "Merge it?", Options: yesNoOpts})}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deps.Boards.Create(jam.Poster{ID: p.UserID}, string(home.ID), "Plan", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Something in another channel is not this session's artifact.
	e.post(p.UserID, "elsewhere https://example.org/room", "")

	code, body := e.paneTab(t, p, personal.ActorID, "artifacts", false)
	if code != http.StatusOK {
		t.Fatalf("artifacts = %d", code)
	}
	for _, want := range []string{"cove PR #472", "cove issue #9", `href="https://example.com/docs"`, "Merge it?", "open", "Plan", "/thumb.png"} {
		if !strings.Contains(body, want) {
			t.Errorf("artifacts missing %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "cove PR #472"); n != 1 {
		t.Errorf("PR #472 listed %d times, want once", n)
	}
	if strings.Contains(body, "example.org") {
		t.Error("artifacts list another channel's link")
	}
}

func TestSessionPanePreviewsAndStatus(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	// Previews off: said so.
	if _, body := e.paneTab(t, p, personal.ActorID, "previews", false); !strings.Contains(body, "aren't available") {
		t.Errorf("previews off:\n%s", body)
	}
	e.Deps.PreviewURL = func(actorID, name string) (string, error) {
		return "https://" + name + "--" + actorID + ".preview.test/", nil
	}
	personal.Exposures = []jam.Exposure{{Name: "web", Port: 5173}}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	// The owner may open their session's previews.
	_, body := e.paneTab(t, p, personal.ActorID, "previews", false)
	for _, want := range []string{`href="https://web--` + personal.ActorID + `.preview.test/"`, `target="_blank"`, "port 5173"} {
		if !strings.Contains(body, want) {
			t.Errorf("previews missing %q:\n%s", want, body)
		}
	}
	_, body = e.paneTab(t, p, personal.ActorID, "status", false)
	for _, want := range []string{"is idle", "Turns", "1 (1 episode)", "$0.42", "1,200 in / 300 out", "Up", "1h 30m"} {
		if !strings.Contains(body, want) {
			t.Errorf("status missing %q:\n%s", want, body)
		}
	}
	if code, _ := e.paneTab(t, p, personal.ActorID, "bogus", false); code != http.StatusNotFound {
		t.Errorf("unknown tab = %d", code)
	}
}

// A draft studio's kit reads "<kit> (draft @<tree7>)", never kit@v0; a
// published kit keeps kit@vN.
func TestSessionPaneStatusKit(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	personal.Kit = jam.KitRef{ID: "my-app", Version: 3}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	if _, body := e.paneTab(t, p, personal.ActorID, "status", false); !strings.Contains(body, "my-app@v3") {
		t.Errorf("published kit:\n%s", body)
	}
	tree := strings.Repeat("ab", 32)
	personal.Kit = jam.KitRef{ID: "my-app"}
	personal.KitDraft = &jam.KitDraftRef{Kit: "my-app", Tree: tree}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	_, body := e.paneTab(t, p, personal.ActorID, "status", false)
	if !strings.Contains(body, "my-app (draft @abababa)") || strings.Contains(body, "@v0") {
		t.Errorf("draft studio kit:\n%s", body)
	}
}

func TestSessionPaneHiddenFromOutsiders(t *testing.T) {
	e, _, personal, _, _ := sessionEnv(t)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	for _, tab := range paneTabs {
		if code, _ := e.paneTab(t, bob, personal.ActorID, tab, false); code != http.StatusNotFound {
			t.Errorf("%s by an outsider = %d", tab, code)
		}
	}
}

func TestLinksIn(t *testing.T) {
	got := linksIn("see https://a.test/x. and (https://b.test/y) or https://en.wikipedia.org/wiki/Go_(game), `https://c.test`")
	want := []string{"https://a.test/x", "https://b.test/y", "https://en.wikipedia.org/wiki/Go_(game)", "https://c.test"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("linksIn = %q, want %q", got, want)
	}
}

func TestSessionPaneThinking(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 2)
	code, body := e.paneTab(t, p, personal.ActorID, "thinking", false)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{`id="thinking"`, `data-f="errors"`, `class="th-stream"`, `<option value="` + thinkStream + `">`, "2 events"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}

	e.Deps.Events = nil
	_, body = e.paneTab(t, p, personal.ActorID, "thinking", false)
	if !strings.Contains(body, "aren't recorded") || strings.Contains(body, `id="thinking"`) {
		t.Errorf("events off body = %s", body)
	}
}
