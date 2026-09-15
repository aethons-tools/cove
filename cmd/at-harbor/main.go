// Command at-harbor is the central credential broker + control plane. `serve`
// runs the credential-injecting reverse proxy and a loopback admin API;
// `enroll`/`revoke`/`destination` are admin-API clients. See the harbor specs
// under docs/superpowers/specs/.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/dispatcher"
	"github.com/aethons-tools/cove/internal/escalate"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminclient"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
	"github.com/aethons-tools/cove/internal/harbor/attach"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
	"github.com/aethons-tools/cove/internal/harbor/browserauth"
	"github.com/aethons-tools/cove/internal/harbor/deviceflow"
	"github.com/aethons-tools/cove/internal/harbor/launcher"
	"github.com/aethons-tools/cove/internal/install"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msglog/msglogpg"
	"github.com/aethons-tools/cove/internal/msgport"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/wakeon"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const defaultAdminURL = "http://127.0.0.1:8081"

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-harbor",
		Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the broker + loopback admin API", Run: cmdServe},
			{Name: "enroll", Brief: "enroll an identity (via the admin API) and print its snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "revoke an identity (via the admin API)", Run: cmdRevoke},
			{Name: "destination", Brief: "manage destinations (add|list|rm|import) via the admin API", Run: cmdDestination},
			{Name: "role", Brief: "manage roles (add|list|rm) via the admin API", Run: cmdRole},
			{Name: "project", Brief: "manage a project's roster (roster add-human|add-channel|list|rm-human|rm-channel) or escalation policy (escalation set|list|clear) via the admin API", Run: cmdProject},
			{Name: "kit", Brief: "manage the kit registry (push|list|show|versions|pin|rm)", Run: cmdKit},
			{Name: "grant", Brief: "grant a role to an actor", Run: cmdGrant},
			{Name: "ungrant", Brief: "remove a role grant from an actor", Run: cmdUngrant},
			{Name: "roster", Brief: "list actors and their grants", Run: cmdRoster},
			{Name: "cove", Brief: "manage managed coves (raise|list|status|teardown) via the admin API", Run: cmdCove},
			{Name: "login", Brief: "sign in via OIDC device flow and cache the operator token", Run: cmdLogin},
			{Name: "logout", Brief: "clear the cached operator token", Run: cmdLogout},
			{Name: "whoami", Brief: "show the cached operator identity", Run: cmdWhoami},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdLogin(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile (from ~/.config/at-harbor/settings.yml)")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL; persisted to the app's settings when given")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor login: unexpected arguments")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 2
	}
	settings := loadSettings(*app)
	adminURL := firstNonEmpty(*adminURLFlag, settings.AdminURL, defaultAdminURL)
	// Persist an explicitly-given --admin-url into the app's settings so future
	// commands for this app don't need the flag.
	if *adminURLFlag != "" && *adminURLFlag != settings.AdminURL {
		s := settings
		s.AdminURL = *adminURLFlag
		if err := saveSettings(*app, s); err != nil {
			fmt.Fprintln(stderr, "at-harbor login: could not save settings:", err)
			return 1
		}
	}
	lc, err := adminclient.New(adminURL, "").LoginConfig()
	if err != nil {
		if errors.Is(err, adminclient.ErrNotFound) {
			fmt.Fprintln(stdout, "this harbor is not OIDC-gated; no login needed")
			return 0
		}
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	ctx := context.Background()
	dc, err := deviceflow.RequestDeviceCode(ctx, http.DefaultClient, deviceflow.Config{
		Issuer: lc.Issuer, Audience: lc.Audience, ClientID: lc.ClientID, Scope: lc.Scope,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	target := firstNonEmpty(dc.VerificationURIComplete, dc.VerificationURI)
	fmt.Fprintf(stdout, "To sign in, open:\n  %s\nand confirm the code: %s\n", target, dc.UserCode)
	// Bound polling by the device code's own lifetime so a wedged/misbehaving IdP
	// can't make login hang forever.
	if dc.ExpiresIn > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(dc.ExpiresIn)*time.Second)
		defer cancel()
	}
	tok, err := deviceflow.PollToken(ctx, http.DefaultClient, time.Sleep, dc.TokenEndpoint, lc.ClientID, dc.DeviceCode, dc.Interval)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("login timed out; run `at-harbor login` again")
		}
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	sub, exp, _ := parseJWTClaims(tok.AccessToken)
	if exp.IsZero() && tok.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if err := saveToken(*app, cachedToken{AccessToken: tok.AccessToken, Sub: sub, Expiry: exp, AdminURL: adminURL}); err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s; token expires %s\n", sub, exp.Format(time.RFC3339))
	return 0
}

