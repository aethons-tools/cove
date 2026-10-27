package collabhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

const ideHost = preview.IDEName + "--" + label26 + "." + testDomain

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

// With no exposure named ide, ide--<label> reaches the built-in IDE's port
// for a caller with the ssh right, and audits it under ssh.
func TestPreviewIDEBuiltInUnderSSH(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.grantSSH(t)
	rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "carol"))
	if rec.Code != 200 || rec.Body.String() != "hello from the app" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if got := ph.tun.opened(); len(got) != 1 || got[0] != previewSession+":13337" {
		t.Fatalf("opened %v, want the built-in IDE port", got)
	}
	logged := ph.logged()
	if !strings.Contains(logged, `msg="preview opened"`) || !strings.Contains(logged, "name=ide") || !strings.Contains(logged, "right=ssh") {
		t.Fatalf("audit: %s", logged)
	}
	l := ph.live.List(previewSession)
	if len(l) != 1 || l[0].Right != "ssh" {
		t.Fatalf("List = %+v", l)
	}
}

// A preview-only caller (a member under preview: members) may not use the IDE.
func TestPreviewIDERefusesPreviewOnly(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "alice"))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "you may not use ssh on this studio" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("opened %d tunnels", n)
	}
}

// A kit exposure named ide replaces the built-in IDE (still under ssh).
func TestPreviewIDEKitOverride(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.setExposures([]jam.Exposure{{Name: "app", Port: appPort}, {Name: preview.IDEName, Port: 8443}})
	ph.grantSSH(t)
	rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "carol"))
	if rec.Code != 200 {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if got := ph.tun.opened(); len(got) != 1 || got[0] != previewSession+":8443" {
		t.Fatalf("opened %v, want the kit's ide port", got)
	}
	// A preview-only caller is still refused the overridden IDE.
	if rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "alice")); rec.Code != http.StatusForbidden {
		t.Fatalf("alice on the kit ide: %d", rec.Code)
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

// The ssh right ending while the IDE's tunnel opens is a 403 naming ssh.
func TestPreviewIDEDialRecheckNamesSSH(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.grantSSH(t)
	ph.tun.onOpen = func() { _ = ph.st.RemoveShareGrant(previewSession, ph.ids["carol"]) }
	rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "carol"))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "you may not use ssh on this studio" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if n := len(ph.live.List(previewSession)); n != 0 {
		t.Fatalf("%d live entries remain", n)
	}
}

// The IDE's tunnel may wait for jam-studio-agent to start code-server (up to
// 15 s), so its open gets 30 s; an app's keeps openTimeout.
func TestPreviewIDEOpenTimeout(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	ph.grantSSH(t)
	if rec := ph.getAt(ideHost, "/", ph.loginAt(t, ideHost, "carol")); rec.Code != 200 {
		t.Fatalf("ide: %d %q", rec.Code, rec.Body)
	}
	if rec := ph.get("/", ph.login(t, "alice")); rec.Code != 200 {
		t.Fatalf("app: %d %q", rec.Code, rec.Body)
	}
	ph.tun.mu.Lock()
	budgets := append([]time.Duration(nil), ph.tun.budgets...)
	ph.tun.mu.Unlock()
	if len(budgets) != 2 {
		t.Fatalf("budgets %v", budgets)
	}
	if b := budgets[0]; b <= 25*time.Second || b > 30*time.Second {
		t.Errorf("ide open budget %v, want 30s", b)
	}
	if b := budgets[1]; b <= 10*time.Second || b > openTimeout {
		t.Errorf("app open budget %v, want %v", b, openTimeout)
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
		{exps, preview.IDEName, preview.IDEPort, collab.SSH, true},
		{nil, preview.IDEName, preview.IDEPort, collab.SSH, true},
		{append(exps, jam.Exposure{Name: preview.IDEName, Port: 8443}), preview.IDEName, 8443, collab.SSH, true},
	} {
		port, right, ok := previewPortAndRight(tc.exps, tc.name)
		if port != tc.port || right != tc.right || ok != tc.ok {
			t.Errorf("%s in %v: %d %q %v, want %d %q %v", tc.name, tc.exps, port, right, ok, tc.port, tc.right, tc.ok)
		}
	}
}
