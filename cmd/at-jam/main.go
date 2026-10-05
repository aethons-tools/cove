// Command at-jam is the central credential broker + control plane. `serve`
// runs the credential-injecting reverse proxy and a loopback admin API;
// `enroll`/`revoke`/`destination` are admin-API clients. See the Jam specs
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
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/allocator/allocpg"
	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatcher"
	"github.com/aethons-tools/cove/internal/escalate"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercompg"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/deviceflow"
	"github.com/aethons-tools/cove/internal/jam/launcher"
	"github.com/aethons-tools/cove/internal/jam/meui"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessionpg"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/mint"
	"github.com/aethons-tools/cove/internal/relay"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/standing"
	"github.com/aethons-tools/cove/internal/studio"
	"github.com/aethons-tools/cove/internal/switchboard"
	"github.com/aethons-tools/cove/internal/usersecret"
	"github.com/aethons-tools/cove/internal/wakeon"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const defaultAdminURL = "http://127.0.0.1:8081"

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-jam",
		Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the broker + loopback admin API", Run: cmdServe},
			{Name: "enroll", Brief: "enroll an identity (via the admin API) and print its snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "revoke an identity (via the admin API)", Run: cmdRevoke},
			{Name: "destination", Brief: "manage destinations (add|list|rm|import) via the admin API", Run: cmdDestination},
			{Name: "model-spec", Brief: "manage model-specs — how a cove runs its agent: harness, version, principal, model, policy (add|list|show|update|delete) via the admin API", Run: cmdModelSpec},
			{Name: "role", Brief: "manage roles (add|list|rm) via the admin API", Run: cmdRole},
			{Name: "project", Brief: "create, list or remove projects (create|list|rm), or manage a project's roster (roster add-human|add-channel|list|rm-human|rm-channel), escalation policy (escalation set|list|clear), or chat service (chat-service set|clear|show) via the admin API", Run: cmdProject},
			{Name: "kit", Brief: "manage the kit registry (push|list|show|versions|pin|rm)", Run: cmdKit},
			{Name: "export", Brief: "export the Jam config (actors, roles, kits, destinations, model-specs, projects) to a file (or stdout) via the admin API", Run: cmdExport},
			{Name: "import", Brief: "import a Jam config backup into an EMPTY Jam via the admin API (refuses if config already exists)", Run: cmdImport},
			{Name: "pool", Brief: "manage the subscription-OAuth account pool (add|list) — writes the host-side pool store", Run: cmdPool},
			{Name: "grant", Brief: "grant a role to an actor", Run: cmdGrant},
			{Name: "ungrant", Brief: "remove a role grant from an actor", Run: cmdUngrant},
			{Name: "roster", Brief: "list actors and their grants", Run: cmdRoster},
			{Name: "studio", Brief: "manage studios (raise|list|status|teardown) via the admin API", Run: cmdStudio},
			{Name: "cove", Brief: "deprecated alias for studio", Run: cmdCove},
			{Name: "standing", Brief: "declare, list or dismiss a role's named standing sessions (add|list|rm) via the admin API", Run: cmdStanding},
			{Name: "egress", Brief: "set, show or clear a role's raw-egress policy (set|show|clear) via the admin API; applied at the role's next raise", Run: cmdEgress},
			{Name: "context", Brief: "show, set or clear authored session context for a role, project or the Jam (show|set|clear) via the admin API; applied at the next raise", Run: cmdContext},
			{Name: "session", Brief: "request, list or release your personal sessions (request|list|release) via the admin API", Run: cmdSession},
			{Name: "login", Brief: "sign in via OIDC device flow and cache the operator token", Run: cmdLogin},
			{Name: "logout", Brief: "clear the cached operator token", Run: cmdLogout},
			{Name: "whoami", Brief: "show the cached operator identity", Run: cmdWhoami},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdLogin(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile (from ~/.config/at-jam/settings.yml)")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL; persisted to the app's settings when given")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-jam login: unexpected arguments")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam login:", err)
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
			fmt.Fprintln(stderr, "at-jam login: could not save settings:", err)
			return 1
		}
	}
	lc, err := adminclient.New(adminURL, "").LoginConfig()
	if err != nil {
		if errors.Is(err, adminclient.ErrNotFound) {
			fmt.Fprintln(stdout, "this Jam is not OIDC-gated; no login needed")
			return 0
		}
		fmt.Fprintln(stderr, "at-jam login:", err)
		return 1
	}
	ctx := context.Background()
	dc, err := deviceflow.RequestDeviceCode(ctx, http.DefaultClient, deviceflow.Config{
		Issuer: lc.Issuer, Audience: lc.Audience, ClientID: lc.ClientID, Scope: lc.Scope,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-jam login:", err)
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
			err = fmt.Errorf("login timed out; run `at-jam login` again")
		}
		fmt.Fprintln(stderr, "at-jam login:", err)
		return 1
	}
	sub, exp, _ := parseJWTClaims(tok.AccessToken)
	if exp.IsZero() && tok.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if err := saveToken(*app, cachedToken{AccessToken: tok.AccessToken, Sub: sub, Expiry: exp, AdminURL: adminURL}); err != nil {
		fmt.Fprintln(stderr, "at-jam login:", err)
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
		fmt.Fprintln(stderr, "at-jam logout:", err)
		return 2
	}
	if err := clearToken(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
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
		fmt.Fprintln(stderr, "at-jam whoami:", err)
		return 2
	}
	if t, ok := loadToken(*app); ok {
		fmt.Fprintf(stdout, "%s @ %s (expires %s)\n", t.Sub, t.AdminURL, t.Expiry.Format(time.RFC3339))
		return 0
	}
	if _, err := os.Stat(tokenPath(*app)); err == nil {
		fmt.Fprintln(stdout, "session expired; run `at-jam login`")
	} else {
		fmt.Fprintln(stdout, "not logged in")
	}
	return 0
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	baseURLFlag := fs.String("base-url", "", "Jam broker base URL for the printed snippet (overrides the app's settings)")
	jsonOut := fs.Bool("json", false, `print {"id","token"} JSON instead of the shell snippet (base-url not required)`)
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam enroll:", err)
		return 2
	}
	settings := loadSettings(*app)
	adminURL := firstNonEmpty(*adminURLFlag, settings.AdminURL, defaultAdminURL)
	baseURL := firstNonEmpty(*baseURLFlag, settings.BaseURL)
	// --base-url is only needed for the printed snippet; --json omits it.
	if len(pos) > 0 || *id == "" || (!*jsonOut && baseURL == "") {
		fmt.Fprintln(stderr, "at-jam enroll: --id (and --base-url unless --json) are required")
		return 2
	}
	res, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if *jsonOut {
		// Machine-readable output for at-cove auto-enrollment (COV-141). The token
		// is on stdout only — the caller captures it in memory, never argv/logs.
		_ = json.NewEncoder(stdout).Encode(struct {
			ID        string             `json:"id"`
			Token     string             `json:"token"`
			Connector *snippet.Connector `json:"connector,omitempty"`
		}{res.ID, res.Token, res.Connector})
		return 0
	}
	if res.Connector != nil {
		fmt.Fprint(stdout, res.Connector.Render(baseURL, res.Token))
		return 0
	}
	// A Jam that predates connectors: the legacy Anthropic + git snippet.
	fmt.Fprint(stdout, jam.RenderEnrollSnippet(baseURL, res.Token))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam revoke:", err)
		return 2
	}
	if len(pos) > 0 || *id == "" {
		fmt.Fprintln(stderr, "at-jam revoke: --id is required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	if err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Revoke(*id); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
	return 0
}

// oauthBetaDoc is the doc the removed --oauth-beta flag points at.
const oauthBetaDoc = "docs/usage/jam/pool.md"

func cmdDestination(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam destination: expected add|list|rm|import")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("destination "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	// add flags
	var d jam.Destination
	fs.StringVar(&d.Name, "name", "", "destination name")
	fs.StringVar(&d.Route, "route", "", "inbound path prefix, e.g. /git/")
	fs.StringVar(&d.Upstream, "upstream", "", "upstream base URL")
	var identityIn, apply string
	fs.StringVar(&identityIn, "identity-in", "", "bearer|basic-password|x-api-key|raw (custom header specs: destination import)")
	fs.StringVar(&d.CredName, "cred-name", "", "credential name to inject")
	fs.StringVar(&apply, "apply", "", "bearer|basic-password|x-api-key|raw (custom header specs: destination import)")
	// --oauth-beta was removed (COV-241); it stays registered only to refuse
	// with a pointer to its replacement rather than "flag provided but not defined".
	oauthBeta := fs.Bool("oauth-beta", false, "REMOVED: the broker adds the oauth-2025-04-20 anthropic-beta for every subscription-pool credential (see "+oauthBetaDoc+")")
	var envKV []string
	fs.Func("env", "client env KEY=TEMPLATE a studio sets for this destination (repeatable; templates: {url} {base} {host} {token})", func(s string) error {
		envKV = append(envKV, s)
		return nil
	})
	fs.BoolVar(&d.Git, "git", false, "route studios' https://github.com/ through this destination")
	fs.StringVar(&d.Note, "note", "", "usage hint shown to sessions granted this destination (≤300 bytes)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam destination:", err)
		return 2
	}
	if *oauthBeta {
		fmt.Fprintf(stderr, "at-jam destination: --oauth-beta was removed: the broker now adds the oauth-2025-04-20 anthropic-beta to every request carrying a subscription-pool credential, so drop the flag (a non-pool subscription token takes a model-spec principal header rule instead) — see %s\n", oauthBetaDoc)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		d.IdentityIn, d.Apply = jam.ApplyMethod(identityIn), jam.ApplyMethod(apply)
		for _, kv := range envKV {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				fmt.Fprintf(stderr, "at-jam destination add: --env %q: want KEY=TEMPLATE\n", kv)
				return 2
			}
			if d.Env == nil {
				d.Env = map[string]string{}
			}
			d.Env[k] = v
		}
		if err := c.AddDestination(d); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added destination", d.Name)
	case "list":
		ds, err := c.ListDestinations()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, dd := range ds {
			ob := ""
			if len(dd.Env) > 0 {
				ob += ", env=" + strings.Join(slices.Sorted(maps.Keys(dd.Env)), ",")
			}
			if dd.Git {
				ob += ", git"
			}
			if dd.Note != "" {
				ob += ", note"
			}
			fmt.Fprintf(stdout, "%s\t%s\t-> %s\t(cred %q, %s%s)\n", dd.Name, dd.Route, dd.Upstream, dd.CredName, dd.Apply, ob)
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam destination rm: expected one destination name")
			return 2
		}
		if err := c.RemoveDestination(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed destination", pos[0])
	case "import":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam destination import: expected one YAML file path")
			return 2
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		var conf jam.Config
		if err := yaml.Unmarshal(data, &conf); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, dd := range conf.Destinations {
			if err := c.AddDestination(dd); err != nil {
				fmt.Fprintln(stderr, "at-jam:", err)
				return 1
			}
			fmt.Fprintln(stdout, "imported destination", dd.Name)
		}
	default:
		fmt.Fprintln(stderr, "at-jam destination: unknown subcommand", sub)
		return 2
	}
	return 0
}