func cmdLogout(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	if _, code, ok := cli.ParseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor logout:", err)
		return 2
	}
	if err := clearToken(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "logged out")
	return 0
}

func cmdWhoami(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	if _, code, ok := cli.ParseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor whoami:", err)
		return 2
	}
	if t, ok := loadToken(*app); ok {
		fmt.Fprintf(stdout, "%s @ %s (expires %s)\n", t.Sub, t.AdminURL, t.Expiry.Format(time.RFC3339))
		return 0
	}
	if _, err := os.Stat(tokenPath(*app)); err == nil {
		fmt.Fprintln(stdout, "session expired; run `at-harbor login`")
	} else {
		fmt.Fprintln(stdout, "not logged in")
	}
	return 0
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	baseURLFlag := fs.String("base-url", "", "harbor broker base URL for the printed snippet (overrides the app's settings)")
	jsonOut := fs.Bool("json", false, `print {"id","token"} JSON instead of the shell snippet (base-url not required)`)
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor enroll:", err)
		return 2
	}
	settings := loadSettings(*app)
	adminURL := firstNonEmpty(*adminURLFlag, settings.AdminURL, defaultAdminURL)
	baseURL := firstNonEmpty(*baseURLFlag, settings.BaseURL)
	// --base-url is only needed for the printed snippet; --json omits it.
	if len(pos) > 0 || *id == "" || (!*jsonOut && baseURL == "") {
		fmt.Fprintln(stderr, "at-harbor enroll: --id (and --base-url unless --json) are required")
		return 2
	}
	res, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if *jsonOut {
		// Machine-readable output for at-cove auto-enrollment (COV-141). The token
		// is on stdout only — the caller captures it in memory, never argv/logs.
		_ = json.NewEncoder(stdout).Encode(struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}{res.ID, res.Token})
		return 0
	}
	fmt.Fprint(stdout, harbor.RenderEnrollSnippet(baseURL, res.Token))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor revoke:", err)
		return 2
	}
	if len(pos) > 0 || *id == "" {
		fmt.Fprintln(stderr, "at-harbor revoke: --id is required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	if err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Revoke(*id); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
	return 0
}

func cmdDestination(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor destination: expected add|list|rm|import")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("destination "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
	// add flags
	var d harbor.Destination
	fs.StringVar(&d.Name, "name", "", "destination name")
	fs.StringVar(&d.Route, "route", "", "inbound path prefix, e.g. /git/")
	fs.StringVar(&d.Upstream, "upstream", "", "upstream base URL")
	var identityIn, apply string
	fs.StringVar(&identityIn, "identity-in", "", "bearer|basic-password|x-api-key")
	fs.StringVar(&d.CredName, "cred-name", "", "credential name to inject")
	fs.StringVar(&apply, "apply", "", "bearer|basic-password|x-api-key")
	fs.BoolVar(&d.RepoScoped, "repo-scoped", false, "path is <route>/<owner>/<repo>/…")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor destination:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		d.IdentityIn, d.Apply = harbor.ApplyMethod(identityIn), harbor.ApplyMethod(apply)
		if err := c.AddDestination(d); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added destination", d.Name)
	case "list":
		ds, err := c.ListDestinations()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, dd := range ds {
			fmt.Fprintf(stdout, "%s\t%s\t-> %s\t(cred %q, %s)\n", dd.Name, dd.Route, dd.Upstream, dd.CredName, dd.Apply)
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor destination rm: expected one destination name")
			return 2
		}
		if err := c.RemoveDestination(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed destination", pos[0])
	case "import":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor destination import: expected one YAML file path")
			return 2
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		var conf harbor.Config
		if err := yaml.Unmarshal(data, &conf); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, dd := range conf.Destinations {
			if err := c.AddDestination(dd); err != nil {
				fmt.Fprintln(stderr, "at-harbor:", err)
				return 1
			}
			fmt.Fprintln(stdout, "imported destination", dd.Name)
		}
	default:
		fmt.Fprintln(stderr, "at-harbor destination: unknown subcommand", sub)
		return 2
	}
	return 0
}

func cmdRole(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor role: expected add|list|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("role "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	name := fs.String("name", "", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	addressing := fs.String("addressing", "", "comma-separated comms target globs, e.g. human:*,channel:eng-help")
	ttl := fs.Duration("ttl", 0, "default token lifetime for actors of this role (0 = no expiry)")
	kitName := fs.String("kit", "", "bind a registered kit (name)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor role:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "at-harbor role add: --name is required")
			return 2
		}
		r := harbor.Role{Name: *name, Kit: *kitName, Scope: harbor.Scope{Destinations: splitCSV(*dests), Repos: splitCSV(*repos), Addressing: splitCSV(*addressing), TTL: *ttl}}
		if err := c.PutRole(*project, r); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added role", firstNonEmpty(*project, harbor.DefaultProject)+"/"+*name)
	case "list":
		roles, err := c.ListRoles(*project)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, r := range roles {
			fmt.Fprintf(stdout, "%s\tdests=%s\trepos=%s\taddressing=%s\tttl=%s\n", r.Name, strings.Join(r.Scope.Destinations, ","), strings.Join(r.Scope.Repos, ","), strings.Join(r.Scope.Addressing, ","), r.Scope.TTL)
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor role rm: expected one role name")
			return 2
		}
		if err := c.RemoveRole(firstNonEmpty(*project, harbor.DefaultProject), pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed role", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor role: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProject manages a project's roster (humans + channels) or escalation
// policy via the admin API. Roster subcommands nest under "roster":
// `project roster add-human|add-channel|list|rm-human|rm-channel`. Escalation
// subcommands nest under "escalation" and are handled by
// cmdProjectEscalation: `project escalation set|list|clear`.
func cmdProject(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) >= 1 && args[0] == "escalation" {
		return cmdProjectEscalation(args[1:], stdout, stderr)
	}
	if len(args) < 2 || args[0] != "roster" {
		fmt.Fprintln(stderr, "at-harbor project: expected roster add-human|add-channel|list|rm-human|rm-channel or escalation set|list|clear")
		return 2
	}
	sub, rest := args[1], args[2:]
	fs := flag.NewFlagSet("project roster "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	name := fs.String("name", "", "roster-local name (add-human|add-channel)")
	handle := fs.String("handle", "", "tracker @-mention handle (add-human)")
	ref := fs.String("ref", "", "tracker issue identifier the channel posts to (add-channel)")
	service := fs.String("service", "linear", "channel service (add-channel)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor project:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add-human":
		if len(pos) != 1 || *name == "" || *handle == "" {
			fmt.Fprintln(stderr, "at-harbor project roster add-human: expected <project> --name and --handle")
			return 2
		}
		if err := c.AddHuman(pos[0], harbor.Human{Name: *name, Handle: *handle}); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added human", *name, "to", pos[0])
	case "add-channel":
		if len(pos) != 1 || *name == "" || *ref == "" {
			fmt.Fprintln(stderr, "at-harbor project roster add-channel: expected <project> --name and --ref")
			return 2
		}
		if err := c.AddChannel(pos[0], harbor.Channel{Name: *name, Service: *service, Ref: *ref}); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added channel", *name, "to", pos[0])
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor project roster list: expected one project name")
			return 2
		}
		rr, err := c.GetRoster(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, h := range rr.Humans {
			fmt.Fprintf(stdout, "human\t%s\thandle=%s\n", h.Name, h.Handle)
		}
		for _, ch := range rr.Channels {
			fmt.Fprintf(stdout, "channel\t%s\tservice=%s\tref=%s\n", ch.Name, ch.Service, ch.Ref)
		}
	case "rm-human":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-harbor project roster rm-human: expected <project> <name>")
			return 2
		}
		if err := c.RemoveHuman(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed human", pos[1], "from", pos[0])
	case "rm-channel":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-harbor project roster rm-channel: expected <project> <name>")
			return 2
		}
		if err := c.RemoveChannel(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed channel", pos[1], "from", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor project roster: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProjectEscalation manages a project's escalation policy via the admin
// API: `project escalation set|list|clear`.
func cmdProjectEscalation(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-harbor project escalation: expected set|list|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project escalation "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	category := fs.String("category", "", "escalation category (default chain when empty); set/clear")
	var tiers tierFlags
	fs.Var(&tiers, "tier", "a tier as 'target,target@timeout' (repeatable, ordered); set only")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor project escalation:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "set":
		if len(pos) != 1 || len(tiers) == 0 {
			fmt.Fprintln(stderr, "at-harbor project escalation set: expected <project> and at least one --tier 'targets@timeout'")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], *category, tiers); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set escalation policy for", pos[0], "-", len(tiers), "tier(s)")
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor project escalation list: expected one project name")
			return 2
		}
		v, err := c.GetEscalationPolicy(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for i, tr := range v.Default {
			fmt.Fprintf(stdout, "default\ttier %d\t%s\ttimeout=%s\n", i, strings.Join(tr.Targets, ","), tr.Timeout)
		}
		categories := make([]string, 0, len(v.ByCategory))
		for cat := range v.ByCategory {
			categories = append(categories, cat)
		}
		sort.Strings(categories)
		for _, cat := range categories {
			for i, tr := range v.ByCategory[cat] {
				fmt.Fprintf(stdout, "%s\ttier %d\t%s\ttimeout=%s\n", cat, i, strings.Join(tr.Targets, ","), tr.Timeout)
			}
		}
	case "clear":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor project escalation clear: expected one project name")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], *category, nil); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared escalation policy for", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor project escalation: unknown subcommand", sub)
		return 2
	}
	return 0
}

// tierFlags collects repeatable --tier values, parsing 'targets@timeout'.
type tierFlags []harbor.EscalationTier

func (t *tierFlags) String() string { return fmt.Sprintf("%d tiers", len(*t)) }
func (t *tierFlags) Set(v string) error {
	at := strings.LastIndex(v, "@")
	if at < 0 {
		return fmt.Errorf("tier %q missing '@timeout'", v)
	}
	d, err := time.ParseDuration(v[at+1:])
	if err != nil {
		return fmt.Errorf("tier %q: bad timeout: %w", v, err)
	}
	targets := strings.Split(v[:at], ",")
	*t = append(*t, harbor.EscalationTier{Targets: targets, Timeout: d})
	return nil
}

func cmdKit(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor kit: expected push|list|show|versions|pin|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kit "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	name := fs.String("name", "", "kit name")
	config := fs.String("config", "", "path to the kit config.yml (or - for stdin); push only")
	version := fs.Int("version", 0, "kit version (show; 0 = current)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor kit:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "push":
		if *name == "" || *config == "" {
			fmt.Fprintln(stderr, "at-harbor kit push: --name and --config are required")
			return 2
		}
		data, err := readConfig(*config) // file path or "-" for stdin
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		if _, err := kit.ParseConfig(data); err != nil {
			fmt.Fprintln(stderr, "at-harbor kit push: invalid kit config:", err)
			return 1
		}
		v, err := c.PushKit(*name, string(data))
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "pushed %s v%d\n", *name, v)
	case "list":
		kits, err := c.ListKits()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, k := range kits {
			fmt.Fprintf(stdout, "%s\tcurrent=v%d\tversions=%d\n", k.Name, k.Current, k.Versions)
		}
	case "show":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit show: expected one kit name")
			return 2
		}
		res, err := c.GetKit(pos[0], *version)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprint(stdout, res.Config)
	case "versions":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit versions: expected one kit name")
			return 2
		}
		vers, err := c.KitVersions(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, v := range vers {
			fmt.Fprintf(stdout, "v%d\n", v)
		}
	case "pin":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-harbor kit pin: expected <name> <version>")
			return 2
		}
		v, err := strconv.Atoi(pos[1])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor kit pin: version must be an integer")
			return 2
		}
		if err := c.PinKit(pos[0], v); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "pinned %s to v%d\n", pos[0], v)
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit rm: expected one kit name")
			return 2
		}
		if err := c.RemoveKit(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed kit", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor kit: unknown subcommand", sub)
		return 2
	}
	return 0
}

