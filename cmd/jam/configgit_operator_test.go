package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// operatorDestinations is an operator's NEA destinations as `destination
// import` reads them: GitLab API with custom Private-Token specs both ways,
// GitLab git with a custom inbound (prefixes, explicit raw encoding),
// basic-password out and git: true.
const operatorDestinations = `destinations:
  - name: nea-gitlab-api
    route: /nea-gitlab-api/
    upstream: https://gitlab.nea.example/api/v4
    identity_in: custom
    identity_in_spec: {header: Private-Token}
    cred_name: gitlab-pat
    apply: custom
    apply_spec: {header: Private-Token, template: "{cred}"}
    env: {GITLAB_HOST: "{host}", GITLAB_TOKEN: "{token}"}
    log_headers_in: examined
    log_headers_out: changed
  - name: nea-gitlab-git
    route: /nea-gitlab-git/
    upstream: https://gitlab.nea.example
    identity_in: custom
    identity_in_spec: {header: Authorization, prefixes: ["Bearer ", "token "], encoding: raw}
    cred_name: gitlab-pat
    apply: basic-password
    git: true
    log_headers_in: all
    log_headers_out: all
`

// TestConfigWriteAllAdoptsOperatorJam: the objects an operator reported as
// "exists and is not managed", created the way jam and the admin API
// create them — basic-password presets with env and header logging, custom
// GitLab specs and git: true, a project whose chat service was set by
// service kind (creating the connection), with a resource and a member whose
// discord delivery is bound to a linked account — written by
// `config write --all` and planned back by `config diff --dir`: every
// object adopts, and the repo files are the operator's bytes.
func TestConfigWriteAllAdoptsOperatorJam(t *testing.T) {
	url, st := configServer(t)
	if code, out, errs := runJam("destination", "add", "--name", "nea-atlassian", "--route", "/nea-atlassian/",
		"--upstream", "https://nexteraanalytics.atlassian.net", "--identity-in", "basic-password", "--cred-name", "atlassian-agent-apikey",
		"--apply", "basic-password", "--env", "JIRA_API_TOKEN={token}", "--log-headers-in", "all", "--log-headers-out", "all",
		"--admin-url", url); code != 0 {
		t.Fatalf("destination add: %d %s %s", code, out, errs)
	}
	imp := filepath.Join(t.TempDir(), "dests.yml")
	mustWrite(t, imp, operatorDestinations, 0o644)
	if code, out, errs := runJam("destination", "import", imp, "--admin-url", url); code != 0 {
		t.Fatalf("destination import: %d %s %s", code, out, errs)
	}
	brent, err := st.CreateUser(jam.User{ID: ident.New(ident.User), Name: "Brent", Status: jam.StatusLive})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject("Conduit"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatService("Conduit", "discord"); err != nil { // by kind: creates connection discord
		t.Fatal(err)
	}
	p, _ := st.GetProject("Conduit")
	if err := st.PutMembership(jam.Membership{ProjectID: p.ID, UserID: brent.ID,
		Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "cove-harbor", UserID: "123456789012345678"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectContext("Conduit", sessionctx.Layer{}, []sessionctx.Resource{
		{Name: "Conduit", Kind: "repo", Ref: "nuclear/nuclear-nexus/nexus", Note: "Formerly known as Nexus; NEA GitLab"}}); err != nil {
		t.Fatal(err)
	}

	dir := gitDir(t)
	if code, out, errs := runJam("config", "write", dir, "--all", "--admin-url", url); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	for p, want := range map[string]string{
		"destinations/nea-atlassian.yml": "kind: destination\nversion: 1\nroute: /nea-atlassian/\nupstream: https://nexteraanalytics.atlassian.net\n" +
			"identity_in: basic-password\ncred_name: atlassian-agent-apikey\napply: basic-password\nenv:\n  JIRA_API_TOKEN: '{token}'\n" +
			"log_headers_in: all\nlog_headers_out: all\n",
		"projects/Conduit/project.yml": "kind: project\nversion: 1\nchat-service: discord\nresources:\n  - name: Conduit\n    kind: repo\n" +
			"    ref: nuclear/nuclear-nexus/nexus\n    note: Formerly known as Nexus; NEA GitLab\nmembers:\n  - user: Brent\n    delivery:\n" +
			"      - service: discord\n        address: cove-harbor\n",
	} {
		if b, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q (%v), want the operator's bytes %q", p, b, err, want)
		}
	}
	code, out, errs := runJam("config", "diff", "--dir", dir, "--admin-url", url)
	if code != 0 {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	for _, obj := range []string{"destination nea-atlassian", "destination nea-gitlab-api", "destination nea-gitlab-git",
		"user Brent", "connection discord", "project Conduit"} {
		if !strings.Contains(out, "adopt  "+obj+"\n") {
			t.Errorf("%s not adopted:\n%s", obj, out)
		}
	}
	if strings.Contains(out, "not managed") || strings.Contains(out, "refused") {
		t.Errorf("refused:\n%s", out)
	}
}