func cmdRole(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam role: expected add|list|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("role "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	name := fs.String("name", "", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names, each optionally name=credential (the credential the broker injects; default: the destination's cred-name)")
	addressing := fs.String("addressing", "", "comma-separated comms target globs, e.g. human:*,channel:eng-help")
	ttl := fs.Duration("ttl", 0, "default token lifetime for actors of this role (0 = no expiry)")
	kitName := fs.String("kit", "", "bind a registered kit (name)")
	modelSpec := fs.String("model-spec", "", "bind a model-spec (name); empty = "+jam.DefaultModelSpec)
	maxEphemeral := fs.Int("max-ephemeral", 0, "cap on this role's concurrent ephemeral (Requisitioner) sessions (0 = unset: the Requisitioner's max-concurrent applies)")
	maxPersonal := fs.Int("max-personal", 0, "cap on this role's concurrent personal sessions across all owners (0 = no personal sessions)")
	maxPersonalPerOwner := fs.Int("max-personal-per-owner", 0, "cap on one owner's concurrent personal sessions of this role (0 = the pool cap only)")
	idleAfter := fs.Duration("idle-after", 0, "nag a personal session's owner once it has waited on them this long (0 = default 4h)")
	nagEvery := fs.Duration("nag-every", 0, "then re-nag the owner this often (0 = default 24h)")
	reclaimAfter := fs.Duration("reclaim-after", 0, "reclaim a personal session once it has waited on its owner this long (0 = never)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam role:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "at-jam role add: --name is required")
			return 2
		}
		if *maxEphemeral < 0 || *maxPersonal < 0 || *maxPersonalPerOwner < 0 {
			fmt.Fprintln(stderr, "at-jam role add: --max-ephemeral, --max-personal and --max-personal-per-owner must be >= 0")
			return 2
		}
		if *idleAfter < 0 || *nagEvery < 0 || *reclaimAfter < 0 {
			fmt.Fprintln(stderr, "at-jam role add: --idle-after, --nag-every and --reclaim-after must be >= 0")
			return 2
		}
		ds, creds, err := jam.ParseDestinations(*dests)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam role add:", err)
			return 2
		}
		r := jam.Role{
			Name: *name, Kit: *kitName, ModelSpec: *modelSpec,
			Scope: jam.Scope{Destinations: ds, Credentials: creds, Addressing: splitCSV(*addressing), TTL: *ttl},
			Allocation: jam.RoleAllocation{
				MaxEphemeral: *maxEphemeral, MaxPersonal: *maxPersonal, MaxPersonalPerOwner: *maxPersonalPerOwner,
				IdleAfter: *idleAfter, NagEvery: *nagEvery, ReclaimAfter: *reclaimAfter,
			},
		}
		if err := c.PutRole(*project, r); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added role", firstNonEmpty(*project, jam.DefaultProject)+"/"+*name)
	case "list":
		roles, err := c.ListRoles(*project)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, r := range roles {
			fmt.Fprintf(stdout, "%s\tdests=%s\tmodel-spec=%s\taddressing=%s\tttl=%s\tmax-ephemeral=%d\tmax-personal=%d\tmax-personal-per-owner=%d\tidle-after=%s\tnag-every=%s\treclaim-after=%s\tegress=%s\n", r.Name, jam.FormatDestinations(r.Scope.Destinations, r.Scope.Credentials), r.ModelSpecName(), strings.Join(r.Scope.Addressing, ","), r.Scope.TTL, r.Allocation.MaxEphemeral, r.Allocation.MaxPersonal, r.Allocation.MaxPersonalPerOwner, r.Allocation.IdleAfter, r.Allocation.NagEvery, r.Allocation.ReclaimAfter, egressState(r.Scope.Egress))
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam role rm: expected one role name")
			return 2
		}
		if err := c.RemoveRole(firstNonEmpty(*project, jam.DefaultProject), pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed role", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam role: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProject manages a project's roster (humans + channels), escalation
// policy, or chat service via the admin API. Roster subcommands nest under
// "roster": `project roster add-human|add-channel|list|rm-human|rm-channel`.
// Escalation subcommands nest under "escalation" and are handled by
// cmdProjectEscalation: `project escalation set|list|clear`. Chat-service
// subcommands nest under "chat-service" and are handled by
// cmdProjectChatService: `project chat-service set|clear|show`.
func cmdProject(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) >= 1 && (args[0] == "create" || args[0] == "list" || args[0] == "rm") {
		return cmdProjectLifecycle(args[0], args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "escalation" {
		return cmdProjectEscalation(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "chat-service" {
		return cmdProjectChatService(args[1:], stdout, stderr)
	}
	if len(args) < 2 || args[0] != "roster" {
		fmt.Fprintln(stderr, "at-jam project: expected create|list|rm, roster add-human|add-channel|list|rm-human|rm-channel, escalation set|list|clear, or chat-service set|clear|show")
		return 2
	}
	sub, rest := args[1], args[2:]
	fs := flag.NewFlagSet("project roster "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	name := fs.String("name", "", "roster-local name (add-human|add-channel)")
	handle := fs.String("handle", "", "tracker @-mention handle (add-human)")
	login := fs.String("login", "", "link the human to their admin login: the operator identity (OIDC sub, or \"local\" on loopback) (add-human)")
	ref := fs.String("ref", "", "tracker issue identifier the channel posts to (add-channel)")
	service := fs.String("service", "linear", "channel service (add-channel)")
	var delivery multiFlag
	fs.Var(&delivery, "delivery", "per-service delivery target, `service:address[:user-id]` (repeatable, add-human), e.g. discord:<inbox-channel-id>:<your-discord-user-id>; the user id (discord only) binds the human to their Discord account")
	var oidc multiFlag
	fs.Var(&oidc, "oidc", "OIDC identity binding, `issuer:subject` (repeatable, add-human); binds a browser OIDC subject to this roster human. issuer and subject must be non-empty; issuer may itself contain colons (a URL), the subject is the text after the final colon")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam project:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add-human":
		if len(pos) != 1 || *name == "" || *handle == "" {
			fmt.Fprintln(stderr, "at-jam project roster add-human: expected <project> --name and --handle")
			return 2
		}
		var profiles []jam.DeliveryProfile
		for _, d := range delivery {
			p, err := jam.ParseDeliverySpec(d)
			if err != nil {
				fmt.Fprintf(stderr, "at-jam project roster add-human: invalid --delivery %q: %v\n", d, err)
				return 2
			}
			profiles = append(profiles, p)
		}
		var identities []jam.OIDCIdentity
		for _, o := range oidc {
			id, err := jam.ParseOIDCSpec(o)
			if err != nil {
				fmt.Fprintf(stderr, "at-jam project roster add-human: invalid --oidc %q: %v\n", o, err)
				return 2
			}
			identities = append(identities, id)
		}
		if err := c.AddHuman(pos[0], jam.Human{Name: *name, Handle: *handle, Login: *login, Delivery: profiles, Identity: identities}); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added human", *name, "to", pos[0])
	case "add-channel":
		if len(pos) != 1 || *name == "" || *ref == "" {
			fmt.Fprintln(stderr, "at-jam project roster add-channel: expected <project> --name and --ref")
			return 2
		}
		if err := c.AddChannel(pos[0], jam.Channel{Name: *name, Service: *service, Ref: *ref}); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added channel", *name, "to", pos[0])
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam project roster list: expected one project name")
			return 2
		}
		rr, err := c.GetRoster(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, h := range rr.Humans {
			line := fmt.Sprintf("human\t%s\thandle=%s", h.Name, h.Handle)
			if h.Login != "" {
				line += "\tlogin=" + h.Login
			}
			if d, ok := h.DeliveryFor("discord"); ok && d.UserID != "" {
				line += "\tdiscord-user=" + d.UserID
			}
			for _, id := range h.Identity {
				line += "\toidc=" + id.Issuer + ":" + id.Subject
			}
			fmt.Fprintln(stdout, line)
		}
		for _, ch := range rr.Channels {
			fmt.Fprintf(stdout, "channel\t%s\tservice=%s\tref=%s\n", ch.Name, ch.Service, ch.Ref)
		}
	case "rm-human":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam project roster rm-human: expected <project> <name>")
			return 2
		}
		if err := c.RemoveHuman(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed human", pos[1], "from", pos[0])
	case "rm-channel":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam project roster rm-channel: expected <project> <name>")
			return 2
		}
		if err := c.RemoveChannel(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed channel", pos[1], "from", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam project roster: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProjectEscalation manages a project's escalation policy via the admin
// API: `project escalation set|list|clear`.
func cmdProjectEscalation(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam project escalation: expected set|list|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project escalation "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	category := fs.String("category", "", "escalation category (default chain when empty); set/clear")
	var tiers tierFlags
	fs.Var(&tiers, "tier", "a tier as 'target,target@timeout' (repeatable, ordered); set only")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam project escalation:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "set":
		if len(pos) != 1 || len(tiers) == 0 {
			fmt.Fprintln(stderr, "at-jam project escalation set: expected <project> and at least one --tier 'targets@timeout'")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], *category, tiers); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set escalation policy for", pos[0], "-", len(tiers), "tier(s)")
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam project escalation list: expected one project name")
			return 2
		}
		v, err := c.GetEscalationPolicy(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
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
			fmt.Fprintln(stderr, "at-jam project escalation clear: expected one project name")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], *category, nil); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared escalation policy for", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam project escalation: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProjectLifecycle creates, lists or removes projects via the admin API:
// `project create <name>`, `project list`, `project rm <name>`. A project must
// exist before roles, grants or its roster can name it (except "default").
func cmdProjectLifecycle(sub string, rest []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if want := map[string]int{"create": 1, "list": 0, "rm": 1}[sub]; len(pos) != want {
		if want == 1 {
			fmt.Fprintf(stderr, "at-jam project %s: expected <project>\n", sub)
		} else {
			fmt.Fprintf(stderr, "at-jam project %s: unexpected arguments\n", sub)
		}
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam project:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "create":
		if err := c.CreateProject(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "created project", pos[0])
	case "rm":
		if err := c.RemoveProject(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed project", pos[0])
	case "list":
		names, err := c.ListProjects()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, n := range names {
			fmt.Fprintln(stdout, n)
		}
	}
	return 0
}

// cmdProjectChatService manages a project's chat service (the service backing
// its humans' DMs) via the admin API: `project chat-service set|clear|show`.
func cmdProjectChatService(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam project chat-service: expected set|clear|show")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project chat-service "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name")
	service := fs.String("service", "", "chat service (set), e.g. discord")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "at-jam project chat-service: unexpected arguments")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam project chat-service:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "set":
		if *project == "" || *service == "" {
			fmt.Fprintln(stderr, "at-jam project chat-service set: expected --project and --service")
			return 2
		}
		if err := c.SetChatService(*project, *service); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set chat service for", *project, "to", *service)
	case "clear":
		if *project == "" {
			fmt.Fprintln(stderr, "at-jam project chat-service clear: expected --project")
			return 2
		}
		if err := c.SetChatService(*project, ""); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared chat service for", *project)
	case "show":
		if *project == "" {
			fmt.Fprintln(stderr, "at-jam project chat-service show: expected --project")
			return 2
		}
		svc, err := c.GetChatService(*project)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		if svc == "" {
			svc = "(none)"
		}
		fmt.Fprintln(stdout, svc)
	default:
		fmt.Fprintln(stderr, "at-jam project chat-service: unknown subcommand", sub)
		return 2
	}
	return 0
}

// multiFlag collects repeatable string flag values (e.g. --delivery
// service:address, repeatable).
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// tierFlags collects repeatable --tier values, parsing 'targets@timeout'.
type tierFlags []jam.EscalationTier

func (t *tierFlags) String() string { return fmt.Sprintf("%d tiers", len(*t)) }
func (t *tierFlags) Set(v string) error {
	tier, err := jam.ParseEscalationTierSpec(v)
	if err != nil {
		return err
	}
	*t = append(*t, tier)
	return nil
}

func cmdKit(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam kit: expected push|list|show|versions|pin|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kit "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	name := fs.String("name", "", "kit name")
	config := fs.String("config", "", "path to the kit config.yml (or - for stdin); push only")
	version := fs.Int("version", 0, "kit version (show; 0 = current)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam kit:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "push":
		if *name == "" || *config == "" {
			fmt.Fprintln(stderr, "at-jam kit push: --name and --config are required")
			return 2
		}
		data, err := readConfig(*config) // file path or "-" for stdin
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		sk, err := studio.ParseStudioKit(data)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam kit push: invalid studio kit config:", err)
			return 1
		}
		if err := sk.CheckName(*name); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		if err := sk.CheckPrompt(); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		// Resolve a client-only base.context-dir by packing that host directory
		// into base.context (a zip) before sending — the server can't read the
		// operator's filesystem. A relative context-dir is resolved against the
		// kit file's directory (CWD for stdin). A no-op when unset.
		baseDir := "."
		if *config != "-" {
			baseDir = filepath.Dir(*config)
		}
		if err := sk.ResolveContextDir(baseDir); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push: base.context-dir:", err)
			return 1
		}
		if err := sk.ResolveNoteFiles(baseDir); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push: notes:", err)
			return 1
		}
		if err := sk.CheckNotes(); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		resolved, err := sk.ToJSON()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		res, err := c.PushKit(*name, string(resolved))
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		if res.Unchanged {
			fmt.Fprintf(stdout, "%s unchanged (current v%d)\n", *name, res.Version)
		} else {
			fmt.Fprintf(stdout, "pushed %s v%d\n", *name, res.Version)
		}
	case "list":
		kits, err := c.ListKits()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, k := range kits {
			fmt.Fprintf(stdout, "%s\tcurrent=v%d\tversions=%d\n", k.Name, k.Current, k.Versions)
		}
	case "show":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam kit show: expected one kit name")
			return 2
		}
		res, err := c.GetKit(pos[0], *version)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		// Stored form is canonical JSON; render it back as YAML for the human.
		// ParseStudioKit accepts JSON (and any YAML rows) alike.
		sk, err := studio.ParseStudioKit([]byte(res.Config))
		if err != nil {
			fmt.Fprintln(stderr, "at-jam kit show: stored config is not a valid studio kit:", err)
			return 1
		}
		sk.LegacyName = "" // the registry names the kit; a legacy row's name isn't shown
		y, err := yaml.Marshal(sk)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprint(stdout, string(y))
		ceiling, excluded := studioShowEgress(sk.Egress)
		fmt.Fprintf(stdout, "egress ceiling: %s\n", joinOrNone(ceiling))
		fmt.Fprintf(stdout, "excluded (COV-208): %s\n", joinOrNone(excluded))
	case "versions":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam kit versions: expected one kit name")
			return 2
		}
		vers, err := c.KitVersions(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, v := range vers {
			fmt.Fprintf(stdout, "v%d\n", v)
		}
	case "pin":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam kit pin: expected <name> <version>")
			return 2
		}
		v, err := strconv.Atoi(pos[1])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam kit pin: version must be an integer")
			return 2
		}
		if err := c.PinKit(pos[0], v); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "pinned %s to v%d\n", pos[0], v)
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam kit rm: expected one kit name")
			return 2
		}
		if err := c.RemoveKit(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed kit", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam kit: unknown subcommand", sub)
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