// readConfig reads a config file path, or stdin when path == "-".
func readConfig(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func cmdCove(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor cove: expected raise|list|status|teardown")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("cove "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "cove/actor id")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	role := fs.String("role", "", "role to raise the cove for")
	unit := fs.String("unit", "", "unit of work (e.g. issue identifier)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the workload prompt (raise only; read host-side, never passed on argv)")
	activity := fs.String("activity", "", "reported activity: running|waiting|blocked|done (status only)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor cove:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "raise":
		if *id == "" || *role == "" {
			fmt.Fprintln(stderr, "at-harbor cove raise: --id and --role are required")
			return 2
		}
		var prompt string
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor cove raise: --prompt-file:", err)
				return 1
			}
			prompt = string(b)
		}
		res, err := c.RaiseCove(adminclient.CoveRaiseParams{ID: *id, Project: *project, Role: *role, Unit: *unit, Prompt: prompt})
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "raised %s (phase=%s)\n", res.ID, res.Phase)
	case "list":
		coves, err := c.ListCoves()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, cv := range coves {
			fmt.Fprintf(stdout, "%s\trole=%s\tunit=%s\tphase=%s\tactivity=%s\tholder=%s\n",
				cv.ID, cv.Role, cv.Unit, cv.Phase, cv.Activity, cv.LeaseHolder)
		}
	case "status":
		if *id == "" || *activity == "" {
			fmt.Fprintln(stderr, "at-harbor cove status: --id and --activity are required")
			return 2
		}
		if err := c.ReportCoveStatus(*id, *activity); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "reported %s activity=%s\n", *id, *activity)
	case "teardown":
		name := *id
		if name == "" && len(pos) == 1 {
			name = pos[0]
		}
		if name == "" {
			fmt.Fprintln(stderr, "at-harbor cove teardown: --id (or a positional id) is required")
			return 2
		}
		if err := c.TeardownCove(name); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "tore down", name)
	default:
		fmt.Fprintln(stderr, "at-harbor cove: unknown subcommand", sub)
		return 2
	}
	return 0
}

