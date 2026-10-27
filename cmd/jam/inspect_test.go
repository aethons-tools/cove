package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func inspectServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.PutRole(jam.DefaultProject, jam.Role{Name: "dev", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(st, placeholderLauncher{}, "h", time.Minute, 30*time.Second, nil, log)
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "s1", Project: jam.DefaultProject, Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	d := jam.SetupDeps{Store: st, Sup: sup, SealedEgress: []string{".github.com"}}
	ts := httptest.NewServer(jam.NewAdminHandler(st, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, jam.WithSetupInspect(d, log)))
	t.Cleanup(ts.Close)
	return ts
}

func TestRoleAndStudioInspect(t *testing.T) {
	ts := inspectServer(t)
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	run1 := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(args, getenv, &out, &errb)
	}
	if code := run1("role", "inspect", "default/dev", "--admin-url", ts.URL, "--section", "egress", "--section", "policy"); code != 0 {
		t.Fatalf("role inspect = %d: %s", code, errb.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "# role default/dev\n") || !strings.Contains(got, "\n== egress (config)\n") || !strings.Contains(got, "\n== policy (config)\n") || strings.Contains(got, "== kit") {
		t.Fatalf("role inspect output:\n%s", got)
	}
	if code := run1("studio", "inspect", "s1", "--admin-url", ts.URL, "--diff", "--json", "--section", "egress"); code != 0 {
		t.Fatalf("studio inspect = %d: %s", code, errb.String())
	}
	var s jam.StudioSetup
	if err := json.Unmarshal(out.Bytes(), &s); err != nil || s.Subject != "studio s1" || len(s.Sections) != 1 || !s.Sections[0].Diffed {
		t.Fatalf("--json = %+v, %v\n%s", s, err, out.String())
	}
	if code := run1("role", "inspect", "dev", "--admin-url", ts.URL, "--section", "egress"); code != 0 || !strings.HasPrefix(out.String(), "# role default/dev") {
		t.Fatalf("a bare role means the default project: %d %s", code, out.String())
	}
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"role", "inspect", "--admin-url", ts.URL}, 2},
		{[]string{"role", "inspect", "default/dev", "--admin-url", ts.URL, "--section", "bogus"}, 2},
		{[]string{"studio", "inspect", "--admin-url", ts.URL}, 2},
		{[]string{"studio", "inspect", "nope", "--admin-url", ts.URL}, 1},
		{[]string{"role", "inspect", "default/nope", "--admin-url", ts.URL}, 1},
	} {
		if code := run1(c.args...); code != c.code {
			t.Errorf("%v = %d (%s), want %d", c.args, code, errb.String(), c.code)
		}
	}
}
