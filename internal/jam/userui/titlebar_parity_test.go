package userui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

var titlebarRe = regexp.MustCompile(`(?s)<header class="app-titlebar">.*?</header>`)

// The Inbox and the console render the same title bar for one signed-in
// member+admin: identical apart from which tab is current.
func TestTitlebarIdenticalAcrossInboxAndConsole(t *testing.T) {
	e, p := fixture()
	principal := appui.Principal{User: &p, Admin: true, Operator: jam.Operator{ID: "op"}}
	shell := appui.Shell{Brand: "Acme Jam", SignOut: true, AdminBadges: func(*http.Request) map[string]appui.Badge {
		return map[string]appui.Badge{"health": {N: 2, Red: true, Title: "2 red"}}
	}}
	serve := func(h http.Handler, path string) string {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, appui.WithPrincipal(jam.WithParticipant(r, p), principal))
		})
		rec := httptest.NewRecorder()
		appui.WithShell(inner, shell).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		bar := titlebarRe.FindString(rec.Body.String())
		if bar == "" {
			t.Fatalf("%s: no title bar", path)
		}
		return bar
	}
	inbox := serve(Handler(e.Deps, nil), "/app/user/")
	console := serve(adminui.Handler(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil), "/app/admin/agents")

	cur := regexp.MustCompile(` aria-current="page"`)
	if strings.Count(inbox, "aria-current") != 1 || strings.Count(console, "aria-current") != 1 {
		t.Fatalf("each bar should have one current tab:\n%s\n%s", inbox, console)
	}
	if !strings.Contains(inbox, `aria-current="page">Inbox`) || !strings.Contains(console, `href="/app/admin/agents" aria-current="page">Agents`) {
		t.Error("wrong current tab")
	}
	if a, b := cur.ReplaceAllString(inbox, ""), cur.ReplaceAllString(console, ""); a != b {
		t.Errorf("title bars differ:\n%s\n---\n%s", a, b)
	}
	for _, want := range []string{"Acme Jam", `href="/app/auth/logout">Sign out`, `class="badge red"`} {
		if !strings.Contains(inbox, want) {
			t.Errorf("title bar missing %q", want)
		}
	}
}