func cmdGrant(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	return grantCommon(args, stdout, stderr, false)
}
func cmdUngrant(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	return grantCommon(args, stdout, stderr, true)
}
func grantCommon(args []string, stdout, stderr io.Writer, remove bool) int {
	verb := "grant"
	if remove {
		verb = "ungrant"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "actor id")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	role := fs.String("role", "", "role name")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor "+verb+":", err)
		return 2
	}
	if len(pos) > 0 || *id == "" || *role == "" {
		fmt.Fprintln(stderr, "at-harbor "+verb+": --id and --role are required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	var err error
	if remove {
		err = c.RemoveGrant(*id, firstNonEmpty(*project, harbor.DefaultProject), *role)
	} else {
		err = c.AddGrant(*id, harbor.Grant{Project: *project, Role: *role})
	}
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, verb+"ed", *role, "to", *id)
	return 0
}

func cmdRoster(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("roster", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor roster:", err)
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor roster: takes no positional arguments")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	roster, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Roster()
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	for _, a := range roster {
		for _, g := range a.Grants {
			fmt.Fprintf(stdout, "%s\t%s/%s\tdests=%s\trepos=%s\n", a.ID, g.Project, g.Role, strings.Join(g.Destinations, ","), strings.Join(g.Repos, ","))
		}
	}
	return 0
}

// placeholderLauncher satisfies harbor.Launcher without a real backend: it
// records a synthetic location and always probes Alive, so the supervisor spine
// (registry, leases, reconciler, restart re-adoption) runs end-to-end against a
// live `at-harbor serve`. `cove raise` against it creates a Live Instance with no
// actual cove. The real backend+kit launcher lands in a later slice.
type placeholderLauncher struct{}

func (placeholderLauncher) Raise(_ context.Context, spec harbor.RaiseSpec, _ harbor.LaunchCreds) (string, error) {
	return "placeholder:" + spec.ActorID, nil
}
func (placeholderLauncher) Teardown(context.Context, harbor.Instance) error { return nil }
func (placeholderLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}
func (placeholderLauncher) Pause(context.Context, harbor.Instance) error   { return nil }
func (placeholderLauncher) Unpause(context.Context, harbor.Instance) error { return nil }

// linearCommenter adapts *linear.Client to harbor.Commenter. It exists here,
// rather than in internal/harbor, so harbor core never imports
// internal/dispatch/linear or internal/dispatch/scheduler (see AGENTS.md
// boundary rules): the concrete tracker type and its scheduler.Comment shape
// are wiring-layer concerns.
type linearCommenter struct{ c *linear.Client }

func (l linearCommenter) IssueByIdentifier(ctx context.Context, identifier string) (string, error) {
	return l.c.IssueByIdentifier(ctx, identifier)
}

func (l linearCommenter) PostComment(ctx context.Context, issueID, body string) error {
	return l.c.PostComment(ctx, issueID, body)
}

func (l linearCommenter) Comments(ctx context.Context, issueID string) ([]harbor.Comment, error) {
	var cs []scheduler.Comment
	cs, err := l.c.Comments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]harbor.Comment, 0, len(cs))
	for _, c := range cs {
		// scheduler.Comment carries only Author/Body; ID/At are left zero
		// (harbor.Comment documents them as best-effort).
		out = append(out, harbor.Comment{Author: c.Author, Body: c.Body})
	}
	return out, nil
}

