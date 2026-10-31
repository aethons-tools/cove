package collabhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

const ideHost = "ide--" + label26 + "." + testDomain

// loginAt gives user a preview session on host and returns its cookie.
func (ph *previewHarness) loginAt(t *testing.T, host, user string) *http.Cookie {
	t.Helper()
	code := ph.auth.bounce(t, "id-"+user, "", "https://"+host+"/", host)
	w := ph.auth.trade(host, code, "/")
	if w.Code != http.StatusFound {
		t.Fatalf("trade on %s = %d", host, w.Code)
	}
	return w.Result().Cookies()[0]
}

func (ph *previewHarness) getAt(host, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "https://"+host+path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	return rec
}

// grantSSH gives carol (not a member) an ssh-only grant.
func (ph *previewHarness) grantSSH(t *testing.T) {
	t.Helper()
	if err := ph.st.PutShareGrant(jam.ShareGrant{SessionID: previewSession, UserID: ph.ids["carol"], Rights: []string{"ssh"}, ExpiresAt: ph.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

// The former built-in IDE's name is ordinary (code-server was removed): with
// no exposure named ide, ide--<label> is 404 and nothing is opened.
func TestPreviewIDENameUnexposedIs404(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.grantSSH(t)
	for _, user := range []string{"alice", "carol"} {
		if rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, user)); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %q, want 404", user, rec.Code, rec.Body)
		}
	}
	if got := ph.tun.opened(); len(got) != 0 {
		t.Fatalf("opened %v", got)
	}
}

// A kit exposure named ide is an ordinary preview: it needs the preview
// right (not ssh), and reaches the exposed port.
func TestPreviewIDENameExposedIsAnOrdinaryPreview(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.setExposures([]jam.Exposure{{Name: "app", Port: appPort}, {Name: "ide", Port: 8443}})
	if rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "alice")); rec.Code != 200 {
		t.Fatalf("alice (preview): %d %q", rec.Code, rec.Body)
	}
	if got := ph.tun.opened(); len(got) != 1 || got[0] != previewSession+":8443" {
		t.Fatalf("opened %v, want the kit's ide port", got)
	}
	ph.grantSSH(t)
	rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "carol"))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "you may not use preview on this studio" {
		t.Fatalf("carol (ssh only): %d %q", rec.Code, rec.Body)
	}
}

// Any other name stays under preview: an ssh-only grantee is refused it.
func TestPreviewOtherNamesNeedPreview(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.grantSSH(t)
	rec := ph.get("/", ph.login(t, "carol"))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "you may not use preview on this studio" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if rec := ph.get("/", ph.login(t, "alice")); rec.Code != 200 {
		t.Fatalf("alice on app: %d", rec.Code)
	}
}

// A connection pooled under one right is never reused under the other.
func TestPreviewPoolHostIncludesRight(t *testing.T) {
	a := previewTarget{actorID: previewSession, port: 8443, user: "usr_x", right: collab.Preview}
	b := a
	b.right = collab.SSH
	if a.poolHost() == b.poolHost() {
		t.Fatal("pool host ignores the right")
	}
}

func TestPreviewPortAndRight(t *testing.T) {
	exps := []jam.Exposure{{Name: "app", Port: appPort}}
	for _, tc := range []struct {
		exps  []jam.Exposure
		name  string
		port  int
		right collab.Right
		ok    bool
	}{
		{exps, "app", appPort, collab.Preview, true},
		{exps, "api", 0, "", false},
		{exps, "ide", 0, "", false},
		{nil, "ide", 0, "", false},
		{append(exps, jam.Exposure{Name: "ide", Port: 8443}), "ide", 8443, collab.Preview, true},
	} {
		port, right, ok := previewPortAndRight(tc.exps, tc.name)
		if port != tc.port || right != tc.right || ok != tc.ok {
			t.Errorf("%s in %v: %d %q %v, want %d %q %v", tc.name, tc.exps, port, right, ok, tc.port, tc.right, tc.ok)
		}
	}
}
