package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// shareJam serves the real collaboration routes on /admin for operator
// "local", over a store with live studio ses_a and user carol.
func shareJam(t *testing.T) (*httptest.Server, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "default", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(jam.User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(collabhttp.Routes("/admin", collabhttp.Deps{
		Store: st, CA: ca, Tunnels: echoTunnels{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Principal: func(*http.Request) (collab.Principal, string, error) {
			return collab.Principal{Operator: "local"}, "op", nil
		},
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestStudioShareUnshareAccess(t *testing.T) {
	sshTestHome(t)
	srv, st := shareJam(t)
	code, out, errs := runCLI("studio", "share", "--operator", "--admin-url", srv.URL, "ses_a", "--with", "user:carol", "--rights", "preview,ssh", "--for", "4h")
	if code != 0 || !strings.Contains(out, "shared ses_a with carol: preview,ssh until ") {
		t.Fatalf("share: %d %q %q", code, out, errs)
	}
	if g := st.ShareGrants("ses_a"); len(g) != 1 || len(g[0].Rights) != 2 {
		t.Fatalf("grants %+v", g)
	}
	code, out, errs = runCLI("studio", "access", "--operator", "--admin-url", srv.URL, "ses_a")
	if code != 0 {
		t.Fatalf("access: %d %q", code, errs)
	}
	for _, want := range []string{"operators\talways", "role default/ic\tpreview=members\tssh=none", "grant\tcarol\tpreview,ssh\tuntil ", "by=op:local", "live\tnone"} {
		if !strings.Contains(out, want) {
			t.Errorf("access output lacks %q:\n%s", want, out)
		}
	}
	code, out, errs = runCLI("studio", "share", "--operator", "--admin-url", srv.URL, "ses_a", "--with", "user:carol", "--rights", "ssh", "--for", "8d")
	if code != 1 || !strings.Contains(errs, "longer than 168h0m0s") {
		t.Fatalf("over-long share: %d %q %q", code, out, errs)
	}
	code, out, errs = runCLI("studio", "unshare", "--operator", "--admin-url", srv.URL, "ses_a", "--with", "user:carol")
	if code != 0 || !strings.Contains(out, "unshared ses_a from carol") || len(st.ShareGrants("ses_a")) != 0 {
		t.Fatalf("unshare: %d %q %q", code, out, errs)
	}
}

func TestStudioShareUsage(t *testing.T) {
	sshTestHome(t)
	for _, args := range [][]string{
		{"studio", "share", "ses_a", "--rights", "ssh"},                    // no --with
		{"studio", "share", "ses_a", "--with", "user:carol"},               // no --rights
		{"studio", "share", "--with", "user:carol", "--rights", "ssh"},     // no studio
		{"studio", "unshare", "ses_a"},                                     // no --with
		{"studio", "access"},                                               // no studio
		{"studio", "access", "--participant", "--operator", "ses_a"},       // both planes
		{"studio", "share", "ses_a", "--with", "carol", "--rights", "ssh"}, // not user:<name>
		{"studio", "access", "bad/id"},                                     // unsafe id
	} {
		if code, _, errs := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d (%s)", args, code, errs)
		}
	}
}