// cmdCove is the deprecated `at-jam cove …` verb: it warns once and runs
// `at-jam studio …` (docs/usage/jam/renamed-from-harbor.md).
func cmdCove(args []string, g cli.Globals, stdout, stderr io.Writer) int {
	logging.Deprecated(stderr, "at-jam cove", "at-jam studio")
	return cmdStudio(args, g, stdout, stderr)
}

// cmdStudio manages studios (managed coves — the entity the UI and CLI call a
// Studio; ids, routes and the admin API still say "cove").
func cmdStudio(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam studio: expected raise|list|status|teardown")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("studio "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "studio/actor id")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role to raise the studio for")
	unit := fs.String("unit", "", "unit of work (e.g. issue identifier)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the workload prompt (raise only; read host-side, never passed on argv)")
	activity := fs.String("activity", "", "reported activity: running|waiting|blocked|done (status only)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam studio:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "raise":
		if *id == "" || *role == "" {
			fmt.Fprintln(stderr, "at-jam studio raise: --id and --role are required")
			return 2
		}
		var prompt string
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam studio raise: --prompt-file:", err)
				return 1
			}
			prompt = string(b)
		}
		res, err := c.RaiseCove(adminclient.CoveRaiseParams{ID: *id, Project: *project, Role: *role, Unit: *unit, Prompt: prompt})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "raised %s (phase=%s)\n", res.ID, res.Phase)
	case "list":
		coves, err := c.ListCoves()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, cv := range coves {
			fmt.Fprintf(stdout, "%s\trole=%s\tunit=%s\tphase=%s\tactivity=%s\tholder=%s\tconnector=%s\n",
				cv.ID, cv.Role, cv.Unit, cv.Phase, cv.Activity, cv.LeaseHolder, cv.Connector)
		}
	case "status":
		if *id == "" || *activity == "" {
			fmt.Fprintln(stderr, "at-jam studio status: --id and --activity are required")
			return 2
		}
		if err := c.ReportCoveStatus(*id, *activity); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "reported %s activity=%s\n", *id, *activity)
	case "teardown":
		name := *id
		if name == "" && len(pos) == 1 {
			name = pos[0]
		}
		if name == "" {
			fmt.Fprintln(stderr, "at-jam studio teardown: --id (or a positional id) is required")
			return 2
		}
		if err := c.TeardownCove(name); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "tore down", name)
	default:
		fmt.Fprintln(stderr, "at-jam studio: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdSession requests, lists and releases the caller's personal sessions. The
// caller is the roster human linked (`project roster add-human --login`) to the
// operator identity the admin API authenticates.
func cmdSession(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam session: expected request|list|release")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("session "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role to request a personal session of (request only)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the session's prompt (request only; read host-side, never passed on argv)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam session:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "request":
		if *role == "" {
			fmt.Fprintln(stderr, "at-jam session request: --role is required")
			return 2
		}
		var prompt string
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam session request: --prompt-file:", err)
				return 1
			}
			prompt = string(b)
		}
		res, err := c.RequestPersonalSession(*project, *role, prompt)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, res.ID)
	case "list":
		sessions, err := c.ListPersonalSessions(*project)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, s := range sessions {
			fmt.Fprintf(stdout, "%s\trole=%s\tphase=%s\tactivity=%s\traised=%s\n",
				s.ID, s.Role, s.Phase, s.Activity, s.RaisedAt.Format(time.RFC3339))
		}
	case "release":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam session release: expected one session id")
			return 2
		}
		if err := c.ReleasePersonalSession(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "released", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam session: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdStanding manages a role's standing-session declarations: Jam keeps one
// cove running per declared name, restarts it if it dies, and tears it down once
// the name is removed.
func cmdStanding(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam standing: expected add|list|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("standing "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role the standing session belongs to")
	name := fs.String("name", "", "standing session name, unique within the role (add only)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the session's prompt (add only; read host-side, never passed on argv)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam standing:", err)
		return 2
	}
	if *role == "" && (sub == "add" || sub == "list" || sub == "rm") {
		fmt.Fprintf(stderr, "at-jam standing %s: --role is required\n", sub)
		return 2
	}
	proj := firstNonEmpty(*project, jam.DefaultProject)
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		if *name == "" || *promptFile == "" {
			fmt.Fprintln(stderr, "at-jam standing add: --name and --prompt-file are required")
			return 2
		}
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam standing add: --prompt-file:", err)
			return 1
		}
		if err := c.AddStanding(proj, *role, jam.StandingSession{Name: *name, Prompt: string(b)}); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "declared standing session %s/%s/%s (%s)\n", proj, *role, *name, jam.StandingActorID(proj, *role, *name))
	case "list":
		list, err := c.ListStanding(proj, *role)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, s := range list {
			fmt.Fprintf(stdout, "%s\tid=%s\n", s.Name, jam.StandingActorID(proj, *role, s.Name))
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam standing rm: expected one standing session name")
			return 2
		}
		if err := c.RemoveStanding(proj, *role, pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "dismissed standing session", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam standing: unknown subcommand", sub)
		return 2
	}
	return 0
}