func cmdServe(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to the serve config YAML")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *cfgPath == "" {
		fmt.Fprintln(stderr, "at-harbor serve: --config is required")
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	cfg, err := parseServeConfig(data)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if unknown := unknownServeKeys(data); len(unknown) > 0 {
		fmt.Fprintf(stderr, "at-harbor: warning: ignoring unknown harbor.yml key(s): %s\n", strings.Join(unknown, ", "))
		for _, k := range unknown {
			if k == "destinations" {
				fmt.Fprintln(stderr, "at-harbor: note: destinations are managed via the admin API — run `at-harbor destination import`")
			}
		}
	}
	if err := cfg.validateAdminExposure(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if err := cfg.validateLauncher(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if err := cfg.validateDispatcher(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if err := cfg.validateStorePostgres(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	specs := cfg.credSpecs()

	// Select the store backend. store-postgres wins when set; otherwise the file
	// store. The DB password is resolved on the host in memory and assembled into
	// the DSN — never written to disk/argv, never logged.
	var st harbor.Store
	var pgPool *pgxpool.Pool // non-nil ⇒ Postgres backend; shared with the message log
	if pc := cfg.StorePostgres; pc != nil {
		resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[pc.PasswordCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: store-postgres password:", err)
			return 1
		}
		dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
			pc.Host, pc.Port, pc.Database, pc.User, resolved[pc.PasswordCred], pc.SSLMode)
		ps, err := harbor.NewPostgresStore(context.Background(), dsn, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: store-postgres:", err)
			return 1
		}
		defer ps.Close()
		st = ps
		pgPool = ps.Pool()
		log.Info("harbor store: postgres", "host", pc.Host, "database", pc.Database) // never the password
	} else {
		fs, err := harbor.NewFileStore(cfg.Store)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		st = fs
		log.Info("harbor store: file", "path", cfg.Store)
	}

	creds := harbor.NewSecretResolver(runner.OS{}, specs)
	broker := harbor.NewBroker(st, creds, log)

	ttl, reconcile, err := cfg.runtimeDurations()
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	var lch harbor.Launcher = placeholderLauncher{}
	if lc := cfg.Runtime.Launcher; lc != nil {
		var m install.Manifest
		b, err := os.ReadFile(lc.InstallManifest)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: launcher install-manifest:", err)
			return 1
		}
		if err := json.Unmarshal(b, &m); err != nil {
			fmt.Fprintln(stderr, "at-harbor: launcher install-manifest:", err)
			return 1
		}
		be, ok := colima.New(runner.OS{}).(launcher.Backend) // colima.New returns backend.Backend; *Colima also satisfies DispatchOps+GetStatus
		if !ok {
			fmt.Fprintln(stderr, "at-harbor: colima backend does not satisfy launcher.Backend")
			return 1
		}
		lch = launcher.New(launcher.Config{
			Ops: be, Runner: runner.OS{},
			Image: m.Image, ImageDigest: m.ImageDigest,
			HarborHost: lc.HarborHost, RuntimeAddr: lc.RuntimeAddr,
			IdentityFile: lc.IdentityFile, KnownHostsDir: lc.KnownHostsDir,
			DNS: lc.DNS, Docker: lc.Docker,
		})
		log.Info("harbor launcher: colima", "image", m.Image, "runtime-addr", lc.RuntimeAddr)
	}
	sup := harbor.NewSupervisor(st, lch, harbor.NewHolderID(), ttl, reconcile, time.Now, log)
	go sup.Run(context.Background())

	// Attach gRPC server: served on the cove-facing :443 mux below, and
	// optionally on a plaintext dev listener (runtime.listen). One server, one
	// ControlSink. Built here (ahead of the dispatcher block below) because the
	// resident wake-on engine needs rsrv as its Waker.
	rsrv := attach.NewServer(st, sup, log)
	sup.SetControlSink(rsrv)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, rsrv)

	// Message Log: opened once (handle held for the serve lifetime) and shared
	// between the /messages writer (dual-write shadow, below) and the admin UI's
	// read-only reader (further down). Backend follows the store backend:
	// Postgres (shared control-plane pool) when store-postgres is set, else the
	// file log at message-log. Unset config → nil → the writer's dual-write is
	// disabled and the admin view renders a "not configured" notice.
	var messageLog msglog.Store
	switch {
	case pgPool != nil:
		ml, err := msglogpg.New(context.Background(), pgPool, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log (postgres):", err)
			return 1
		}
		messageLog = ml // Close is a no-op; the store owns the pool
		log.Info("harbor message log: postgres (shared control-plane database)")
	case cfg.MessageLog != "":
		ml, err := msglog.Open(cfg.MessageLog, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log:", err)
			return 1
		}
		defer ml.Close()
		messageLog = ml
		log.Info("harbor message log: file", "path", cfg.MessageLog)
	}

	// httpHandler is the cove-facing HTTP handler mounted on the :443 mux below.
	// It defaults to the broker alone; when a tracker is configured it gains a
	// /messages route sharing the same *linear.Client as the resident
	// dispatcher (built once, used for both).
	var httpHandler http.Handler = broker
	if dc := cfg.Runtime.Dispatcher; dc != nil {
		// Resolve harbor's own tracker token (never injected into a cove, never logged).
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{dc.TrackerToken.toSpec("AT_DISPATCH_TRACKER_TOKEN")})
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: dispatcher tracker-token:", err)
			return 1
		}
		token := tokEnv["AT_DISPATCH_TRACKER_TOKEN"]
		// linear.New wants a full kit.Config; wrap the configured LinearTracker.
		kitShell := kit.Config{Tracker: &kit.Tracker{Linear: dc.Linear}}
		tracker, err := linear.New(kitShell, token, nil)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: dispatcher tracker:", err)
			return 1
		}
		poll, _ := time.ParseDuration(dc.PollInterval) // "" or invalid → 0 → dispatcher default
		disp := dispatcher.New(tracker, sup, st, dispatcher.Config{
			Role: dc.Role, Project: dc.Project, MaxConcurrent: dc.MaxConcurrent, PollInterval: poll,
		}, log)
		go disp.Run(context.Background())
		log.Info("harbor dispatcher: resident", "role", dc.Role, "max-concurrent", dc.MaxConcurrent)

		// Pass messageLog as the appender only when it's genuinely non-nil: it is
		// an msglog.Store interface value assigned only to a real backend (see
		// messageLog above) or left as a true nil interface, so this guard is a
		// plain nil check with no typed-nil hazard.
		var msgH *harbor.MessagesHandler
		if messageLog != nil {
			msgH = harbor.NewMessagesHandler(st, linearCommenter{tracker}, messageLog, log)
		} else {
			msgH = harbor.NewMessagesHandler(st, linearCommenter{tracker}, nil, log)
		}
		escH := harbor.NewEscalateHandler(st, sup, log)
		httpHandler = messagesMux(msgH, escH, broker)
		log.Info("harbor messages: mounted", "path", "/messages")
		log.Info("harbor escalate: mounted", "path", "/escalate")

		// Wake-on engine: watches Waiting instances and Wakes them over the
		// live Attach stream (rsrv, the ControlSink) when an external-origin
		// reply lands in the message Log addressed to them, or tears down
		// past max-wait. Resident for the lifetime of the process.
		wpoll, _ := time.ParseDuration(dc.WakePollInterval) // "" or invalid → 0 → engine default
		wmax, _ := time.ParseDuration(dc.WaitMax)           // "" or invalid → 0 → engine default
		warm, _ := time.ParseDuration(dc.WarmTimeout)       // "" or invalid → 0 → engine default
		// Pass messageLog as the Inbox only when it's genuinely non-nil (same
		// plain nil check as the msgH wiring above — no typed-nil hazard).
		var inbox wakeon.Inbox
		if messageLog != nil {
			inbox = messageLog
		} else {
			log.Warn("harbor wake-on: message-log not configured — coves will not wake on replies (teardown/pause only)")
		}
		eng := wakeon.New(st, rsrv /*ControlSink Waker*/, sup /*Reaper*/, sup /*Idler*/, inbox /*Inbox, may be nil*/, wakeon.Config{PollInterval: wpoll, MaxWait: wmax, WarmTimeout: warm}, log)
		go eng.Run(context.Background())
		log.Info("harbor wake-on engine: resident", "wait-max", wmax)

		// Escalation engine: while a managed cove is Waiting, pings ordered
		// human tiers of its Project escalation policy on per-tier timers by
		// @-mentioning them on the cove's own ticket. Reply-detection, waking,
		// and max-wait teardown stay wake-on's job (above); the two engines
		// share only the Instance.Activity==Waiting gate. Resident for the
		// lifetime of the process.
		epoll, _ := time.ParseDuration(dc.EscalationPollInterval) // "" or invalid → 0 → engine default
		eeng := escalate.New(st /*Registry*/, st /*Projects*/, sup /*State*/, linearCommenter{tracker} /*Pinger*/, escalate.Config{PollInterval: epoll}, log)
		go eeng.Run(context.Background())
		log.Info("harbor escalation engine: resident", "poll-interval", epoll)

		// msgport linear ingress engine: polls the team-scoped comments feed
		// and appends inbound human replies to the same messageLog opened
		// above (Slice 1a's shadow writer). Egress stays off this slice — the
		// old count-based wake-on/escalation above are untouched; this only
		// makes the Log start filling from the ingress side too. Nil-guarded
		// on messageLog: without a configured message-log there is nothing to
		// ingest into, so no engine runs.
		if messageLog != nil {
			self, err := tracker.Viewer(context.Background())
			if err != nil {
				log.Warn("harbor msgport: viewer lookup failed; self-post filter disabled", "error", err.Error())
			}
			surf := &linearSurface{feed: tracker, started: time.Now()}
			dir := &directory{store: st, project: firstNonEmpty(dc.Project, harbor.DefaultProject), selfIdentity: self}
			cur, err := newFileCursors(filepath.Join(filepath.Dir(cfg.Store), "msgport-cursors.json"))
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: msgport cursors:", err)
				return 1
			}
			ingest := msgport.New(surf, messageLog, noopMarkers{}, cur, dir, msgport.Config{EgressEnabled: false}, log)
			go ingest.Run(context.Background())
			log.Info("harbor msgport (linear ingress): resident", "egress", false, "self", self != "")
		}
	}

	if cfg.Runtime.Listen != "" { // optional plaintext dev listener (not the production path)
		lis, err := net.Listen("tcp", cfg.Runtime.Listen)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		go func() {
			log.Info("harbor runtime (Attach) plaintext dev listener", "addr", cfg.Runtime.Listen)
			if err := gs.Serve(lis); err != nil {
				log.Error("runtime dev listener stopped", "err", err.Error())
			}
		}()
	}

	// Admin API on the loopback listener (operator surface).
	if cfg.AdminListen != "" {
		var auth harbor.OperatorAuthenticator = harbor.LoopbackAuthenticator{}
		if o := cfg.OperatorAuth.OIDC; o != nil {
			oidcAuth, err := harbor.NewOIDCAuthenticator(context.Background(), o.Issuer, o.Audience, o.RequireScope)
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: operator OIDC:", err)
				return 1
			}
			auth = oidcAuth
			log.Info("harbor admin auth: OIDC", "issuer", o.Issuer, "audience", o.Audience)
		} else {
			log.Info("harbor admin auth: loopback")
		}
		credExists := func(n string) bool { _, ok := specs[n]; return ok }

		// Compose the /ui subtree with its own gate: loopback always reaches it;
		// off-loopback needs a browser session when browser login is configured,
		// else is refused. The login routes (/ui/auth/*) stay unauthenticated.

		// Read-only message-log view: shares the Log opened once above (the same
		// handle the /messages writer dual-writes into) with the admin UI as a
		// read-only reader. Unset config → messageLog nil → msgReader stays its
		// zero value and the view renders a "not configured" notice.
		var msgReader adminui.MessageReader
		if messageLog != nil {
			msgReader = messageLog
		}

		uiMux := http.NewServeMux()
		gate := browserauth.Gate{LoginPath: "/ui/auth/login", ExpectedHosts: cfg.UIHosts, Log: log}
		if bc := cfg.browserAuthConfig(); bc != nil {
			svc, err := browserauth.New(context.Background(), *bc, nil, log)
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: browser login:", err)
				return 1
			}
			uiMux.Handle("/ui/auth/", svc.Routes())
			if oidcAuth, ok := auth.(*harbor.OIDCAuthenticator); ok {
				gate.Sess = &browserauth.SessionVerifier{Auth: oidcAuth}
			}
			log.Info("harbor UI auth: browser OIDC login", "client-id", bc.ClientID)
		} else {
			log.Info("harbor UI auth: loopback-only")
		}
		uiMux.Handle("/ui/", gate.Wrap(adminui.Handler(st, log, sup, credExists, msgReader)))

		admin := harbor.NewAdminHandler(st, sup, auth, credExists, cfg.operatorLoginConfig(), log, uiMux)
		go func() {
			if cfg.adminUsesTLS() {
				cert, key, _ := cfg.adminTLS()
				log.Info("harbor admin API listening (TLS)", "addr", cfg.AdminListen)
				if err := (&http.Server{Addr: cfg.AdminListen, Handler: admin}).ListenAndServeTLS(cert, key); err != nil {
					log.Error("admin API stopped", "err", err.Error())
				}
				return
			}
			log.Info("harbor admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, admin); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
	}

	// Cove-facing :443 listener: multiplex the broker (HTTP) and the Attach gRPC
	// on one TLS port (content-type demux), so a hardened cove reaches both
	// within its 443-only egress.
	cert, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	rawLis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	log.Info("harbor broker+Attach listening (mux)", "addr", cfg.Listen)
	if err := serveMux(rawLis, tlsCfg, gs, httpHandler); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