// egressState renders a role's egress policy for listings: "kit" (no policy:
// the kit's default list), "none" (set but empty) or the comma-joined list.
func egressState(p *jam.EgressPolicy) string {
	switch {
	case p == nil:
		return "kit"
	case len(p.Domains) == 0:
		return "none"
	default:
		return strings.Join(p.Domains, ",")
	}
}

// cmdEgress manages a role's raw-egress policy: the list Jam pushes into each
// cove of the role at raise, replacing the kit's policy list within the kit's
// ceiling. A change takes effect at the role's next raise.
func cmdEgress(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam egress: expected set|show|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("egress "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role whose egress policy to manage")
	none := fs.Bool("none", false, "set an empty policy: nothing beyond the sealed base and the kit's infra domains (set only)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam egress:", err)
		return 2
	}
	switch sub {
	case "set", "show", "clear":
	default:
		fmt.Fprintln(stderr, "at-jam egress: unknown subcommand", sub)
		return 2
	}
	if *role == "" {
		fmt.Fprintf(stderr, "at-jam egress %s: --role is required\n", sub)
		return 2
	}
	var domains []string
	switch {
	case sub == "set" && *none && len(pos) > 0:
		fmt.Fprintln(stderr, "at-jam egress set: give domains or --none, not both")
		return 2
	case sub == "set" && !*none:
		for _, d := range strings.Split(strings.Join(pos, ","), ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
		if len(domains) == 0 {
			fmt.Fprintln(stderr, "at-jam egress set: expected a comma-separated domain list, or --none for an empty policy")
			return 2
		}
	case sub != "set" && len(pos) > 0:
		fmt.Fprintf(stderr, "at-jam egress %s: unexpected arguments\n", sub)
		return 2
	}
	proj := firstNonEmpty(*project, jam.DefaultProject)
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "set":
		if err := c.SetEgress(proj, *role, domains); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "set egress policy for %s/%s; applies at the next raise\n", proj, *role)
	case "show":
		v, err := c.ShowEgress(proj, *role)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		switch {
		case !v.Managed:
			fmt.Fprintln(stdout, "kit default")
		case len(v.Domains) == 0:
			fmt.Fprintln(stdout, "none (nothing beyond the sealed base and the kit's infra domains)")
		default:
			for _, d := range v.Domains {
				fmt.Fprintln(stdout, d)
			}
		}
	case "clear":
		if err := c.ClearEgress(proj, *role); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "cleared egress policy for %s/%s (kit default); applies at the next raise\n", proj, *role)
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
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "actor id")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role name")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam "+verb+":", err)
		return 2
	}
	if len(pos) > 0 || *id == "" || *role == "" {
		fmt.Fprintln(stderr, "at-jam "+verb+": --id and --role are required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	var err error
	if remove {
		err = c.RemoveGrant(*id, firstNonEmpty(*project, jam.DefaultProject), *role)
	} else {
		err = c.AddGrant(*id, jam.Grant{Project: *project, Role: *role})
	}
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	fmt.Fprintln(stdout, verb+"ed", *role, "to", *id)
	return 0
}

func cmdRoster(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("roster", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam roster:", err)
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-jam roster: takes no positional arguments")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	roster, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Roster()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	for _, a := range roster {
		for _, g := range a.Grants {
			fmt.Fprintf(stdout, "%s\t%s/%s\tdests=%s\n", a.ID, g.Project, g.Role, jam.FormatDestinations(g.Destinations, g.Credentials))
		}
	}
	return 0
}

// placeholderLauncher satisfies jam.Launcher without a real backend: it
// records a synthetic location and always probes Alive, so the supervisor spine
// (registry, leases, reconciler, restart re-adoption) runs end-to-end against a
// live `at-jam serve`. `cove raise` against it creates a Live Instance with no
// actual cove. The real backend+kit launcher lands in a later slice.
type placeholderLauncher struct{}

func (placeholderLauncher) Raise(_ context.Context, spec jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
	return "placeholder:" + spec.ActorID, nil
}
func (placeholderLauncher) Teardown(context.Context, jam.Instance) error { return nil }
func (placeholderLauncher) Probe(context.Context, jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (placeholderLauncher) Pause(context.Context, jam.Instance) error   { return nil }
func (placeholderLauncher) Unpause(context.Context, jam.Instance) error { return nil }

// ApplyEgress is a no-op: there is no cove to police (raise ignores egress too).
func (placeholderLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}

// PrepareKit is a no-op: the placeholder's Raise never reports ErrKitNotReady,
// so the supervisor never asks it to prepare a kit.
func (placeholderLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}

// linearCommenter adapts *linear.Client to escalate.Pinger (the escalation
// engine's ticket-comment capability). It exists here, rather than in
// internal/jam, so Jam core never imports internal/dispatch/linear or
// internal/dispatch/scheduler (see AGENTS.md boundary rules): the concrete
// tracker type is a wiring-layer concern.
type linearCommenter struct{ c *linear.Client }

func (l linearCommenter) IssueByIdentifier(ctx context.Context, identifier string) (string, error) {
	return l.c.IssueByIdentifier(ctx, identifier)
}

func (l linearCommenter) PostComment(ctx context.Context, issueID, body string) error {
	return l.c.PostComment(ctx, issueID, body)
}

func cmdServe(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to the serve config YAML")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *cfgPath == "" {
		fmt.Fprintln(stderr, "at-jam serve: --config is required")
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	cfg, err := parseServeConfig(data)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	for _, d := range cfg.deprecated {
		logging.Deprecated(stderr, d[0], d[1])
	}
	if unknown := unknownServeKeys(data); len(unknown) > 0 {
		fmt.Fprintf(stderr, "at-jam: warning: ignoring unknown jam.yml key(s): %s\n", strings.Join(unknown, ", "))
		for _, k := range unknown {
			if k == "destinations" {
				fmt.Fprintln(stderr, "at-jam: note: destinations are managed via the admin API — run `at-jam destination import`")
			}
		}
	}
	if err := cfg.validateAdminExposure(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateLauncher(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateRequisitioner(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateStorage(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateStorePostgres(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateDiscord(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateWake(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateSessionEvents(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validatePool(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if err := cfg.validateCredentials(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	specs, err := planCredentials(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}

	// Postgres is the only store backend (validateStorage guarantees
	// store-postgres is set). The DB password is resolved on the host in memory and assembled into
	// the DSN — never written to disk/argv, never logged.
	var st jam.Store
	var pgPool *pgxpool.Pool // shared with the message log and session events
	{
		pc := cfg.StorePostgres
		resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[pc.PasswordCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: store-postgres password:", err)
			return 1
		}
		dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
			pc.Host, pc.Port, pc.Database, pc.User, resolved[pc.PasswordCred], pc.SSLMode)
		ps, err := jam.NewPostgresStore(context.Background(), dsn, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: store-postgres:", err)
			return 1
		}
		defer ps.Close()
		st = ps
		pgPool = ps.Pool()
		log.Info("Jam store: postgres", "host", pc.Host, "database", pc.Database) // never the password
	}
	// The one-time model-spec store migration, before seeding — each schema
	// step at most once (jam.ModelSpecSchemaVersion). COV-242: every spec
	// stored before the version split gets an exact version pin (a legacy
	// range kept as version-constraint when it admits the pin), unknown-
	// marketplace plugins dropped, and the image-wide default plugins when it
	// has none. COV-245: a stored claude-default gains the Claude preference
	// keys it lacks (moved out of the sealed managed settings; operator values
	// kept). Each loss is a WARN naming the spec. A no-op once recorded —
	// except the COV-241 scan, run every startup: a destination still carrying
	// the removed oauth_beta flag has it cleared (the broker adds the beta for
	// pool credentials; a non-pool flagged credential's specs gain the rule).
	mig, err := jam.MigrateModelSpecs(st)
	for _, w := range mig.Warnings {
		log.Warn("model-spec migration", "detail", w)
	}
	if err != nil {
		log.Warn("model-spec migration incomplete (retried next startup)", "migrated", mig.Migrated, "reason", err.Error())
	} else if len(mig.Migrated) > 0 || len(mig.Destinations) > 0 {
		log.Info("model-specs migrated", "names", mig.Migrated, "default_version", modelspec.DefaultClaudeVersion, "oauth_beta_destinations", mig.Destinations)
	}
	// Seed the default model-spec every unbound role resolves to. Not fatal when
	// no principal resolves: unbound roles then deliver no spec and their coves
	// keep the harness's built-in defaults (re-tried at every startup).
	if created, err := jam.EnsureDefaultModelSpec(st, cfg.Pool != nil); err != nil {
		log.Warn("default model-spec not seeded", "name", jam.DefaultModelSpec, "reason", err.Error())
	} else if created {
		log.Info("default model-spec seeded", "name", jam.DefaultModelSpec)
	}

	base := jam.NewSecretResolver(runner.OS{}, specs)
	var creds jam.CredResolver = base
	if cfg.Pool != nil {
		poolStore, err := jam.NewFilePoolStore(cfg.Pool.Store)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: pool store:", err)
			return 1
		}
		pool := jam.NewPool(poolStore)
		creds = jam.NewChainResolver(base, pool, cfg.Pool.CredName)
		interval, margin, _ := cfg.poolDurations() // validated above
		refresher := jam.NewRefresher(poolStore, jam.RefresherOptions{
			TokenURL: cfg.Pool.TokenURL, ClientID: cfg.Pool.ClientID, Scope: cfg.Pool.Scope,
			Margin: margin, Log: log,
		})
		go refresher.Run(context.Background(), interval)
		log.Info("Jam subscription pool enabled", "store", cfg.Pool.Store, "cred", cfg.Pool.CredName) // never tokens
	}
	broker := jam.NewBroker(st, creds, log)

	ttl, reconcile, err := cfg.runtimeDurations()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	var lch jam.Launcher = placeholderLauncher{}
	var defaultRef jam.KitRef
	var haveDefaultKit bool
	if lc := cfg.Runtime.Launcher; lc != nil {
		be, ok := colima.New(runner.OS{}).(launcher.Backend) // colima.New returns backend.Backend; *Colima also satisfies DispatchOps+GetStatus
		if !ok {
			fmt.Fprintln(stderr, "at-jam: colima backend does not satisfy launcher.Backend")
			return 1
		}
		// The launcher builds each studio kit's image on demand, so it needs the
		// public half of the SSH identity to bake into the image's authorized_keys
		// (the same key at-cove install baked; its private half is IdentityFile). No
		// kit source directory: the build context comes from the kit definition
		// (data) + resources compiled into this binary, and the FROM-base is resolved
		// per kit on the substrate (gate ON).
		pub, err := os.ReadFile(lc.IdentityFile + ".pub")
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: launcher identity public key ("+lc.IdentityFile+".pub):", err)
			return 1
		}
		lch = launcher.New(launcher.Config{
			Ops: be, Runner: runner.OS{},
			JamHost: lc.JamHost, RuntimeAddr: lc.RuntimeAddr,
			IdentityFile: lc.IdentityFile, KnownHostsDir: lc.KnownHostsDir,
			DNS: lc.DNS, Docker: lc.Docker,
			PublicKey: pub,
			// Seed raised coves in subscription mode when the pool is enabled, so
			// their claude authenticates as a pooled subscription principal.
			Subscription: cfg.Pool != nil,
			Log:          log,
		})
		// Seed the built-in default studio kit (blessed base, Anthropic-free egress
		// ceiling — COV-208) in the registry and hand its reference to the
		// supervisor. Idempotent: an unchanged definition reuses the version across
		// restarts; the supervisor resolves the full definition from the registry
		// only when the launcher reports the kit is not yet built.
		defaultRef, err = jam.EnsureDefaultStudioKit(st)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: seed default studio kit:", err)
			return 1
		}
		haveDefaultKit = true
		log.Info("Jam launcher: colima", "runtime-addr", lc.RuntimeAddr, "default-kit", defaultRef.String())
	}
	sup := jam.NewSupervisor(st, lch, jam.NewHolderID(), ttl, reconcile, time.Now, log)
	if haveDefaultKit {
		sup.SetDefaultStudioKit(defaultRef)
	}
	go sup.Run(context.Background())

	// Attach gRPC server: served on the cove-facing :443 mux below, and
	// optionally on a plaintext dev listener (runtime.listen). One server, one
	// ControlSink. Built here (ahead of the Requisitioner block below) because the
	// resident wake-on engine needs rsrv as its Waker.
	rsrv := attach.NewServer(st, sup, log)
	sup.SetControlSink(rsrv)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, rsrv)

	// Message Log: opened once (handle held for the serve lifetime) and shared
	// between the /squawks writer (dual-write shadow, below) and the admin UI's
	// read-only reader (further down). Postgres (the shared control-plane pool).
	ml, err := intercompg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam: message log (postgres):", err)
		return 1
	}
	var intercomLog intercom.Store = ml // Close is a no-op; the store owns the pool
	log.Info("Jam message log: postgres (shared control-plane database)")
	// Every writer (agent send, relay ingress, /me/send, escalation) shares this
	// one handle, so wrapping it lets live views (/me/events) see each append.
	logChanges := intercom.NewNotifier(intercomLog)
	intercomLog = logChanges
	sup.SetTailReader(intercomLog)

	// Session events (docs/usage/jam/session-events.md): stored in the shared
	// control-plane Postgres.
	sessStore, err := sessionpg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam: session events (postgres):", err)
		return 1
	}
	log.Info("Jam session events: postgres (shared control-plane database)")
	sessHub := sessionevents.NewHub()
	// Derived per-session status for /me's presence strip, fed by every event.
	sessPresence := sessionevents.NewPresence(nil)
	sessHub.Observe(sessPresence.Observe)
	rsrv.SetSessionEvents(sessionevents.NewIngest(sessStore, sessHub, nil))
	if keep, _ := sessionevents.ParseRetention(cfg.SessionEventsRetention); keep > 0 {
		go sessionevents.RunRetention(context.Background(), sessStore, keep, 24*time.Hour, nil, log)
	}

	// The Allocator is Jam's capacity authority, built whenever Jam serves
	// (not only with a Requisitioner): it admits the Requisitioner's ephemeral raises and
	// operators' personal-session requests against
	// the roster's per-(project, role) policy (`role add --max-ephemeral
	// --max-personal --max-personal-per-owner`), read live on each grant. The
	// Requisitioner's max-concurrent is the ephemeral fallback for its own
	// (project, role), seeded only when a Requisitioner is configured.
	//
	// Ledger cutover (slice 4): with Postgres the allocation event store is the
	// AUTHORITATIVE cap — admission is an atomic OCC grant (append-iff-under-caps)
	// scoped per-(project, role) Outstanding, and teardown/compensation release the
	// slot. Without the Postgres allocator ledger (tests only) the ledger is nil ⇒ ephemeral Grant
	// falls back to the registry live count (global, slice-1 behavior), personal
	// Grant fails with ErrNeedsLedger, and RecordRelease is a no-op
	// (SetReleaser(alloc) stays a harmless no-op).
	var ledger allocator.Ledger
	if pgPool != nil {
		as, err := allocpg.New(context.Background(), pgPool, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		ledger = as
	}
	alloc := allocator.New(jam.InstanceCounter{Store: st}, newRosterPolicy(st, cfg.Runtime.Requisitioner), ledger)
	alloc.SetLogger(log)
	sup.SetReleaser(alloc) // actual-state-out: teardown records ReservationReleased (frees the slot)
	// Reconcile sweep (slice 5): with the authoritative ledger, a crash between a
	// Grant and its raise leaves a dangling ReservationGranted (a leaked slot). The
	// resident sweep periodically releases outstanding ephemeral and standing reservations older
	// than a grace window with no live instance, so the ledger self-heals.
	// Postgres-only (nil ledger ⇒ Sweep is a no-op, so no loop). Sweep often (a
	// leaked slot reduces capacity until reclaimed) with a grace window comfortably
	// beyond a Colima raise so an in-flight raise — instance not yet in the
	// registry — is never swept.
	if ledger != nil {
		const (
			allocSweepInterval = 1 * time.Minute
			allocSweepGrace    = 5 * time.Minute
		)
		go alloc.SweepLoop(context.Background(), allocSweepInterval, allocSweepGrace)
		log.Info("Jam allocator: reconcile sweep resident", "interval", allocSweepInterval, "grace", allocSweepGrace)
	}

	// Standing reconciler: keeps one live cove per standing session declared on a
	// role (`at-jam standing add`) — raises a missing or dead one under its
	// per-name actor id (admitted by the Allocator, released on a failed raise,
	// with per-name backoff) and tears down one whose name was removed. Resident
	// whenever Jam serves; with no declarations a tick does nothing.
	stdg := standing.New(st /*Roster*/, st /*Registry*/, alloc /*Granter*/, sup /*Supervisor*/, standing.DefaultInterval, log)
	stdg.SetActors(st) // clear a standing identity left over from an interrupted raise
	go stdg.Run(context.Background())
	log.Info("Jam standing reconciler: resident", "interval", standing.DefaultInterval)

	// The intercom — the /squawks + /escalate endpoints, the wake-on engine and
	// the Discord relay — runs always (message log via Postgres is always present):
	// a personal session converses with its owner over it. The tracker, the
	// Requisitioner, the escalation engine and the Linear relay need the tracker, so
	// they stay in the Requisitioner block below.
	dc := cfg.Runtime.Requisitioner

	// httpHandler is the cove-facing HTTP handler mounted on the :443 mux below:
	// the broker, plus /squawks and /escalate (always available).
	httpHandler := coveHTTPHandler(broker, st, sup, intercomLog, dc != nil, log)

	// Wake-on engine: watches Waiting instances and Wakes them over the live
	// Attach stream (rsrv, the ControlSink) when an external-origin reply lands
	// in the message Log addressed to them, pauses them past the warm-timeout,
	// tears down non-personal ones past wait-max, and runs the personal-session
	// idle ladder (nag the owner, optionally reclaim). Resident for the lifetime
	// of the process. Settings: runtime.wake > runtime.requisitioner > defaults.
	wcfg := cfg.wakeSettings()
	eng := wakeon.New(st, rsrv /*ControlSink Waker*/, sup /*Reaper*/, sup /*Idler*/, intercomLog /*Inbox*/, wcfg, log)
	// Personal-session idle ladder: nag the owner past the role's idle-after
	// (squawks sent as the cove, delivered by the relay), optionally reclaim
	// past reclaim-after.
	nagger := intercomNagger{log: intercomLog, roster: st}
	eng.SetIdleLadder(st /*RoleLookup*/, sup /*NagRecorder*/, nagger)
	// Wake Running coves on a reply too: an agent holding its episode open for
	// a background task is Running, and its owner's reply must reach it then.
	eng.SetRunningWake(sup /*Cursor*/)
	go eng.Run(context.Background())
	log.Info("Jam wake-on engine: resident", "wait-max", wcfg.MaxWait)

	// Relay state shared by the Linear and Discord relay engines (one cursors
	// file, one markers file, one directory). Every directory field is set
	// before any engine starts.
	var (
		relayCursors *fileCursors
		relayMarkers *fileMarkers
		discordTok   string
	)
	dir := &directory{store: st, log: log}
	runDiscord := cfg.Runtime.Discord != nil
	var stateDir string
	if dc != nil || runDiscord {
		if stateDir, err = cfg.stateDir(); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			fmt.Fprintln(stderr, "at-jam: state-dir:", err)
			return 1
		}
		cursorsPath, markersPath, _ := relayStatePaths(stateDir)
		if relayCursors, err = newFileCursors(cursorsPath); err != nil {
			fmt.Fprintln(stderr, "at-jam: relay cursors:", err)
			return 1
		}
		if relayMarkers, err = newFileMarkers(markersPath); err != nil {
			fmt.Fprintln(stderr, "at-jam: relay markers:", err)
			return 1
		}
	}
	var discordReceipts *fileReceipts
	if runDiscord {
		_, _, receiptsPath := relayStatePaths(stateDir)
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[cfg.Runtime.Discord.BotTokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: discord bot-token:", err)
			return 1
		}
		discordTok = tokEnv[cfg.Runtime.Discord.BotTokenCred]
		if discordReceipts, err = newFileReceipts(receiptsPath); err != nil {
			fmt.Fprintln(stderr, "at-jam: relay receipts:", err)
			return 1
		}
		dir.receipts = discordReceipts // wires directory.routeDiscord (COV-183): reply→cove lookup
	}

	if dc != nil {
		// Resolve Jam's own tracker token (never injected into a cove, never logged).
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[dc.TrackerTokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: requisitioner tracker-token:", err)
			return 1
		}
		token := tokEnv[dc.TrackerTokenCred]
		// linear.New wants a full kit.Config; wrap the configured LinearTracker.
		kitShell := kit.Config{Tracker: &kit.Tracker{Linear: dc.Linear}}
		tracker, err := linear.New(kitShell, token, nil)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: requisitioner tracker:", err)
			return 1
		}
		poll, _ := time.ParseDuration(dc.PollInterval) // "" or invalid → 0 → Requisitioner default
		// The Requisitioner's (project, role), normalized the same way as the
		// Allocator's fallback (see requisitionerProject).
		project := requisitionerProject(dc)
		if r, ok := st.GetRole(project, dc.Role); ok && r.Allocation.MaxEphemeral > 0 && r.Allocation.MaxEphemeral != dc.MaxConcurrent {
			log.Info("Jam allocator: roster max-ephemeral overrides Requisitioner max-concurrent",
				"project", project, "role", dc.Role, "max-ephemeral", r.Allocation.MaxEphemeral, "max-concurrent", dc.MaxConcurrent)
		}
		disp := dispatcher.New(tracker, sup, st, alloc, dispatcher.Config{
			Role: dc.Role, Project: project, PollInterval: poll,
		}, log)
		go disp.Run(context.Background())
		log.Info("requisitioner: resident", "role", dc.Role, "max-concurrent", dc.MaxConcurrent)

		// Escalation engine: while a managed cove is Waiting on a ticket, pings
		// ordered human tiers of its Project escalation policy on per-tier timers
		// by @-mentioning them on the cove's own ticket. Reply-detection, waking,
		// and max-wait teardown stay wake-on's job (above); the two engines
		// share only the Instance.Activity==Waiting gate. Resident for the
		// lifetime of the process.
		epoll, _ := time.ParseDuration(dc.EscalationPollInterval) // "" or invalid → 0 → engine default
		eeng := escalate.New(st /*Registry*/, st /*Projects*/, sup /*State*/, linearCommenter{tracker} /*Pinger*/, escalate.Config{PollInterval: epoll}, log)
		go eeng.Run(context.Background())
		log.Info("Jam escalation engine: resident", "poll-interval", epoll)

		// relay linear engine: polls the team-scoped comments feed and
		// appends inbound human replies to the intercomLog opened above
		// (ingress), and delivers outbound Log messages to Linear (egress,
		// COV-176 Task 4) — the Log is the single source of truth for both
		// directions.
		self, err := tracker.Viewer(context.Background())
		if err != nil {
			log.Warn("Jam relay: viewer lookup failed; self-post filter disabled", "error", err.Error())
		}
		dir.project = firstNonEmpty(dc.Project, jam.DefaultProject)
		dir.selfIdentity = self
		surf := &linearSurface{feed: tracker, poster: tracker, started: time.Now()}
		// Seed once: skip everything the 1a dual-write already delivered live,
		// so turning egress on never re-posts the Log's shadow history.
		// Persisted with a nonzero LastSeq → never re-seeds (a re-seed to a
		// newer tail would drop messages appended-but-not-yet-delivered since
		// the first cutover). needsSeed also re-seeds a marker whose LastSeq
		// is zero, which covers a pre-COV-184 marker file (persisted the
		// low-water as LastMsg, a string) upgrading in place — see
		// fileMarkers.needsSeed.
		if relayMarkers.needsSeed("linear") {
			if err := relayMarkers.SetEgress("linear", relay.EgressMark{LastSeq: logTailSeq(intercomLog)}); err != nil {
				fmt.Fprintln(stderr, "at-jam: relay egress seed:", err)
				return 1
			}
		}
		eng := relay.New(surf, intercomLog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
		go eng.Run(context.Background())
		log.Info("Jam relay (linear): resident, egress ON", "self", self != "")

	}
	// relay discord engine: a resident engine over the same Log, markers file,
	// cursors, and directory as the Linear one — delivers outbound Log messages
	// to Discord (egress) AND polls every discord project's inbox channels for
	// human replies, routing a reply back to the cove it answers via the receipt
	// store (ingress). The engine keys EgressMark by Service(), so "linear" and
	// "discord" marks live side by side in the one markers file. Gated on
	// runtime.discord; no Requisitioner needed.
	if runDiscord {
		dsurf := &discordSurface{
			dial: func(channels []string) discordClient {
				return switchboard.NewRESTClient(discordTok, channels)
			},
			channelsFor: func(project string) []string { return discordPolledChannels(st, project) },
			receipts:    discordReceipts,
			log:         log,
		}
		if relayMarkers.needsSeed("discord") { // seed: don't re-deliver the backlog to Discord (see needsSeed doc)
			if err := relayMarkers.SetEgress("discord", relay.EgressMark{LastSeq: logTailSeq(intercomLog)}); err != nil {
				fmt.Fprintln(stderr, "at-jam: discord egress seed:", err)
				return 1
			}
		}
		deng := relay.New(dsurf, intercomLog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
		go deng.Run(context.Background())
		log.Info("Jam relay (discord): resident, egress ON")
	}

	if cfg.Runtime.Listen != "" { // optional plaintext dev listener (not the production path)
		lis, err := net.Listen("tcp", cfg.Runtime.Listen)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		go func() {
			log.Info("Jam runtime (Attach) plaintext dev listener", "addr", cfg.Runtime.Listen)
			if err := gs.Serve(lis); err != nil {
				log.Error("runtime dev listener stopped", "err", err.Error())
			}
		}()
	}

	// Admin API on the loopback listener (operator surface).
	if cfg.AdminListen != "" {
		var auth jam.OperatorAuthenticator = jam.LoopbackAuthenticator{}
		if o := cfg.OperatorAuth.OIDC; o != nil {
			oidcAuth, err := jam.NewOIDCAuthenticator(context.Background(), o.Issuer, o.Audience, o.RequireScope)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam: operator OIDC:", err)
				return 1
			}
			auth = oidcAuth
			log.Info("Jam admin auth: OIDC", "issuer", o.Issuer, "audience", o.Audience)
		} else {
			log.Info("Jam admin auth: loopback")
		}
		// A destination's cred_name is valid if it's a configured `credentials:`
		// entry OR (pool enabled) the pool cred-name, which the ChainResolver
		// resolves from the account pool by identity rather than from `credentials:`.
		credExists := cfg.credConfigured

		// Compose the /ui subtree with its own gate: loopback always reaches it;
		// off-loopback needs a browser session when browser login is configured,
		// else is refused. The login routes (/ui/auth/*) stay unauthenticated.

		// Read-only intercom view: shares the Log opened once above (the same
		// handle the /squawks writer dual-writes into) with the admin UI as a
		// read-only reader.
		var squawkReader adminui.SquawkReader = intercomLog

		uiMux := http.NewServeMux()
		gate := browserauth.Gate{
			LoopbackTrust: browserauth.OperatorLoopbackTrust(),
			LoginPath:     "/ui/auth/login",
			ExpectedHosts: cfg.UIHosts,
			Log:           log,
		}
		// dev-identity (DEV ONLY; parse refuses it off-loopback): loopback
		// requests act as one roster human with no login, on /ui and /me.
		var dev *browserauth.DevIdentity
		if d := cfg.DevIdentity; d != nil {
			dev = &browserauth.DevIdentity{Store: st, Project: d.Project, Human: d.Human}
			gate.LoopbackTrust = dev.OperatorLoopbackTrust()
			log.Warn("DEV IDENTITY ACTIVE: loopback browser requests act as a roster human with no login; never use in production",
				"project", d.Project, "human", d.Human)
		}

		// Participant intercom plane (/me): mapped to a roster human (no operator
		// scope) by browser OIDC login and/or the dev identity. Unlike /ui there
		// is NO loopback trust: we must know which human, and loopback cannot say
		// (except under the explicit dev identity).
		var meHandler http.Handler
		meMux := http.NewServeMux()
		var meSession func(*http.Request) (*http.Request, browserauth.SessionOutcome)
		meLoginPath := ""
		if bc := cfg.browserAuthConfig(); bc != nil {
			svc, err := browserauth.New(context.Background(), *bc, browserauth.OperatorUIMount(), nil, log)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam: browser login:", err)
				return 1
			}
			uiMux.Handle("/ui/auth/", svc.Routes())
			if oidcAuth, ok := auth.(*jam.OIDCAuthenticator); ok {
				gate.Session = browserauth.OperatorSession(oidcAuth, browserauth.OperatorUIMount().SessionCookie)
			}
			log.Info("Jam UI auth: browser OIDC login", "client-id", bc.ClientID)

			// The SAME OIDC client as /ui; the login routes (/me/auth/*) stay ungated.
			meSvc, err := browserauth.New(context.Background(), *bc, browserauth.ParticipantMount(), nil, log)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam: participant login:", err)
				return 1
			}
			meMux.Handle("/me/auth/", meSvc.Routes())
			meSession = meSvc.ParticipantSession(st)
			meLoginPath = "/me/auth/login"
			log.Info("Jam participant intercom: browser OIDC login", "client-id", bc.ClientID)
		} else {
			log.Info("Jam UI auth: loopback-only")
		}
		if dev != nil {
			meSession = dev.ParticipantSession(meSession)
		}
		if meSession != nil {
			meGate := browserauth.Gate{
				Session:       meSession,
				LoginPath:     meLoginPath,
				ExpectedHosts: cfg.UIHosts,
				Log:           log,
			}
			// The gated participant surface: the landing view and the send
			// endpoint (POST /me/send), the human analog of the agent send tool.
			// It writes to the same intercom Log the agent send + relay ingress
			// use, so a reply wakes a waiting studio.
			meSurface := http.NewServeMux()
			sendH := jam.NewParticipantSendHandler(st, intercomLog, log)
			meSurface.Handle("/me/send", sendH)
			// The inbox reads the same intercom Log.
			var meLog jam.LogReader = intercomLog
			var meOpts []meui.Option
			meOpts = append(meOpts, meui.WithChanges(logChanges), meui.WithPresence(sessPresence))
			meSurface.Handle("/me/", meui.Handler(st, meLog, log, meOpts...))
			meMux.Handle("/me/", meGate.Wrap(meSurface))
			meHandler = meMux
			log.Info("Jam participant intercom: inbox + send mounted", "path", "/me/")
		}
		uiMux.Handle("/ui/", gate.Wrap(adminui.Handler(st, log, sup, personalAllocator{alloc}, credExists, squawkReader, adminui.WithTrustedOrigins(cfg.UIOrigins...), adminui.WithSessions(sessStore, sessHub), adminui.WithCredentialNames(cfg.credNames()...), adminui.WithPoolConfigured(cfg.Pool != nil))))

		admin := jam.NewAdminHandler(st, sup, personalAllocator{alloc}, auth, credExists, cfg.operatorLoginConfig(), log, uiMux, meHandler,
			jam.WithAdminRoute("GET /admin/sessions/{actor_id}/events", sessionevents.ExportHandler(sessStore)),
			jam.WithModelSpecs(st, credExists, cfg.Pool != nil, log))
		go func() {
			if cfg.adminUsesTLS() {
				cert, key, _ := cfg.adminTLS()
				log.Info("Jam admin API listening (TLS)", "addr", cfg.AdminListen)
				if err := (&http.Server{Addr: cfg.AdminListen, Handler: admin}).ListenAndServeTLS(cert, key); err != nil {
					log.Error("admin API stopped", "err", err.Error())
				}
				return
			}
			log.Info("Jam admin API listening", "addr", cfg.AdminListen)
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
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	rawLis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	log.Info("Jam broker+Attach listening (mux)", "addr", cfg.Listen)
	if err := serveMux(rawLis, tlsCfg, gs, httpHandler); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	return 0
}

// logTailSeq returns the Seq of the last (newest) message in lg, or 0 when
// the Log is empty. Used to seed the egress low-water at cutover so already-
// delivered shadow history is skipped.
func logTailSeq(lg intercom.Store) int64 {
	seq, _ := lg.TailSeq()
	return seq
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

func main() {
	os.Exit(runAs(filepath.Base(os.Args[0]), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// runAs is the process entry: invokedAs is the basename the binary was run
// under. The deprecated at-harbor name (shipped as a copy of at-jam for one
// release) runs normally after a one-time warning. It also carries a
// pre-rename ~/.config/at-harbor/ across once. See
// docs/usage/jam/renamed-from-harbor.md.
func runAs(invokedAs string, argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if strings.TrimSuffix(invokedAs, ".exe") == "at-harbor" {
		logging.Deprecated(stderr, "at-harbor", "at-jam")
	}
	migrateConfigDir(stderr)
	return run(argv, getenv, stdout, stderr)
}

// parseClaudeAiOauth extracts a PoolAccount from a claude .credentials.json (or a
// file containing just the claudeAiOauth block). expiresAt is epoch milliseconds.
func parseClaudeAiOauth(name string, data []byte) (jam.PoolAccount, error) {
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return jam.PoolAccount{}, fmt.Errorf("parse credentials: %w", err)
	}
	o := doc.ClaudeAiOauth
	if o.AccessToken == "" || o.RefreshToken == "" {
		return jam.PoolAccount{}, fmt.Errorf("credentials missing claudeAiOauth.accessToken/refreshToken")
	}
	var exp time.Time
	if o.ExpiresAt > 0 {
		exp = time.UnixMilli(o.ExpiresAt)
	}
	return jam.PoolAccount{Name: name, AccessToken: o.AccessToken, RefreshToken: o.RefreshToken, ExpiresAt: exp}, nil
}

// cmdPool manages the host-side subscription-OAuth account pool store directly
// (not via the admin API): add seeds a real account from a claude login's
// credentials, list shows account names and expiries (never token values).
func cmdPool(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam pool: expected add|list")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("pool "+sub, flag.ContinueOnError)
	store := fs.String("store", "", "path to the pool JSON store (required)")
	name := fs.String("name", "", "account name (add)")
	fromFile := fs.String("from-file", "", "file holding the claudeAiOauth credentials block; default stdin (add)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	_ = pos
	if !ok {
		return code
	}
	if *store == "" {
		fmt.Fprintln(stderr, "at-jam pool: --store is required")
		return 2
	}
	ps, err := jam.NewFilePoolStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam pool:", err)
		return 1
	}
	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "at-jam pool add: --name is required")
			return 2
		}
		var data []byte
		if *fromFile == "" {
			if data, err = io.ReadAll(os.Stdin); err != nil {
				fmt.Fprintln(stderr, "at-jam pool add: read stdin:", err)
				return 1
			}
		} else if data, err = os.ReadFile(*fromFile); err != nil {
			fmt.Fprintln(stderr, "at-jam pool add:", err)
			return 1
		}
		acct, err := parseClaudeAiOauth(*name, data)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam pool add:", err)
			return 1
		}
		if err := ps.SetAccount(acct); err != nil {
			fmt.Fprintln(stderr, "at-jam pool add:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added pool account", acct.Name) // never the tokens
	case "list":
		accts, err := ps.Accounts()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam pool:", err)
			return 1
		}
		for _, a := range accts {
			fmt.Fprintf(stdout, "%s\texpires %s\n", a.Name, a.ExpiresAt.Format(time.RFC3339))
		}
	default:
		fmt.Fprintln(stderr, "at-jam pool: unknown subcommand", sub)
		return 2
	}
	return 0
}

// planCredentials loads the protected credentials file, resolves every demanded
// credential to a secret.Spec keyed by name, and fails closed if the serve config
// demands a credential the file does not supply. The broker and every downstream
// resolver index the returned map by credential name.
func planCredentials(cfg serveConfig) (map[string]secret.Spec, error) {
	path := cfg.credentialsFilePath()
	store, err := usersecret.LoadFlat(path)
	if err != nil {
		return nil, fmt.Errorf("credentials file: %w", err)
	}
	demanded := cfg.demandedCredentials()
	specSlice, unresolved, err := store.PlanFlat(demanded, mint.Expander(runner.OS{}, store.Global, ""))
	if err != nil {
		return nil, err
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("credential(s) demanded in the serve config but not supplied by %s: %s", path, strings.Join(unresolved, ", "))
	}
	specs := make(map[string]secret.Spec, len(specSlice))
	for _, s := range specSlice {
		specs[s.Name] = s
	}
	return specs, nil
}

// validatePushedKit validates a studio-kit config before it is stored, so a
// malformed kit is caught at push, not at raise.
func validatePushedKit(data []byte) error {
	_, err := studio.ParseStudioKit(data)
	return err
}

// studioShowEgress returns the effective ceiling and the excluded (Anthropic)
// roots for display, so an operator sees exactly what a studio kit can reach.
func studioShowEgress(authored []string) (ceiling, excluded []string) {
	return studio.Ceiling(authored)
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}
