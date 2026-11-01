// Command jam is the central credential broker + control plane. `serve`
// runs the credential-injecting reverse proxy and a loopback admin API;
// `enroll`/`revoke`/`destination` are admin-API clients. See the Jam specs
// under docs/superpowers/specs/.
package main

import (
	"cmp"
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
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // IANA zones for role time zones, whatever the image ships

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/allocator/allocpg"
	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/discord"
	"github.com/aethons-tools/cove/internal/dispatcher"
	"github.com/aethons-tools/cove/internal/escalate"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercompg"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/apptoken"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/condition/conditionpg"
	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/deviceflow"
	"github.com/aethons-tools/cove/internal/jam/fleet"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/managers"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/notify"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessionpg"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/jam/sshca"
	"github.com/aethons-tools/cove/internal/jam/userui"
	"github.com/aethons-tools/cove/internal/jam/vscodeext"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/mint"
	"github.com/aethons-tools/cove/internal/mintcmd"
	"github.com/aethons-tools/cove/internal/relay"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/standing"
	"github.com/aethons-tools/cove/internal/studio"
	"github.com/aethons-tools/cove/internal/textesc"
	"github.com/aethons-tools/cove/internal/tracker"
	"github.com/aethons-tools/cove/internal/usersecret"
	"github.com/aethons-tools/cove/internal/wakeon"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const defaultAdminURL = "http://127.0.0.1:8081"

// commandName is the command cli.App.Run would dispatch argv to: the first
// argument past the leading global flags ("" when there is none).
func commandName(argv []string) string {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			if i+1 < len(argv) {
				return argv[i+1]
			}
			return ""
		case !strings.HasPrefix(a, "-"):
			return a
		case !strings.Contains(a, "="):
			// The globals that take a value as the next argument.
			switch strings.TrimLeft(a, "-") {
			case "log-mode", "log-level":
				i++
			}
		}
	}
	return ""
}

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if commandName(argv) == "colima" {
		fmt.Fprintln(stderr, `jam: colima moved to jam-studio-manager: run "jam-studio-manager colima setup-docker|check-docker"`)
		return 2
	}
	app := cli.App{
		Name:    "jam",
		Version: version + basedigest.VersionNote(),
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the broker + loopback admin API", Run: cmdServe},
			{Name: "enroll", Brief: "enroll an identity (via the admin API) and print its snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "revoke an identity (via the admin API)", Run: cmdRevoke},
			{Name: "destination", Brief: "list destinations (list) via the admin API", Run: cmdDestination},
			{Name: "attention", Brief: "list operator-attention conditions (list [--all]) via the admin API", Run: cmdAttention},
			{Name: "manager", Brief: "manage Studio Managers (add|list|rotate|revoke) via the admin API", Run: cmdManager},
			{Name: "model-spec", Brief: "show model-specs — how a cove runs its agent: harness, version, principal, model, policy (list|show) via the admin API", Run: cmdModelSpec},
			{Name: "role", Brief: "list and inspect roles (list|inspect|images) via the admin API", Run: cmdRole},
			{Name: "project", Brief: "list projects (list), or show a project's members (member list), escalation policy (escalation list), or chat service (chat-service show) via the admin API", Run: cmdProject},
			{Name: "user", Brief: "manage users — the people agents talk to (list|show|oidc) via the admin API", Run: cmdUser},
			{Name: "account", Brief: "manage users' accounts on connected services (list|add|link|unlink) via the admin API", Run: cmdAccount},
			{Name: "room", Brief: "list a project's rooms — named channels on a Linear issue or a Discord channel (list) via the admin API", Run: cmdRoom},
			{Name: "connection", Brief: "list connections to external services — a Linear workspace, a Discord bot (list) via the admin API", Run: cmdConnection},
			{Name: "kit", Brief: "manage the kit registry (list|show|versions|diff|import|track|untrack|sync)", Run: cmdKit},
			{Name: "config", Brief: "keep the Jam's configuration in git: write it out (write <dir> [--all]); plan a checkout or a git source, never applying it (diff [--dir <path> | --git <src>]); apply the followed repo (sync [--yes]); show the follower (status) via the admin API", Run: cmdConfig},
			{Name: "export", Brief: "export the Jam config (actors, roles, kits, destinations, model-specs, projects) to a file (or stdout) via the admin API", Run: cmdExport},
			{Name: "import", Brief: "import a Jam config backup into an EMPTY Jam via the admin API (refuses if config already exists)", Run: cmdImport},
			{Name: "pool", Brief: "manage the subscription-OAuth account pool (add|list) — writes the host-side pool store", Run: cmdPool},
			{Name: "grant", Brief: "grant a role to an actor", Run: cmdGrant},
			{Name: "ungrant", Brief: "remove a role grant from an actor", Run: cmdUngrant},
			{Name: "actors", Brief: "list actors and their grants", Run: cmdActors},
			{Name: "studio", Brief: "manage studios (raise|list|status|teardown|inspect) via the admin API, and who may reach one (share|unshare|access)", Run: cmdStudio},
			{Name: "cove", Brief: "deprecated alias for studio", Run: cmdCove},
			{Name: "standing", Brief: "list, reset or upgrade a role's named standing sessions (list|reset|upgrade) via the admin API", Run: cmdStanding},
			{Name: "egress", Brief: "show a role's raw-egress policy (show) via the admin API", Run: cmdEgress},
			{Name: "context", Brief: "show the authored session context for a role, project or the Jam (show) via the admin API", Run: cmdContext},
			{Name: "session", Brief: "request, list or release your personal sessions (request|list|release) via the admin API", Run: cmdSession},
			{Name: "push", Brief: "push notification helpers (keygen: print a fresh VAPID key pair)", Run: func(args []string, _ cli.Globals, stdout, stderr io.Writer) int { return cmdPush(args, stdout, stderr) }},
			{Name: "ssh-config", Brief: "print (or --write into ~/.ssh/config) a Host jam-<studio> block for SSH into a studio, fetching its certificate", Run: cmdSSHConfig},
			{Name: "ssh-proxy", Brief: "the ssh ProxyCommand: renew the studio's SSH certificate when under 1 h remains, then tunnel stdio through Jam (OpenSSH reads the certificate before running it, so a renewal applies from the next connection)", Run: cmdSSHProxy},
			{Name: "ssh", Brief: "SSH into a studio: renew its certificate if needed, then exec ssh jam-<studio> with any extra ssh args", Run: cmdSSH},
			{Name: "login", Brief: "sign in via OIDC device flow and cache the operator token", Run: cmdLogin},
			{Name: "logout", Brief: "clear the cached operator token", Run: cmdLogout},
			{Name: "whoami", Brief: "show the cached operator identity", Run: cmdWhoami},
			{Name: "mint", Brief: "mint a short-lived token (github | anthropic) — what Jam runs for a minter credential", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return mintcmd.Run(a, version, os.LookupEnv, http.DefaultClient, out, errw)
			}},
			{Name: "update", Brief: "update the Jam installation (jam, jam-studio-manager) to the latest release", Run: cmdUpdate(getenv)},
			{Name: "vscode", Brief: "install the Jam VS Code extension this Jam serves (install), with `code --install-extension`; it then offers its own updates", Run: cmdVSCode},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdLogin(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile (from ~/.config/jam/settings.yml)")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL; persisted to the app's settings when given")
	participant := fs.Bool("participant", false, "sign in as a participant (a Jam user, not an operator) and cache the OIDC ID token for the /me routes")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "jam login: unexpected arguments")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam login:", err)
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
			fmt.Fprintln(stderr, "jam login: could not save settings:", err)
			return 1
		}
	}
	lc, err := adminclient.New(adminURL, "").LoginConfig()
	if err != nil {
		if errors.Is(err, adminclient.ErrNotFound) {
			if *participant {
				fmt.Fprintln(stderr, "jam login: this Jam has no device login configured; participant login is unavailable")
				return 1
			}
			fmt.Fprintln(stdout, "this Jam is not OIDC-gated; no login needed")
			return 0
		}
		fmt.Fprintln(stderr, "jam login:", err)
		return 1
	}
	// The participant bearer is the OIDC ID token, which only scope openid mints.
	if *participant && !slices.Contains(strings.Fields(lc.Scope), "openid") {
		fmt.Fprintf(stderr, "jam login: this Jam's device login scope %q lacks openid, so it mints no ID token; participant login is unavailable\n", lc.Scope)
		return 1
	}
	ctx := context.Background()
	dc, err := deviceflow.RequestDeviceCode(ctx, http.DefaultClient, deviceflow.Config{
		Issuer: lc.Issuer, Audience: lc.Audience, ClientID: lc.ClientID, Scope: lc.Scope,
	})
	if err != nil {
		fmt.Fprintln(stderr, "jam login:", err)
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
			err = fmt.Errorf("login timed out; run `jam login` again")
		}
		fmt.Fprintln(stderr, "jam login:", err)
		return 1
	}
	if *participant {
		return saveParticipantLogin(*app, adminURL, tok, stdout, stderr)
	}
	sub, exp, _ := parseJWTClaims(tok.AccessToken)
	if exp.IsZero() && tok.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if err := saveToken(*app, cachedToken{AccessToken: tok.AccessToken, Sub: sub, Expiry: exp, AdminURL: adminURL}); err != nil {
		fmt.Fprintln(stderr, "jam login:", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s; token expires %s\n", sub, exp.Format(time.RFC3339))
	return 0
}

// saveParticipantLogin caches a participant login: the ID token alone (never
// printed), expiring at its own exp, under the app's participant profile.
func saveParticipantLogin(app, adminURL string, tok deviceflow.Token, stdout, stderr io.Writer) int {
	if tok.IDToken == "" {
		fmt.Fprintln(stderr, "jam login: the identity provider returned no ID token; participant login needs scope openid")
		return 1
	}
	sub, exp, err := parseJWTClaims(tok.IDToken)
	if err != nil || exp.IsZero() {
		fmt.Fprintln(stderr, "jam login: the identity provider's ID token is unreadable or carries no expiry")
		return 1
	}
	if err := saveTokenAt(participantTokenPath(app), cachedToken{IDToken: tok.IDToken, Sub: sub, Expiry: exp, AdminURL: adminURL}); err != nil {
		fmt.Fprintln(stderr, "jam login:", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as participant %s; token expires %s\n", sub, exp.Format(time.RFC3339))
	return 0
}

func cmdLogout(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	participant := fs.Bool("participant", false, "clear the cached participant token instead of the operator token")
	if _, code, ok := cli.ParseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam logout:", err)
		return 2
	}
	path := tokenPath(*app)
	if *participant {
		path = participantTokenPath(*app)
	}
	if err := clearTokenAt(path); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
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
		fmt.Fprintln(stderr, "jam whoami:", err)
		return 2
	}
	if t, ok := loadToken(*app); ok {
		fmt.Fprintf(stdout, "%s @ %s (expires %s)\n", t.Sub, t.AdminURL, t.Expiry.Format(time.RFC3339))
		return 0
	}
	if _, err := os.Stat(tokenPath(*app)); err == nil {
		fmt.Fprintln(stdout, "session expired; run `jam login`")
	} else {
		fmt.Fprintln(stdout, "not logged in")
	}
	return 0
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: JAM_ADMIN_TOKEN)")
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
		fmt.Fprintln(stderr, "jam enroll:", err)
		return 2
	}
	settings := loadSettings(*app)
	adminURL := firstNonEmpty(*adminURLFlag, settings.AdminURL, defaultAdminURL)
	baseURL := firstNonEmpty(*baseURLFlag, settings.BaseURL)
	// --base-url is only needed for the printed snippet; --json omits it.
	if len(pos) > 0 || *id == "" || (!*jsonOut && baseURL == "") {
		fmt.Fprintln(stderr, "jam enroll: --id (and --base-url unless --json) are required")
		return 2
	}
	res, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
	})
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if *jsonOut {
		// Machine-readable output for scripted enrollment (COV-141). The token
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
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam revoke:", err)
		return 2
	}
	if len(pos) > 0 || *id == "" {
		fmt.Fprintln(stderr, "jam revoke: --id is required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	if err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Revoke(*id); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
	return 0
}

func cmdDestination(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam destination: expected list")
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub != "list" {
		fmt.Fprintf(stderr, "jam destination: unknown subcommand %q (want list)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("destination "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: JAM_ADMIN_TOKEN)")
	_, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam destination:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		ds, err := c.ListDestinations()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, dd := range ds {
			ob := ""
			if len(dd.Env) > 0 {
				ob += ", env=" + strings.Join(slices.Sorted(maps.Keys(dd.Env)), ",")
			}
			if len(dd.AllowPaths) > 0 {
				ob += fmt.Sprintf(", %d allow-paths", len(dd.AllowPaths))
			}
			if dd.Git {
				ob += ", git"
			}
			if dd.Note != "" {
				ob += ", note"
			}
			if dd.LogHeadersIn != "" && dd.LogHeadersIn != jam.HeaderLogNone || dd.LogHeadersOut != "" && dd.LogHeadersOut != jam.HeaderLogNone {
				ob += fmt.Sprintf(", log-headers=%s/%s", cmp.Or(dd.LogHeadersIn, jam.HeaderLogNone), cmp.Or(dd.LogHeadersOut, jam.HeaderLogNone))
			}
			fmt.Fprintf(stdout, "%s\t%s\t-> %s\t(cred %q, %s%s)\n", dd.Name, dd.Route, dd.Upstream, dd.CredName, dd.Apply, ob)
		}
	default:
		fmt.Fprintf(stderr, "jam destination: unknown subcommand %q (want list)\n", sub)
		return 2
	}
	return 0
}

func cmdRole(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam role: expected list|inspect|images")
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub == "inspect" {
		return cmdRoleInspect(rest, stdout, stderr)
	}
	if sub != "list" && sub != "images" {
		fmt.Fprintf(stderr, "jam role: unknown subcommand %q (want list|inspect|images)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("role "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam role:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		roles, err := c.ListRoles(*project)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, r := range roles {
			fmt.Fprintf(stdout, "%s\tdests=%s\tmodel-spec=%s\taddressing=%s\tttl=%s\tmax-ephemeral=%d\tmax-personal=%d\tmax-personal-per-owner=%d\tidle-after=%s\tnag-every=%s\treclaim-after=%s\tidle-timeout=%s\ton-idle=%s\ttime-zone=%s\tegress=%s\tcollaborate-preview=%s\tcollaborate-ssh=%s\n", r.Name, jam.FormatDestinations(r.Scope.Destinations, r.Scope.Credentials), r.ModelSpecName(), strings.Join(r.Scope.Addressing, ","), r.Scope.TTL, r.Allocation.MaxEphemeral, r.Allocation.MaxPersonal, r.Allocation.MaxPersonalPerOwner, r.Allocation.IdleAfter, r.Allocation.NagEvery, r.Allocation.ReclaimAfter, r.TurnEnd.IdleTimeout, r.TurnEnd.Action(), r.TurnEnd.Location(), egressState(r.Scope.Egress), collabLabel(r.Collaborate.Preview, "members"), collabLabel(r.Collaborate.SSH, "none"))
		}
	case "images":
		if len(pos) != 1 || pos[0] == "" {
			fmt.Fprintln(stderr, "usage: jam role images <project>/<role>")
			return 2
		}
		proj, role, found := strings.Cut(pos[0], "/")
		if !found {
			proj, role = jam.DefaultProject, pos[0]
		}
		rows, err := c.RoleImages(proj, role)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, r := range rows {
			kit := "-"
			if r.HasKit {
				kit = r.Kit.ID
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", firstNonEmpty(r.Manager, jam.LocalManager), kit, firstNonEmpty(r.Tag, "-")) // "" = legacy record with no manager (the old local manager)
		}
	default:
		fmt.Fprintln(stderr, "jam role: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProject lists projects, and shows a project's members, escalation policy
// and chat service via the admin API. Member subcommands nest under "member"
// (cmdProjectMember): `project member list`. (A project's channels
// are rooms: cmdRoom.)
// Escalation subcommands nest under "escalation" and are handled by
// cmdProjectEscalation: `project escalation list`. Chat-service
// subcommands nest under "chat-service" and are handled by
// cmdProjectChatService: `project chat-service show`.
func cmdProject(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam project: expected list, member list, escalation list, or chat-service show (a project's channels are rooms: jam room)")
		return 2
	}
	switch args[0] {
	case "list":
		return cmdProjectLifecycle(args[0], args[1:], stdout, stderr)
	case "escalation":
		return cmdProjectEscalation(args[1:], stdout, stderr)
	case "chat-service":
		return cmdProjectChatService(args[1:], stdout, stderr)
	case "member":
		return cmdProjectMember(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "jam project: unknown subcommand %q (want list|member|escalation|chat-service)\n", args[0])
	return 2
}

// cmdProjectEscalation manages a project's escalation policy via the admin
// API: `project escalation list`.
func cmdProjectEscalation(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam project escalation: expected list")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project escalation "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam project escalation:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam project escalation list: expected one project name")
			return 2
		}
		v, err := c.GetEscalationPolicy(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
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
	default:
		fmt.Fprintln(stderr, "jam project escalation: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdProjectLifecycle lists projects via the admin API: `project list`.
func cmdProjectLifecycle(sub string, rest []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintf(stderr, "jam project %s: unexpected arguments\n", sub)
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam project:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		names, err := c.ListProjects()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, n := range names {
			fmt.Fprintln(stdout, n)
		}
	}
	return 0
}

// cmdProjectChatService manages a project's chat service (the service backing
// its humans' DMs) via the admin API: `project chat-service show`.
func cmdProjectChatService(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam project chat-service: expected show")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project chat-service "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "jam project chat-service: unexpected arguments")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam project chat-service:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "show":
		if *project == "" {
			fmt.Fprintln(stderr, "jam project chat-service show: expected --project")
			return 2
		}
		svc, err := c.GetChatService(*project)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		if svc == "" {
			svc = "(none)"
		}
		fmt.Fprintln(stdout, svc)
	default:
		fmt.Fprintln(stderr, "jam project chat-service: unknown subcommand", sub)
		return 2
	}
	return 0
}

// managerCol is a studio list line's trailing "\tmanager=<name>", empty for a
// studio raised before managers existed (its line keeps its old shape).
func managerCol(m string) string {
	if m == "" {
		return ""
	}
	return "\tmanager=" + m
}

// multiFlag collects repeatable string flag values (e.g. --delivery
// service:address, repeatable).
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func cmdKit(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam kit: expected list|show|versions|diff|import|track|untrack|sync")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kit "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	name := fs.String("name", "", "kit name")
	version := fs.Int("version", 0, "kit version (show; 0 = current)")
	gitSrc := fs.String("git", "", "git source <repo>[//dir]@<ref>; import and track only")
	from := fs.Int("from", 0, "diff: the older version (default: the one before --to)")
	to := fs.Int("to", 0, "diff: the newer version (default: current)")
	own := fs.Bool("own", false, "show: print the kit's own stored YAML instead of the merged (effective) kit")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam kit:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "diff":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam kit diff: expected one kit name")
			return 2
		}
		for _, f := range []struct {
			key string
			n   int
		}{{"from", *from}, {"to", *to}} {
			if f.n < 0 {
				fmt.Fprintln(stderr, "jam kit diff: "+f.key+" must be a version number")
				return 2
			}
		}
		d, err := c.KitDiff(pos[0], *from, *to)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s v%d → v%d\n%s", textesc.Escape(d.Name, false), d.From, d.To, textesc.Escape(d.Text, true))
	case "list":
		kits, err := c.ListKits()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, k := range kits {
			fmt.Fprintf(stdout, "%s\tcurrent=v%d\tversions=%d", k.Name, k.Current, k.Versions)
			if k.Parent != "" {
				fmt.Fprintf(stdout, "\tparent=%s", k.Parent)
			}
			fmt.Fprintln(stdout)
		}
	case "show":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam kit show: expected one kit name")
			return 2
		}
		res, err := c.GetKit(pos[0], *version)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		// Stored form is canonical JSON; render it back as YAML for the human.
		// ParseStudioKit accepts JSON (and any YAML rows) alike. The default
		// view is the server's effective (merged) YAML; an older server sends
		// none, so show the stored kit.
		// --own is the server's own view (note files read from the tree into
		// their bodies) when it sends one, else the stored kit.yml.
		shown := res.Config
		switch {
		case res.Effective != "" && !*own:
			shown = res.Effective
		case res.Own != "":
			shown = res.Own
		}
		if shown == "" && res.ChainError != "" {
			// A broken version has no kit.yml: its own fixed error says why.
			fmt.Fprintln(stderr, "jam kit show:", res.ChainError)
			return 1
		}
		sk, err := studio.ParseStudioKit([]byte(shown))
		if err != nil {
			fmt.Fprintln(stderr, "jam kit show: stored config is not a valid studio kit:", err)
			return 1
		}
		sk.LegacyName = "" // the registry names the kit; a legacy row's name isn't shown
		y, err := yaml.Marshal(sk)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprint(stdout, kitSourceHeader(res))
		if res.ChainError != "" {
			fmt.Fprintf(stdout, "# chain error: %s\n", res.ChainError)
		}
		fmt.Fprint(stdout, kitBuiltOnHeader(res))
		if len(res.Chain) > 0 && res.Effective != "" && !*own {
			fmt.Fprintln(stdout, "# effective view (base.kit flattened); use --own for the kit.yml to commit")
		}
		fmt.Fprint(stdout, string(y))
		if len(res.Files) > 0 {
			fmt.Fprintf(stdout, "# files (tree %s):\n", shortCommit(res.Tree))
			for _, f := range res.Files {
				fmt.Fprintf(stdout, "#   %s  %8d  %s\n", f.Mode, f.Size, f.Path)
			}
		}
		ceiling, excluded := studioShowEgress(sk.Egress)
		fmt.Fprintf(stdout, "egress ceiling: %s\n", joinOrNone(ceiling))
		fmt.Fprintf(stdout, "excluded (COV-208): %s\n", joinOrNone(excluded))
	case "versions":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam kit versions: expected one kit name")
			return 2
		}
		hist, err := c.KitHistory(pos[0])
		var se *adminclient.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			// An older Jam has no /history: fall back to the bare version list.
			vers, verr := c.KitVersions(pos[0])
			if verr != nil {
				fmt.Fprintln(stderr, "jam:", verr)
				return 1
			}
			for _, v := range vers {
				fmt.Fprintf(stdout, "v%d\n", v)
			}
			break
		}
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tCURRENT\tPUSHED\tSOURCE\tCOMMIT")
		for _, h := range hist {
			cur, pushed, srcText, commit := "", "-", "local", "-"
			if h.Current {
				cur = "*"
			}
			if !h.PushedAt.IsZero() {
				pushed = h.PushedAt.UTC().Format(time.RFC3339)
			}
			if h.Source != nil {
				srcText, commit = kitSourceText(h.Source), shortCommit(h.Source.Commit)
			}
			if h.Source == nil && h.Origin != "" {
				srcText = textesc.Escape(h.Origin, false) // "draft": published from a draft
			}
			fmt.Fprintf(tw, "v%d\t%s\t%s\t%s\t%s\n", h.Version, cur, pushed, srcText, commit)
		}
		_ = tw.Flush()
	case "import", "track":
		want := 0
		if sub == "track" {
			want = 1
		}
		if len(pos) != want || *gitSrc == "" {
			if sub == "track" {
				fmt.Fprintln(stderr, "jam kit track: expected <name> --git <repo>[//dir]@<ref>")
			} else {
				fmt.Fprintln(stderr, "jam kit import: expected --git <repo>[//dir]@<ref> [--name N]")
			}
			return 2
		}
		src, err := gitsource.Parse(*gitSrc)
		if err != nil {
			fmt.Fprintf(stderr, "jam kit %s: %v\n", sub, err)
			return 2
		}
		var res jam.KitSourceResult
		if sub == "track" {
			res, err = c.TrackKit(pos[0], src.String())
		} else {
			res, err = c.ImportKit(*name, src.String())
		}
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		printKitSetAncestors(stdout, res)
		if res.Unchanged {
			fmt.Fprintf(stdout, "%s unchanged (current v%d, %s)\n", res.Name, res.Version, shortCommit(res.Commit))
		} else {
			fmt.Fprintf(stdout, "imported %s v%d from %s (%s)\n", res.Name, res.Version, src, shortCommit(res.Commit))
		}
		if sub == "track" {
			fmt.Fprintf(stdout, "tracking %s\n", src)
		}
	case "untrack":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam kit untrack: expected one kit name")
			return 2
		}
		if err := c.UntrackKit(pos[0]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s no longer tracks a source\n", pos[0])
	case "sync":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam kit sync: expected one kit name")
			return 2
		}
		res, err := c.SyncKit(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		printKitSetAncestors(stdout, res)
		if res.Unchanged {
			fmt.Fprintf(stdout, "%s unchanged (%s)\n", res.Name, shortCommit(res.Commit))
		} else {
			fmt.Fprintf(stdout, "synced %s v%d (%s)\n", res.Name, res.Version, shortCommit(res.Commit))
		}
	default:
		fmt.Fprintln(stderr, "jam kit: unknown subcommand", sub)
		return 2
	}
	return 0
}

// printKitSetAncestors prints one indented line per ancestor kit of a set
// import, track or sync (root first), ahead of the kit's own result line.
func printKitSetAncestors(w io.Writer, res jam.KitSourceResult) {
	for _, a := range res.Also {
		if a.Unchanged {
			fmt.Fprintf(w, "  %s unchanged (v%d)\n", a.Name, a.Version)
		} else {
			fmt.Fprintf(w, "  %s v%d (with %s)\n", a.Name, a.Version, res.Name)
		}
	}
}

// shortCommit is the first 12 characters of a commit id.
func shortCommit(c string) string {
	if c == "" {
		return "-"
	}
	return c[:min(len(c), 12)]
}

// kitSourceText renders a kit source as <repo>[//dir]@<ref>.
func kitSourceText(s *jam.KitSource) string {
	return gitsource.Source{Repo: s.Repo, Dir: s.Dir, Ref: s.Ref}.String()
}

// kitSourceHeader is the leading comment lines of `kit show`: where the shown
// version came from and, for a tracked kit, the tracked source.
func kitSourceHeader(res jam.KitConfigResult) string {
	h := "# source: local\n"
	if res.Source != nil {
		h = fmt.Sprintf("# source: %s (%s)\n", kitSourceText(res.Source), shortCommit(res.Source.Commit))
	}
	if res.Tracking != nil {
		h += fmt.Sprintf("# tracking: %s (last synced %s)\n", kitSourceText(res.Tracking), shortCommit(res.Tracking.Commit))
	}
	return h
}

// kitBuiltOnHeader is the "# built on:" line of a chained kit: the chain root
// first, the direct parent annotated with how it is tracked. "" when unchained.
func kitBuiltOnHeader(res jam.KitConfigResult) string {
	if len(res.Chain) == 0 {
		return ""
	}
	parts := make([]string, 0, len(res.Chain))
	for _, l := range res.Chain[:len(res.Chain)-1] {
		parts = append(parts, fmt.Sprintf("%s@v%d", l.Name, l.Version))
	}
	last := res.Chain[len(res.Chain)-1]
	if !last.Follow {
		parts = append(parts, fmt.Sprintf("%s@v%d (pinned)", last.Name, last.Version))
	} else {
		note := fmt.Sprintf("follows, now v%d", last.Version)
		if res.Parent != nil {
			note += fmt.Sprintf("; pushed against v%d", res.Parent.Version)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", last.Name, note))
	}
	return "# built on: " + strings.Join(parts, " → ") + "\n"
}

// readConfig reads a config file path, or stdin when path == "-".
func readConfig(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// cmdCove is the deprecated `jam cove …` verb: it warns once and runs
// `jam studio …` (docs/usage/jam/renamed-from-harbor.md).
func cmdCove(args []string, g cli.Globals, stdout, stderr io.Writer) int {
	logging.Deprecated(stderr, "jam cove", "jam studio")
	return cmdStudio(args, g, stdout, stderr)
}

// cmdStudio manages studios (managed coves — the entity the UI and CLI call a
// Studio; ids, routes and the admin API still say "cove").
func cmdStudio(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam studio: expected raise|list|status|teardown|inspect|share|unshare|access")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "inspect":
		return cmdStudioInspect(rest, stdout, stderr)
	case "share", "unshare", "access":
		return cmdStudioShare(sub, rest, stdout, stderr)
	}
	fs := flag.NewFlagSet("studio "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "studio/actor id")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role to raise the studio for")
	unit := fs.String("unit", "", "unit of work (e.g. issue identifier)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the workload prompt (raise only; read host-side, never passed on argv)")
	activity := fs.String("activity", "", "reported activity: running|holding|waiting|blocked|done (status only)")
	var managers multiFlag
	fs.Var(&managers, "manager", "raise only: a manager the studio may land on, in preference order (repeatable; default any)")
	placementTimeout := fs.String("placement-timeout", "", "raise only: how long to look for a manager that accepts, e.g. 2m (default: do not wait)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam studio:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "raise":
		if *id == "" || *role == "" {
			fmt.Fprintln(stderr, "jam studio raise: --id and --role are required")
			return 2
		}
		var prompt string
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fmt.Fprintln(stderr, "jam studio raise: --prompt-file:", err)
				return 1
			}
			prompt = string(b)
		}
		res, err := c.RaiseCove(adminclient.CoveRaiseParams{ID: *id, Project: *project, Role: *role, Unit: *unit, Prompt: prompt, Managers: managers, PlacementTimeout: *placementTimeout})
		if err != nil {
			fmt.Fprintln(stderr, "jam:", textesc.Escape(err.Error(), false))
			return 1
		}
		fmt.Fprintf(stdout, "raised %s as session %s (phase=%s)\n", res.Label, res.ID, res.Phase)
	case "list":
		coves, err := c.ListCoves()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, cv := range coves {
			fmt.Fprintf(stdout, "%s\tname=%s\trole=%s\tunit=%s\tphase=%s\tactivity=%s\tholder=%s\tconnector=%s\timage=%s%s\n",
				cv.ID, cv.Name, cv.Role, cv.Unit, cv.Phase, cv.Activity, cv.LeaseHolder, cv.Connector, imageOrUnknown(cv.Image), managerCol(cv.Manager))
		}
	case "status":
		if *id == "" || *activity == "" {
			fmt.Fprintln(stderr, "jam studio status: --id and --activity are required")
			return 2
		}
		if err := c.ReportCoveStatus(*id, *activity); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "reported %s activity=%s\n", *id, *activity)
	case "teardown":
		name := *id
		if name == "" && len(pos) == 1 {
			name = pos[0]
		}
		if name == "" {
			fmt.Fprintln(stderr, "jam studio teardown: --id (or a positional id) is required")
			return 2
		}
		if err := c.TeardownCove(name); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "tore down", name)
	default:
		fmt.Fprintln(stderr, "jam studio: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdSession requests, lists and releases the caller's personal sessions. The
// caller is the project member whose user holds the login (a user's login) of the
// operator identity the admin API authenticates.
func cmdSession(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam session: expected request|list|release")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("session "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role to request a personal session of (request only)")
	promptFile := fs.String("prompt-file", "", "path to a file containing the session's prompt (request only; read host-side, never passed on argv)")
	var managers multiFlag
	fs.Var(&managers, "manager", "request only: a manager the session may land on, in preference order (repeatable; default any)")
	placementTimeout := fs.String("placement-timeout", "", "request only: how long to look for a manager that accepts, e.g. 2m (default: do not wait)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam session:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "request":
		if *role == "" {
			fmt.Fprintln(stderr, "jam session request: --role is required")
			return 2
		}
		var prompt string
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fmt.Fprintln(stderr, "jam session request: --prompt-file:", err)
				return 1
			}
			prompt = string(b)
		}
		res, err := c.RequestPersonalSession(jam.PersonalSessionBody{Project: *project, Role: *role, Prompt: prompt, Managers: managers, PlacementTimeout: *placementTimeout})
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, res.ID)
	case "list":
		sessions, err := c.ListPersonalSessions(*project)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, s := range sessions {
			fmt.Fprintf(stdout, "%s\trole=%s\tphase=%s\tactivity=%s\traised=%s\n",
				s.ID, s.Role, s.Phase, s.Activity, s.RaisedAt.Format(time.RFC3339))
		}
	case "release":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam session release: expected one session id")
			return 2
		}
		if err := c.ReleasePersonalSession(pos[0]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "released", pos[0])
	default:
		fmt.Fprintln(stderr, "jam session: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdStanding lists a role's standing-session declarations (which change only
// in git) and acts on them: reset deletes a declared name's cove and state so
// Jam raises it afresh; upgrade re-raises it on the current image, keeping its
// state.
func cmdStanding(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam standing: expected list|reset|upgrade")
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub != "list" && sub != "reset" && sub != "upgrade" {
		fmt.Fprintf(stderr, "jam standing: unknown subcommand %q (want list|reset|upgrade)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("standing "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role the standing session belongs to")
	force := fs.Bool("force", false, "upgrade only: don't wait for the session to be idle, and restart it even if already current")
	wait := fs.Bool("wait", false, "upgrade only: poll `standing list` until the upgrade is done")
	waitTimeout := fs.Duration("wait-timeout", 15*time.Minute, "upgrade --wait only: give up after this long")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam standing:", err)
		return 2
	}
	if *role == "" && (sub == "list" || sub == "reset" || sub == "upgrade") {
		fmt.Fprintf(stderr, "jam standing %s: --role is required\n", sub)
		return 2
	}
	proj := firstNonEmpty(*project, jam.DefaultProject)
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		list, err := c.ListStanding(proj, *role)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		// The running status is best-effort: the declarations still print
		// without it (phase/image "-").
		coves, err := c.ListRoleCoves(proj, *role)
		if err != nil {
			fmt.Fprintln(stderr, "jam: warning: studio status unavailable:", err)
		}
		running := map[string]jam.CoveSummary{}
		for _, cv := range coves {
			running[cv.ID] = cv
		}
		for _, s := range list {
			id := s.SessionID
			phase, image := "-", "-" // no studio running for this name
			if id == "" {
				id = "-" // never raised: no session yet
			}
			if cv, ok := running[id]; ok {
				phase, image = cv.Phase, imageOrUnknown(cv.Image)
			}
			line := fmt.Sprintf("%s\tid=%s\tphase=%s\timage=%s", s.Name, id, phase, image)
			if s.Upgrade != "" {
				line += "\tupgrade=" + s.Upgrade
			}
			fmt.Fprintln(stdout, line)
		}
	case "reset":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam standing reset: expected one standing session name")
			return 2
		}
		res, err := c.ResetStanding(proj, *role, pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		if res.Pending {
			fmt.Fprintf(stdout, "reset of standing session %s pending: %s\n", pos[0], res.Reason)
			break
		}
		fmt.Fprintf(stdout, "reset standing session %s (cove and state deleted; Jam raises it fresh)\n", pos[0])
	case "upgrade":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam standing upgrade: expected one standing session name")
			return 2
		}
		res, err := c.UpgradeStanding(proj, *role, pos[0], *force)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		if !res.Pending {
			fmt.Fprintf(stdout, "standing session %s already runs the current image (%s); nothing restarted (--force restarts it anyway)\n", pos[0], imageOrUnknown(res.Image))
			break
		}
		if !*wait {
			fmt.Fprintf(stdout, "upgrade of standing session %s queued (%s); watch `jam standing list --project %s --role %s`\n", pos[0], res.State, proj, *role)
			break
		}
		return waitStandingUpgrade(c, proj, *role, pos[0], *waitTimeout, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "jam standing: unknown subcommand", sub)
		return 2
	}
	return 0
}

// standingWaitPoll is how often `standing upgrade --wait` polls (tests shorten it).
var standingWaitPoll = 2 * time.Second

// waitStandingUpgrade polls the standing list until name's pending upgrade is
// gone (printing each state change), then reports its studio's phase and
// image: exit 0 when it runs with image ok (or unknown, when Jam can't name
// images), 1 when it isn't running on a current image or the wait timed out.
func waitStandingUpgrade(c *adminclient.Client, proj, role, name string, timeout time.Duration, stdout, stderr io.Writer) int {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		list, err := c.ListStanding(proj, role)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		state := ""
		for _, s := range list {
			if s.Name == name {
				state = s.Upgrade
			}
		}
		if state == "" {
			break
		}
		if state != last {
			fmt.Fprintf(stdout, "upgrade of %s: %s\n", name, state)
			last = state
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "jam: timed out after %s waiting for the upgrade of %s (still %s); it stays queued\n", timeout, name, state)
			return 1
		}
		time.Sleep(standingWaitPoll)
	}
	coves, err := c.ListRoleCoves(proj, role)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	var id string
	if list, err := c.ListStanding(proj, role); err == nil {
		for _, s := range list {
			if s.Name == name {
				id = s.SessionID
			}
		}
	}
	phase, image := "-", "-"
	for _, cv := range coves {
		if cv.ID == id {
			phase, image = cv.Phase, imageOrUnknown(cv.Image)
		}
	}
	if phase != "-" && (image == "ok" || image == "unknown") {
		fmt.Fprintf(stdout, "upgraded standing session %s: phase=%s image=%s (conversation and workspace kept)\n", name, phase, image)
		return 0
	}
	fmt.Fprintf(stderr, "jam: upgrade of %s finished but its studio is phase=%s image=%s; see `jam standing list` and Jam's log\n", name, phase, image)
	return 1
}

// imageOrUnknown is a studio's image status for listings: an older Jam that
// doesn't report one prints unknown.
func imageOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
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

// cmdEgress shows a role's raw-egress policy: the list Jam pushes into each
// cove of the role at raise, replacing the kit's policy list within the kit's
// ceiling. The policy changes only in git.
func cmdEgress(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "jam egress: expected show")
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub != "show" {
		fmt.Fprintf(stderr, "jam egress: unknown subcommand %q (want show)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("egress "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role whose egress policy to manage")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam egress:", err)
		return 2
	}
	switch sub {
	case "show":
	default:
		fmt.Fprintln(stderr, "jam egress: unknown subcommand", sub)
		return 2
	}
	if *role == "" {
		fmt.Fprintf(stderr, "jam egress %s: --role is required\n", sub)
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "jam egress %s: unexpected arguments\n", sub)
		return 2
	}
	proj := firstNonEmpty(*project, jam.DefaultProject)
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "show":
		v, err := c.ShowEgress(proj, *role)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
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
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	id := fs.String("id", "", "actor id")
	project := fs.String("project", "", "project name (default: "+jam.DefaultProject+")")
	role := fs.String("role", "", "role name")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam "+verb+":", err)
		return 2
	}
	if len(pos) > 0 || *id == "" || *role == "" {
		fmt.Fprintln(stderr, "jam "+verb+": --id and --role are required")
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
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	fmt.Fprintln(stdout, verb+"ed", *role, "to", *id)
	return 0
}

func cmdActors(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("actors", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam actors:", err)
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "jam actors: takes no positional arguments")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	roster, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Actors()
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	for _, a := range roster {
		for _, g := range a.Grants {
			fmt.Fprintf(stdout, "%s\t%s/%s\tdests=%s\n", a.ID, g.Project, g.Role, jam.FormatDestinations(g.Destinations, g.Credentials))
		}
	}
	return 0
}

// builtLauncher is what buildLauncher hands serve: the launcher the supervisor
// drives, the image reader for setup inspection, and the default studio kit.
type builtLauncher struct {
	launcher       jam.Launcher
	router         *fleet.Router          // nil with the placeholder launcher
	images         jam.ManagedImageReader // nil with the placeholder launcher: image parts unavailable
	defaultKit     jam.KitRef
	haveDefaultKit bool
}

// buildLauncher builds the studio launcher. With runtime.studio-managers it is
// a fleet.Router, empty until Studio Managers connect (the hub registers
// them), and the built-in default studio kit is seeded. Otherwise it is
// placeholderLauncher{} (dev spine only) and there is no router.
func buildLauncher(cfg serveConfig, st jam.Store, log *slog.Logger) (builtLauncher, error) {
	out := builtLauncher{launcher: placeholderLauncher{}}
	if !cfg.Runtime.StudioManagers {
		return out, nil
	}
	router := fleet.New(log)
	out.launcher, out.router, out.images = router, router, router
	// Seed the built-in default studio kit (blessed base, Anthropic-free egress
	// ceiling — COV-208) in the registry and hand its reference to the
	// supervisor. Idempotent: an unchanged definition reuses the version across
	// restarts; the supervisor resolves the full definition from the registry
	// only when the launcher reports the kit is not yet built.
	var err error
	out.defaultKit, err = jam.EnsureDefaultStudioKit(st)
	if err != nil {
		return builtLauncher{}, fmt.Errorf("seed default studio kit: %w", err)
	}
	out.haveDefaultKit = true
	return out, nil
}

// startManagers registers every stored manager with the router (hub.Start),
// then calls run. It must precede the supervisor's reconcile loop so a manager
// that has not reconnected yet probes Unknown rather than Dead. A Start error
// is returned and run is not called.
func startManagers(hub *managers.Hub, run func()) error {
	if err := hub.Start(); err != nil {
		return err
	}
	run()
	return nil
}

// placeholderLauncher satisfies jam.Launcher without a real backend: it
// records a synthetic location and always probes Alive, so the supervisor spine
// (registry, leases, reconciler, restart re-adoption) runs end-to-end against a
// live `jam serve`. `cove raise` against it creates a Live Instance with no
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

func cmdServe(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var cfgPaths configPaths
	fs.Var(&cfgPaths, "config", "serve config YAML (repeatable: later files override; only the first may omit host keys — see serve.md)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || len(cfgPaths) == 0 {
		fmt.Fprintln(stderr, "jam serve: --config is required")
		return 2
	}
	data, portable, err := loadServeConfigs(cfgPaths, os.ReadFile)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	cfg, err := parseServeConfig(data)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	for _, d := range cfg.deprecated {
		logging.Deprecated(stderr, d[0], d[1])
	}
	if unknown := unknownServeKeys(data); len(unknown) > 0 {
		fmt.Fprintf(stderr, "jam: warning: ignoring unknown jam.yml key(s): %s\n", strings.Join(unknown, ", "))
		for _, k := range unknown {
			if k == "destinations" {
				fmt.Fprintln(stderr, "jam: note: destinations are managed in the config repo (see `jam config`)")
			}
		}
	}
	if err := cfg.validateAdminExposure(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateRequisitioner(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateStorage(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateStorePostgres(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateDiscord(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validatePush(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateDisplayName(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateWake(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateMetrics(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateSSH(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateKits(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateConfigRepo(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateBlobs(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validatePreviews(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	// Previews ride the admin listener with their own wildcard cert (picked by
	// SNI); both certs load now so a wrong or missing one fails serve.
	var adminTLSCfg *tls.Config
	// previewURL maps a session's preview to the URL collaborators open; nil
	// means previews are off (no previews: block).
	var previewURL func(actorID, name string) (string, error)
	if p := cfg.Previews; p != nil {
		previewCert, err := loadPreviewCert(p.TLS.Cert, p.TLS.Key, p.Domain)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		certFile, keyFile, _ := cfg.adminTLS()
		adminCert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			fmt.Fprintf(stderr, "jam: admin TLS (%s, %s): %v\n", certFile, keyFile, err)
			return 1
		}
		adminTLSCfg = adminTLSConfig(adminCert, previewCert, p.Domain)
		previewURL = previewURLFunc(p.Domain, cfg.AdminListen)
	}
	if err := cfg.validateSessionEvents(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validatePool(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if err := cfg.validateCredentials(); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	specs, err := planCredentials(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	warnPreRenameCredentialCommands(specs, log)

	// Postgres is the only store backend (validateStorage guarantees
	// store-postgres is set). The DB password is resolved on the host in memory and assembled into
	// the DSN — never written to disk/argv, never logged.
	var st jam.Store
	var pgPool *pgxpool.Pool // shared with the message log and session events
	// Every instance write goes through instChanges, which signals the app's
	// live stream when a session's status card would change.
	var instChanges *jam.InstanceChanges
	{
		pc := cfg.StorePostgres
		resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[pc.PasswordCred]})
		if err != nil {
			fmt.Fprintln(stderr, "jam: store-postgres password:", err)
			return 1
		}
		dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
			pc.Host, pc.Port, pc.Database, pc.User, resolved[pc.PasswordCred], pc.SSLMode)
		ps, err := jam.NewPostgresStore(context.Background(), dsn, log)
		if err != nil {
			fmt.Fprintln(stderr, "jam: store-postgres:", err)
			return 1
		}
		defer ps.Close()
		instChanges = jam.NewInstanceChanges(ps)
		st = instChanges
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

	// Operator-attention conditions (docs/usage/jam/monitoring.md): persisted
	// alongside the store; a load failure starts empty rather than failing serve.
	condStore, err := conditionpg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "jam: conditions store:", err)
		return 1
	}
	conds := condition.New(condition.Options{Persister: condStore, Log: log})
	if err := conds.Load(context.Background()); err != nil {
		log.Warn("conditions not loaded; starting empty", "reason", err.Error())
	}
	clearStaleCredConditions(conds, cfg.credNames())
	// Kits as trees (docs/usage/jam/kits-directory.md#stored-as-trees): every
	// pre-tree version becomes a tree, once; then unreferenced kit objects go.
	// A failed migration fails closed: a half-read registry would raise kits
	// from text the build no longer reads.
	if mig, err := jam.MigrateKitTrees(st, conds, log); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	} else if len(mig.Broken) > 0 {
		log.Warn("kit versions kept broken (their packed context could not be read)", "versions", mig.Broken)
	}
	// The jam rename's kit migration (docs/usage/jam/renamed-from-at.md):
	// kits still on pre-rename build-arg/ENV names or base repository get a
	// migration version. A store write error fails closed, like
	// MigrateKitTrees. Destinations whose stored env uses the now-reserved
	// JAM_ prefix get a destination.env condition (the key is dropped).
	// It logs what it rewrote and kept itself.
	if _, err := jam.MigrateKitRename(st, conds, log); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	jam.ReservedDestinationEnvConditions(st, conds)
	if gc, err := st.GCKitObjects(); err != nil {
		log.Warn("kit object collection failed (retried next start)", "reason", err.Error())
	} else if gc.Trees+gc.Blobs > 0 {
		log.Info("kit objects collected", "trees", gc.Trees, "blobs", gc.Blobs)
	}
	go conds.Run(context.Background(), 5*time.Second)
	// The periodic conditions (kit.migrate, reserved destination env): checked
	// now and every minute.
	go jam.RunConfigChecks(context.Background(), st, conds, jam.ConfigChecksEvery, log)

	var base jam.CredResolver = jam.NewSecretResolver(runner.OS{}, specs)
	if names := cfg.gcpCredentials(); len(names) > 0 {
		// exchange: gcp — the supplied Google credentials JSON stays on this host;
		// destinations get short-lived access tokens, refreshed on demand.
		gcp := jam.NewGCPTokenResolver(base, names, log)
		if err := gcp.Load(); err != nil {
			fmt.Fprintln(stderr, "jam: credentials:", err)
			return 1
		}
		base = gcp
		log.Info("GCP token exchange enabled", "creds", names) // names only
	}
	var creds jam.CredResolver = base
	if cfg.Pool != nil {
		poolStore, err := jam.NewFilePoolStore(cfg.Pool.Store)
		if err != nil {
			fmt.Fprintln(stderr, "jam: pool store:", err)
			return 1
		}
		pool := jam.NewPool(poolStore)
		creds = jam.NewChainResolver(base, pool, cfg.Pool.CredName)
		interval, margin, _ := cfg.poolDurations() // validated above
		refresher := jam.NewRefresher(poolStore, jam.RefresherOptions{
			TokenURL: cfg.Pool.TokenURL, ClientID: cfg.Pool.ClientID, Scope: cfg.Pool.Scope,
			Margin: margin, Log: log, Conditions: conds,
		})
		go refresher.Run(context.Background(), interval)
		log.Info("Jam subscription pool enabled", "store", cfg.Pool.Store, "cred", cfg.Pool.CredName) // never tokens
	}
	creds = jam.NewWatchedResolver(creds, conds, cfg.credFixHint)
	broker := jam.NewBroker(st, creds, log)

	// Jam's SSH user CA (studio collaboration): every raised studio's sshd
	// trusts its public key; the private key stays in this process's memory.
	// nil when the ssh block is unset (SSH collaboration off); it signs the
	// certs the collaboration routes (collabhttp) issue.
	sshCA, err := resolveSSHCA(cfg, specs)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	// The attachments bucket (nil: attachments off). A set but unreachable or
	// missing bucket refuses to start.
	blobs, err := resolveBlobs(cfg, specs)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	if blobs != nil {
		log.Info("Jam attachments: bucket ready", "bucket", cfg.Blobs.Bucket, "max-bytes", cfg.Blobs.maxBytes())
	}
	// Web Push (nil: off). The VAPID private key stays in this process's
	// memory; a set but unresolvable or invalid key refuses to start.
	vapid, err := resolvePush(runner.OS{}, cfg, specs)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	pushPublic := ""
	if vapid != nil {
		pushPublic = vapid.public
		log.Info("Jam push notifications: VAPID key loaded", "cred", cfg.Runtime.Push.VAPIDPrivateKeyCred) // never the key
	}
	// Who has which conversation open in the app (in memory): the push relay
	// skips them; the app's focus route (/app/user/push/focus) records it.
	pushFocus := push.NewFocus(time.Now)
	// Each user's open Jam streams' notifications (/app/user/events), fed by
	// the notify relay engine below.
	notifyHub := notify.NewHub(log)
	// The one long-lived git fetcher for kit sources (it serializes per repo);
	// anonymous when no kits.git credential is configured.
	kitFetcher, err := resolveKitFetcher(cfg, specs)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	var sshUserCA []byte
	if sshCA != nil {
		sshUserCA = sshCA.PublicKey()
		log.Info("Jam SSH user CA: loaded", "cred", cfg.SSH.CACred) // never the key
	}

	ttl, reconcile, err := cfg.runtimeDurations()
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	built, err := buildLauncher(cfg, st, log)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	lch, images := built.launcher, built.images
	defaultRef, haveDefaultKit := built.defaultKit, built.haveDefaultKit
	sup := jam.NewSupervisor(st, lch, jam.NewHolderID(), ttl, reconcile, time.Now, log)
	if haveDefaultKit {
		sup.SetDefaultStudioKit(defaultRef)
	}
	// Before Run: session context (compiled from Run's goroutines) says
	// whether the expose tools work.
	sup.SetPreviewsEnabled(previewURL != nil)
	// Kit draft test builds, and the images draft studios run, build under
	// buildCtx; SIGINT/SIGTERM cancels them before serve exits. The one
	// registry is the supervisor's: the admin build routes read it there.
	buildCtx, stopBuilds := context.WithCancel(context.Background())
	var draftBuilds *jam.KitDraftBuilds
	// Builds run per manager, through the router (the placeholder builds none).
	if mb, ok := lch.(jam.ManagedDraftBuilder); ok {
		draftBuilds = jam.NewKitDraftBuilds(buildCtx, st, mb, log)
	}
	if draftBuilds != nil {
		sup.SetKitDraftBuilds(draftBuilds)
	}
	go stopBuildsOnSignal(shutdownSignals(), stopBuilds, draftBuilds, log, dieBySignal)
	// Studio Managers: register every stored manager with the router (so an
	// instance on a not-yet-reconnected manager probes Unknown, not Dead)
	// before the supervisor starts reconciling.
	var managerHub *managers.Hub
	runSup := func() { go sup.Run(context.Background()) }
	if cfg.Runtime.StudioManagers {
		managerHub = managers.NewHub(managers.Config{Store: st, Router: built.router, Reconciler: sup, Version: version, SSHUserCA: sshUserCA, Log: log})
		if err := startManagers(managerHub, runSup); err != nil {
			fmt.Fprintln(stderr, "jam: studio managers:", err)
			return 1
		}
		log.Info("Jam studio managers: accepting connections", "registered", len(st.ListManagers()))
	} else {
		runSup()
	}

	// Attach gRPC server: served on the cove-facing :443 mux below, and
	// optionally on a plaintext dev listener (runtime.listen). One server, one
	// ControlSink. Built here (ahead of the Requisitioner block below) because the
	// resident wake-on engine needs rsrv as its Waker.
	rsrv := attach.NewServer(st, sup, log)
	sup.SetControlSink(rsrv)
	// The open-studio-connection registry (SSH tunnels and preview
	// connections) both collaboration planes, the preview proxy and the UI's
	// Access card share, so an unshare anywhere closes the user's connections.
	liveConns := collabhttp.NewLive()
	// Exposed preview ports reach each studio's jam-studio-agent as its dynamic
	// tunnel allow-list; a restarted Jam seeds it from the stored exposures.
	// An unexposed port's open preview connections close at once.
	// The preview proxy (built later) drops its label cache on any change.
	var invalidatePreviewLabels atomic.Pointer[func()]
	sup.SetExposureSink(newExposureFanout(rsrv, liveConns, func() {
		if f := invalidatePreviewLabels.Load(); f != nil {
			(*f)()
		}
	}))
	rsrv.SetExposureSource(sup.ExposedPorts)
	// Studio collaboration tunnels: a human's connection reaches a studio's
	// loopback port over a Tunnel the studio dials back; the supervisor closes
	// a studio's tunnels before it pauses or tears it down.
	tunnels := attach.NewTunnelBroker(rsrv.PushOpenTunnel, attach.TunnelOptions{})
	rsrv.SetTunnelBroker(tunnels)
	sup.SetTunnelCloser(tunnels)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, rsrv)
	if managerHub != nil {
		managerpb.RegisterStudioManagerServer(gs, managerHub)
	}

	// The intercom's channel log, opened once for the serve lifetime in the
	// shared control-plane Postgres; it continues the frozen legacy log (ml),
	// which inboxes and the UIs' History still read.
	pglog, err := intercompg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "jam: message log (postgres):", err)
		return 1
	}
	ml := pglog.Legacy()
	log.Info("Jam message log: postgres (shared control-plane database)")
	// Every writer (agent send, relay ingress, /app/user/send, Jam's notices) shares
	// this one handle, so wrapping it lets live views (/app/user/events) see each append.
	logChanges := intercom.NewNotifier(pglog)
	var chlog intercom.Store = logChanges
	sup.SetTailReader(chlog) // seqs run on across the cutover: the tail is the channel log's, else the legacy log's
	// Sessions follow their tickets' channels (intercom slice 2a); ticket
	// channels key on the requisitioner's tracker connection, resolved below
	// (until then, or with no requisitioner, jam.TrackerConnection: the
	// linear connection, else the github one).
	var trackerConn atomic.Value // ident.ID
	icTracker := func() (ident.ID, bool) {
		if id, ok := trackerConn.Load().(ident.ID); ok {
			return id, true
		}
		c, ok := jam.TrackerConnection(st)
		return c.ID, ok
	}
	ic := jam.NewIntercom(st, icTracker, chlog, nil, log)
	sup.SetSessionChannels(ic)
	// Attachments (nil: no blobs block, off); pending uploads past 24h are
	// swept hourly.
	var blobMax int64
	if cfg.Blobs != nil {
		blobMax = cfg.Blobs.maxBytes()
	}
	att := jam.NewAttachments(chlog, blobs, ic, blobMax)
	boards := jam.NewBoards(chlog, ic)
	att.SetLogger(log)
	if att.Enabled() {
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for ; ; <-t.C {
				att.Sweep(context.Background(), time.Now())
			}
		}()
	}
	ic.SetOnAsksChanged(func(session ident.ID) {
		if err := sup.SetOpenAsks(string(session), func() int { return ic.OpenAskCount(session) }); err != nil {
			log.Warn("intercom: recording open asks failed", "session", string(session), "err", err.Error())
		}
	})
	// Asks stranded by a change made outside the intercom (a room removed, a
	// session gone) close on this pass.
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for ; ; <-t.C {
			ic.ReconcileAsks()
		}
	}()

	// Session events (docs/usage/jam/session-events.md): stored in the shared
	// control-plane Postgres.
	sessStore, err := sessionpg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "jam: session events (postgres):", err)
		return 1
	}
	log.Info("Jam session events: postgres (shared control-plane database)")
	// Events recorded before they carried ids get them from their labels
	// (1b-2b), in the background: it never holds up serving.
	go func(projects, owners map[string]string) {
		if n, err := sessStore.BackfillIDs(context.Background(), projects, owners); err != nil {
			log.Warn("session events: backfilling project/owner ids failed", "err", err.Error())
		} else if n > 0 {
			log.Info("session events: backfilled project/owner ids", "rows", n)
		}
	}(ledgerRefs(st))
	sessHub := sessionevents.NewHub()
	// Derived per-session status for the Inbox's presence strip, fed by every event.
	sessPresence := sessionevents.NewPresence(nil)
	sessHub.Observe(sessPresence.Observe)
	rsrv.SetSessionEvents(sessionevents.NewIngest(sessStore, sessHub, nil))
	if keep, _ := sessionevents.ParseRetention(cfg.SessionEventsRetention); keep > 0 {
		go sessionevents.RunRetention(context.Background(), sessStore, keep, 24*time.Hour, nil, log)
	}

	// The Allocator is Jam's capacity authority, built whenever Jam serves
	// (not only with a Requisitioner): it admits the Requisitioner's ephemeral raises and
	// operators' personal-session requests against
	// the roster's per-(project, role) policy (the role's max-ephemeral
	// and max-personal caps), read live on each grant. The
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
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		// Streams are keyed by project id and personal owners by user id
		// (1b-2a): re-key what an older Jam recorded by name, before any grant.
		projects, owners := ledgerRefs(st)
		if n, err := as.RewriteRefs(context.Background(), projects, owners, personalOwners(st)); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		} else if n > 0 {
			log.Info("allocator: re-keyed ledger events by project and user id", "rows", n)
		}
		ledger = as
	}
	var reqProject string // the Requisitioner's project, by id
	if cfg.Runtime.Requisitioner != nil {
		reqProject = resolveRequisitionerProject(st, cfg.Runtime.Requisitioner, log)
	}
	alloc := allocator.New(jam.InstanceCounter{Store: st}, newRosterPolicy(st, cfg.Runtime.Requisitioner, reqProject), ledger)
	alloc.SetProjectKey(func(ref string) string {
		if id, ok := jam.ProjectIDOf(st, ref); ok {
			return string(id)
		}
		return ref
	})
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
	// role in git — raises a missing or dead one under its
	// per-name actor id (admitted by the Allocator, released on a failed raise,
	// with per-name backoff) and tears down one whose name was removed. Resident
	// whenever Jam serves; with no declarations a tick does nothing.
	stdg := standing.New(st /*Roster*/, st /*Registry*/, alloc /*Granter*/, sup /*Supervisor*/, standing.DefaultInterval, log)
	stdg.SetActors(st)            // clear a standing identity left over from an interrupted raise
	sup.SetStandingResetter(stdg) // `standing reset` (API + UI) runs serialized with its passes
	sup.SetStandingUpgrader(stdg) // `standing upgrade` queues into it; its passes carry it out
	go stdg.Run(context.Background())
	log.Info("Jam standing reconciler: resident", "interval", standing.DefaultInterval)

	// The intercom — the /squawks + /escalate endpoints, the wake-on engine and
	// the Discord relay — runs always (message log via Postgres is always present):
	// a personal session converses with its owner over it. The tracker, the
	// Requisitioner, the escalation engine and the tracker relay need the tracker, so
	// they stay in the Requisitioner block below.
	dc := cfg.Runtime.Requisitioner

	// httpHandler is the cove-facing HTTP handler mounted on the :443 mux below:
	// the broker, plus /squawks and /escalate (always available).
	// tickets moves a ticket session's ticket (POST /report, and blocked on an
	// unfinished teardown); its tracker is set below with the Requisitioner's.
	tickets := &ticketHolder{}
	httpHandler := coveHTTPHandler(broker, st, sup, &messaging{ic: ic, log: chlog, legacy: ml, att: att, boards: boards}, dc != nil, tickets, previewURL, log)
	if cfg.Metrics != nil {
		tok, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[cfg.Metrics.TokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "jam: metrics.token-cred:", err)
			return 1
		}
		httpHandler = withMetrics(httpHandler, condition.MetricsHandler(conds, tok[cfg.Metrics.TokenCred], func() []condition.Gauge {
			gauges := []condition.Gauge{{Name: "jam_studios", Help: "Studios Jam knows of.", Value: float64(len(jam.CoveSummaries(st, sup)))}}
			if managerHub != nil {
				var registered, connected int
				for _, m := range managerHub.Statuses() {
					registered++
					if m.Connected {
						connected++
					}
				}
				gauges = append(gauges,
					condition.Gauge{Name: "jam_managers", Help: "Studio Managers registered.", Value: float64(registered)},
					condition.Gauge{Name: "jam_managers_connected", Help: "Studio Managers connected.", Value: float64(connected)})
			}
			return gauges
		}))
		log.Info("Jam metrics: mounted", "path", "/metrics") // never the token
	}

	// Wake-on engine: watches Waiting instances and Wakes them over the live
	// Attach stream (rsrv, the ControlSink) when an external-origin reply lands
	// in the message Log addressed to them, pauses them past the warm-timeout,
	// tears down non-personal ones past wait-max, and runs the personal-session
	// idle ladder (nag the owner, optionally reclaim). Resident for the lifetime
	// of the process. Settings: runtime.wake > runtime.requisitioner > defaults.
	wcfg := cfg.wakeSettings()
	eng := wakeon.New(st, rsrv /*ControlSink Waker*/, sup /*Reaper*/, sup /*Idler*/, jam.SessionInbox{Log: chlog, Legacy: ml} /*Inbox*/, wcfg, log)
	// Personal-session idle ladder: nag the owner past the role's idle-after
	// (squawks sent as the cove, delivered by the relay), optionally reclaim
	// past reclaim-after.
	nagger := intercomNagger{log: ic, roster: st}
	eng.SetIdleLadder(st /*RoleLookup*/, sup /*NagRecorder*/, nagger)
	// Sessions wake sessions (intercom slice 3c), with the loop breaker:
	// past runtime.wake.session-wake-limit session posts in a row in a
	// channel, they stop waking until a person posts there (0: never wake).
	if limit := cfg.sessionWakeLimit(); limit > 0 {
		eng.SetSessionWakes(chlog /*ChannelHistory*/, ic /*BreakerNotifier*/, limit)
	}

	// Wake Running coves on a reply too: an agent holding its episode open for
	// a background task is Running, and its owner's reply must reach it then.
	eng.SetRunningWake(sup /*Cursor*/)
	// Turn end: enforce end(reason) and the role's idle timeout.
	eng.SetTurnEnd(st /*RoleLookup*/, nagger /*Ender*/, sup /*AlarmFirer*/)
	eng.SetGates(sup /*GateState*/, rsrv /*GateRunner*/)
	// An open SSH connection is activity: a studio with a tunnel in use is not
	// paused, reclaimed or torn down for waiting.
	eng.SetTunnels(tunnels)
	eng.SetTickets(tickets /*TicketCloser*/, sup /*ReportRecorder*/)
	// Wake on signal, not only on the poll: every message-log append and every
	// activity a cove reports (incl. its reconnect after an unpause) kicks a
	// tick, so a reply wakes its session within the kick delay; the poll stays
	// the backstop.
	rsrv.SetOnStatus(eng.Kick)
	appends, _ := logChanges.Subscribe()
	go func() {
		for range appends {
			eng.Kick()
		}
	}()
	go eng.Run(context.Background())
	log.Info("Jam wake-on engine: resident", "wait-max", wcfg.MaxWait)

	// Relay state shared by the Linear, Discord and push relay engines (one cursors
	// file, one markers file, one directory). Every directory field is set
	// before any engine starts.
	var (
		relayCursors *fileCursors
		relayMarkers *fileMarkers
		discordTok   string
	)
	runPush := vapid != nil
	runNotify := cfg.AdminListen != "" // the user app is served: its streams carry notify events
	dir := &directory{store: st, ic: ic, log: log, tracker: icTracker, pushOn: runPush, pushFocus: pushFocus}
	dir.notifyOn = runNotify
	runDiscord := cfg.Runtime.Discord != nil
	var stateDir string
	if dc != nil || runDiscord || runPush {
		if stateDir, err = cfg.stateDir(); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			fmt.Fprintln(stderr, "jam: state-dir:", err)
			return 1
		}
		cursorsPath, markersPath, _ := relayStatePaths(stateDir)
		if relayCursors, err = newFileCursors(cursorsPath); err != nil {
			fmt.Fprintln(stderr, "jam: relay cursors:", err)
			return 1
		}
		// Cursors are kept by project id (1b-2b); one stored by name is re-keyed.
		if err := relayCursors.keyedBy(func(ref string) string {
			if id, ok := jam.ProjectIDOf(st, ref); ok {
				return string(id)
			}
			return ref
		}); err != nil {
			fmt.Fprintln(stderr, "jam: relay cursors:", err)
			return 1
		}
		if relayMarkers, err = newFileMarkers(markersPath); err != nil {
			fmt.Fprintln(stderr, "jam: relay markers:", err)
			return 1
		}
		// The relays render only the channel log: a mark still in the legacy
		// log moves to the cutover (see intercom.md, the cutover). Push is
		// not settled: it is seeded at the channel log's tail when first
		// enabled (below), so it never held a legacy mark or legacy pending ids.
		for _, service := range []string{"linear", "discord"} {
			skipped, err := relayMarkers.settleCutover(service, chlog.CutoverSeq(), chlog.SeqOf)
			if err != nil {
				fmt.Fprintln(stderr, "jam: relay markers:", err)
				return 1
			}
			if skipped > 0 {
				log.Warn("relay: legacy squawks not yet delivered at the cutover are skipped", "service", service, "seqs", skipped)
			}
		}
	}
	var discordReceipts *fileReceipts
	if runDiscord {
		_, _, receiptsPath := relayStatePaths(stateDir)
		dconn, dcred, err := resolveServeConnection(st, "runtime.discord", "discord", cfg.Runtime.Discord.Connection, cfg.Runtime.Discord.BotTokenCred, cfg.Credentials)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[dcred]})
		if err != nil {
			fmt.Fprintln(stderr, "jam: discord connection credential:", err)
			return 1
		}
		discordTok = tokEnv[dcred]
		log.Info("Jam relay (discord): connection", "connection", dconn.Name, "id", dconn.ID)
		dir.discord = dconn.ID
		if discordReceipts, err = newFileReceipts(receiptsPath); err != nil {
			fmt.Fprintln(stderr, "jam: relay receipts:", err)
			return 1
		}
		dir.receipts = discordReceipts // wires directory.routeDiscord (COV-183): reply→cove lookup
	}

	if dc != nil {
		kind := dc.trackerKind()
		// Resolve Jam's own tracker token (never injected into a cove, never logged).
		tconn, tcred, err := resolveServeConnection(st, "runtime.requisitioner", kind, dc.Connection, dc.TrackerTokenCred, cfg.Credentials)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		if dc.GitHub != nil { // only the tracker repo's issues are deliverable: other repos' are refused
			jam.SetTrackerRepo(tconn.ID, strings.TrimSpace(dc.GitHub.Repo))
		}
		trackerConn.Store(tconn.ID)
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[tcred]})
		if err != nil {
			fmt.Fprintln(stderr, "jam: requisitioner connection credential:", err)
			return 1
		}
		log.Info("requisitioner: connection", "kind", kind, "connection", tconn.Name, "id", tconn.ID)
		trk, err := openTracker(dc, tokEnv[tcred], nil)
		if err != nil {
			fmt.Fprintln(stderr, "jam: requisitioner tracker:", err)
			return 1
		}
		poll, _ := time.ParseDuration(dc.pollInterval()) // "" or invalid → 0 → Requisitioner default
		// The Requisitioner's (project, role), normalized the same way as the
		// Allocator's fallback (see requisitionerProject).
		project := reqProject
		if r, ok := st.GetRole(project, dc.Role); ok && r.Allocation.MaxEphemeral > 0 && r.Allocation.MaxEphemeral != dc.MaxConcurrent {
			log.Info("Jam allocator: roster max-ephemeral overrides Requisitioner max-concurrent",
				"project", project, "role", dc.Role, "max-ephemeral", r.Allocation.MaxEphemeral, "max-concurrent", dc.MaxConcurrent)
		}
		tickets.set(trackerTicketer{trk})
		disp := dispatcher.New(trk, sup, st, alloc, dispatcher.Config{
			Role: dc.Role, Project: project, PollInterval: poll,
		}, log)
		go disp.Run(context.Background())
		log.Info("requisitioner: resident", "kind", kind, "role", dc.Role, "max-concurrent", dc.MaxConcurrent)

		// The tracker relay engine polls the tracker's comment feed and posts
		// inbound replies into the channel log opened above (ingress). It also
		// delivers outbound Log messages to the tracker (egress). The Log is
		// the single source of truth for both directions.
		dir.project = reqProject // by id: the relays' cursors and polling outlive a rename
		dir.trackerKind = kind
		// Seed once: skip everything the 1a dual-write already delivered live,
		// so turning egress on never re-posts the Log's shadow history.
		// Persisted with a nonzero LastSeq → never re-seeds (a re-seed to a
		// newer tail would drop messages appended-but-not-yet-delivered since
		// the first cutover). needsSeed also re-seeds a marker whose LastSeq
		// is zero, which covers a pre-COV-184 marker file (persisted the
		// low-water as LastMsg, a string) upgrading in place — see
		// fileMarkers.needsSeed. A new github service seeds at the tail.
		if relayMarkers.needsSeed(kind) {
			if err := relayMarkers.SetEgress(kind, relay.EgressMark{LastSeq: logTailSeq(chlog)}); err != nil {
				fmt.Fprintln(stderr, "jam: relay egress seed:", err)
				return 1
			}
		}
		// startRelay runs the relay once Jam's tracker identity (the
		// self-post filter) is known; only this engine reads selfIdentity.
		startRelay := func(self string) {
			dir.selfIdentity = self
			surf := &trackerSurface{service: kind, feed: trk, poster: trk, started: time.Now()}
			eng := relay.New(surf, chlog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
			go eng.Run(context.Background())
			log.Info("Jam relay ("+kind+"): resident, egress ON", "self", self != "")
		}
		if kind == tracker.KindGitHub {
			// GitHub retries a failed Viewer (in the background: dispatch and
			// reports run meanwhile) and starts the relay once it succeeds.
			go func() {
				if self, ok := trackerSelf(context.Background(), kind, trk.Viewer, log, sleepCtx); ok {
					startRelay(self)
				}
			}()
		} else {
			self, _ := trackerSelf(context.Background(), kind, trk.Viewer, log, sleepCtx)
			startRelay(self)
		}

	}
	// Every live session gets its home channel — a ticket session its ticket
	// channel, bound to the issue on the tracker connection resolved above —
	// so replies have somewhere to land before it sends.
	if err := ic.Reconcile(); err != nil {
		log.Warn("intercom: giving live ticket sessions their channels failed (they get them on first send)", "err", err.Error())
	}

	// Escalation engine (intercom slice 4), started once the tracker
	// connection is resolved and live sessions have their home channels
	// (above): while a session is Waiting and has asked for a person, calls
	// ordered tiers of its Project's escalation policy into its home channel
	// on per-tier timers; the relays deliver the notice (a ticket's issue
	// @-mentions the tier). Reply-detection, waking and teardown stay
	// wake-on's job. Runs with or without a Requisitioner.
	epoll := cfg.escalationPollInterval()
	eeng := escalate.New(st /*Registry*/, jam.ProjectMembers{Store: st} /*Projects*/, sup /*State*/, ic /*Caller*/, escalate.Config{PollInterval: epoll}, log)
	go eeng.Run(context.Background())
	log.Info("Jam escalation engine: resident", "poll-interval", epoll)

	// relay discord engine: a resident engine over the same Log, markers file,
	// cursors, and directory as the tracker one — delivers outbound Log messages
	// to Discord (egress) AND polls every discord project's inbox channels for
	// human replies, routing a reply back to the cove it answers via the receipt
	// store (ingress). The engine keys EgressMark by Service(), so the tracker's
	// ("linear" or "github") and "discord" marks live side by side in the one
	// markers file. Gated on runtime.discord; no Requisitioner needed.
	if runDiscord {
		dsurf := &discordSurface{
			dial: func(channels []string) discordClient {
				return discord.NewRESTClient(discordTok, channels)
			},
			channelsFor: func(project string) []string { return discordPolledChannels(st, project) },
			receipts:    discordReceipts,
			log:         log,
		}
		if relayMarkers.needsSeed("discord") { // seed: don't re-deliver the backlog to Discord (see needsSeed doc)
			if err := relayMarkers.SetEgress("discord", relay.EgressMark{LastSeq: logTailSeq(chlog)}); err != nil {
				fmt.Fprintln(stderr, "jam: discord egress seed:", err)
				return 1
			}
		}
		deng := relay.New(dsurf, chlog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
		go deng.Run(context.Background())
		log.Info("Jam relay (discord): resident, egress ON")
	}

	// The push rule's reader and unread total, shared by the push and notify
	// engines.
	reader := pushReader{store: st, ic: ic}
	unread := func(u ident.ID) int { return jam.UnreadTotal(st, ic, chlog, u) }

	// relay push engine: egress only, over the same Log, markers and
	// directory — each notified person's devices (Web Push), signed with the
	// VAPID key resolved above. Gated on runtime.push.
	if runPush {
		if relayMarkers.needsSeed(push.Service) { // seed: never buzz for the backlog on first enable
			if err := relayMarkers.SetEgress(push.Service, relay.EgressMark{LastSeq: logTailSeq(chlog)}); err != nil {
				fmt.Fprintln(stderr, "jam: push egress seed:", err)
				return 1
			}
		}
		psurf := &push.Surface{
			Store:   st,
			Sender:  push.NewWebPushSender(nil),
			Payload: pushPayload(reader, pushFocus, unread, cfg.Runtime.Push.preview()),
			Opts:    push.SendOptions{Subscriber: cfg.Runtime.Push.Subject, VAPIDPrivate: vapid.private, VAPIDPublic: vapid.public},
			Now:     time.Now,
			Conds:   conds,
			Log:     log,
		}
		peng := relay.New(psurf, chlog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
		go peng.Run(context.Background())
		log.Info("Jam relay (push): resident, egress ON", "preview", cfg.Runtime.Push.preview())
	}

	// relay notify engine: egress only, over the same Log and directory —
	// the push rule's recipients' open Jam streams (notifyHub). Its mark is
	// in memory, seeded at the log tail on every start (never a backlog), so
	// it needs no state dir. Runs whenever the user app is served.
	if runNotify {
		preview := cfg.Runtime.Push == nil || cfg.Runtime.Push.preview()
		nsurf := &notify.Surface{Hub: notifyHub, Event: notifyEvent(reader, pushFocus, unread, preview), Now: time.Now}
		neng := relay.New(nsurf, chlog, newMemMarkers(notify.Service, relay.EgressMark{LastSeq: logTailSeq(chlog)}), noCursors{}, dir, relay.Config{EgressEnabled: true}, log)
		go neng.Run(context.Background())
		log.Info("Jam relay (notify): resident, egress ON")
	}

	if cfg.Runtime.Listen != "" { // optional plaintext dev listener (not the production path)
		lis, err := net.Listen("tcp", cfg.Runtime.Listen)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
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
				fmt.Fprintln(stderr, "jam: operator OIDC:", err)
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
		// One planner view of the serve for the config admin API and the UI's
		// Config page.
		cfgEnv := jam.ConfigEnv{CredExists: credExists, PoolConfigured: cfg.Pool != nil, Serve: portable, Conditions: conds,
			Followed: cfg.followedSource()}
		if src := cfgEnv.Followed; src.Repo != "" {
			// Named as configured: a config.git url (a local path or ssh)
			// does not re-parse as a kit source, and carries no password.
			cfgEnv.FollowedLabel = src.String()
		}
		// The followed config repo's clone, built once: the branch editor
		// and the follower read through it.
		var configRepo *configrepo.Repo
		if src := cfgEnv.Followed; src.Repo != "" {
			dir, err := cfg.stateDir()
			if err != nil {
				fmt.Fprintln(stderr, "jam:", err)
				return 1
			}
			if configRepo, err = newConfigRepo(cfg, specs, kitFetcher, dir); err != nil {
				fmt.Fprintln(stderr, "jam:", err)
				return 1
			}
		}
		// The followed branch is read through the clone, whatever its kind;
		// every other source (a kit's .source.yml) through kitFetcher.
		configFetcher := &jam.FollowedFetcher{Repo: configRepo, Followed: cfgEnv.Followed, Rest: kitFetcher}
		// The config repo the Jam follows (config.github, gitlab or git):
		// checked now and every minute; a new commit applies once the first
		// is confirmed.
		if cfgEnv.Followed.Repo != "" {
			cfgEnv.Follower = &jam.ConfigFollower{Store: st, Fetcher: configFetcher, Env: cfgEnv, Log: log}
			go cfgEnv.Follower.Run(context.Background(), jam.ConfigFollowEvery)
		}

		// Read-only intercom view: shares the Log opened once above (the same
		// handle the /squawks writer dual-writes into) with the admin UI as a
		// read-only reader.
		squawkReader := adminui.NewSquawkReader(st, ic, chlog, ml)

		// The browser app (/app): one OIDC login (/app/auth/*, ungated) whose
		// session names a roster user (ID token) and/or an admin (access token
		// with the operator scope); loopback is always an admin. Without browser
		// login it is loopback-only.
		var sessions *appui.Sessions
		var authRoutes http.Handler
		if bc := cfg.browserAuthConfig(); bc != nil {
			svc, err := browserauth.New(context.Background(), *bc, browserauth.AppMount(), nil, log)
			if err != nil {
				fmt.Fprintln(stderr, "jam: browser login:", err)
				return 1
			}
			authRoutes = svc.Routes()
			m := browserauth.AppMount()
			sessions = &appui.Sessions{Issuer: svc.Issuer(), IDCookie: m.IDCookie(), AccessCookie: m.AccessCookie(), VerifyID: svc.VerifySubject, Store: st}
			if oidcAuth, ok := auth.(*jam.OIDCAuthenticator); ok {
				sessions.VerifyAccess = oidcAuth.VerifyToken
			}
			log.Info("Jam app auth: browser OIDC login", "client-id", bc.ClientID)
			if cfg.browserLoginAdminsEveryone() {
				log.Warn("browser login is configured but operator-auth.oidc.require-scope is empty: any access token for the audience verifies, so every signed-in user (Inbox members included) is an admin; set require-scope to keep the console to operators")
			}
		} else {
			log.Info("Jam app auth: loopback-only")
		}
		gc := appui.GateConfig{Sessions: sessions, ExpectedHosts: cfg.UIHosts, Log: log}
		if sessions != nil {
			gc.LoginPath = "/app/auth/login"
		}
		// dev-identity (DEV ONLY; parse refuses it off-loopback): loopback
		// requests act as one user with no login.
		if d := cfg.DevIdentity; d != nil {
			gc.Dev = &browserauth.DevIdentity{Store: st, User: d.User, LegacyProject: d.DeprecatedProject, LegacyHuman: d.DeprecatedHuman}
			log.Warn("DEV IDENTITY ACTIVE: loopback browser requests act as a user with no login; never use in production",
				"user", d.User, "legacy_project", d.DeprecatedProject, "legacy_human", d.DeprecatedHuman)
		}
		// The user area: the Jam app and its API, and the send endpoint (POST
		// /app/user/send), the human analog of the agent send tool. Sends go to
		// the same intercom Log the agent send + relay ingress use, so a reply
		// wakes a waiting studio; the app reads the same channel log, and the
		// legacy log as History.
		userMux := http.NewServeMux()
		// The open app-token requests, so a revoke ends them.
		appTokenLive := apptoken.NewLive()
		// The Jam pane's one-time frame tickets (docs/usage/jam/previews.md
		// "Framing"): the previews' login trades them for frame sessions and
		// prunes them.
		var frames *collabhttp.FrameTickets
		if cfg.Previews != nil {
			frames = collabhttp.NewFrameTickets(time.Now)
		}
		sendH := jam.NewParticipantSendHandler(st, ic, log)
		sendH.SetAttachments(att)
		userMux.Handle("/app/user/send", sendH)
		userDeps := userui.Deps{Send: sendH, Store: st, Intercom: ic, Log: chlog, Legacy: ml, Attachments: att, Boards: boards, Ops: sessionOps{store: st, sup: sup, res: sup, cmd: rsrv, lines: sup, log: log}, Totals: sessionevents.NewTotalsCache(sessStore), PreviewURL: previewURL, Events: sessStore, Hub: sessHub}
		if sup != nil {
			userDeps.Personal = personalStarter{st: st, sup: sup, alloc: personalAllocator{alloc}, log: log}
		}
		// Never a typed nil: a nil Frames hides Studio and View here, as it must
		// without ui-origins (previews then refuse every framer).
		if frames != nil && paneFramingOn(&cfg) {
			userDeps.Frames = frameURLs{tickets: frames, domain: cfg.Previews.Domain, adminListen: cfg.AdminListen}
		}
		// The user area is the Jam app (web/app) and its API; the app's CSP
		// frames the preview hosts when the pane may.
		userOpts := []userui.Option{userui.WithChanges(logChanges), userui.WithPresence(sessPresence), userui.WithInstanceChanges(instChanges), userui.WithPush(pushPublic), userui.WithPushFocus(pushFocus),
			userui.WithFrameSrc(appFrameSrc(&cfg)), userui.WithAppTokenRevoke(appTokenLive.Revoke)}
		if runNotify {
			userOpts = append(userOpts, userui.WithNotify(notifyHub))
		}
		userMux.Handle("/app/user/", userui.Handler(userDeps, log, userOpts...))

		// Setup inspection: the admin API (WithSetupInspect) and the UI's Setup
		// tabs (WithSetupDeps) render with the same deps.
		sealed, err := assemble.SealedEgressBase()
		if err != nil {
			log.Warn("setup inspect: sealed egress base unreadable; egress shows no sealed rows", "err", err.Error())
		}
		setupDeps := jam.SetupDeps{Store: st, Sup: sup, Images: images, SealedEgress: sealed}
		uiOpts := []adminui.Option{adminui.WithTrustedOrigins(cfg.UIOrigins...), adminui.WithSessions(sessStore, sessHub), adminui.WithCredentialNames(cfg.credNames()...), adminui.WithPoolConfigured(cfg.Pool != nil), adminui.WithConditions(conds, alertmanagerURL(cfg)), adminui.WithKitFetcher(kitFetcher), adminui.WithConfigFetcher(configFetcher), adminui.WithConfigEnv(cfgEnv), adminui.WithSetupDeps(setupDeps),
			adminui.WithAppTokenRevoke(appTokenLive.Revoke)}
		if built.router != nil {
			uiOpts = append(uiOpts, adminui.WithSetupManagers(built.router.AvailableNames))
		}
		if managerHub != nil {
			uiOpts = append(uiOpts, adminui.WithManagers(managerHub.Statuses))
		}
		if sshCA != nil || previewURL != nil {
			uiOpts = append(uiOpts, adminui.WithStudioAccess(liveConns), adminui.WithStudioPreviews(sup.Exposures, previewURL))
		}
		if sshCA != nil || cfg.Previews != nil {
			// Revocation: drop open SSH and preview connections whose access has
			// gone (a removed user, a role-policy or membership change, an
			// expired grant).
			go liveConns.Run(context.Background(), st, log, collabhttp.RevokeEvery, time.Now)
		}
		// Writes in the user area must come from the page itself (the mux
		// applies RequireSameOrigin): the session cookie alone (SameSite=Lax)
		// lets a sibling subdomain post as the user.
		// App tokens (docs/usage/jam/app-tokens.md): minted only from a
		// verified browser session, so only with browser login.
		// They act as their user on /app/user and /me/studios, never on
		// /app/admin; revoking one ends its open /app/user requests (live).
		var appTokenRoutes http.Handler
		var appTokenBearer appui.BearerFunc
		if sessions != nil {
			appTokens := apptoken.New(apptoken.Config{Store: st, Sessions: sessions, Live: appTokenLive, TrustedOrigins: cfg.UIOrigins,
				Brand: appBrand(cfg.displayName()), Now: time.Now, Log: log})
			appTokenRoutes, appTokenBearer = appTokens.Routes(), appTokens.Bearer()
			go appTokens.Run(context.Background())
		}
		// The config branch editor (docs/usage/jam/config-branches.md): config
		// pages edit a branch of the followed repo, from a bare clone in the
		// state dir. Branch handlers are the same admin UI over a branch's
		// scratch store, with no supervisor (a branch raises nothing).
		if src := cfgEnv.Followed; src.Repo != "" {
			token := func() (string, error) {
				cr, ok := kitFetcher.Creds.For(src.Repo)
				if !ok {
					return "", errors.New("no credential")
				}
				return cr.Token()
			}
			var prs configrepo.PRs // nil for git: no pull requests to open
			switch cfg.configRepo().kind {
			case "github":
				prs = configrepo.GitHub{URL: src.Repo, Token: token}
			case "gitlab":
				prs = configrepo.GitLab{URL: src.Repo, Token: token}
			}
			branches := &jam.ConfigBranches{Repo: configRepo, PRs: prs, Dir: src.Dir, Fetcher: configFetcher, Env: cfgEnv, Live: st, Log: log}
			branchOpts := slices.Clone(uiOpts)
			factory := func(bs jam.Store) http.Handler {
				return adminui.Handler(bs, log, nil, nil, credExists, squawkReader, branchOpts...)
			}
			uiOpts = append(uiOpts, adminui.WithConfigBranches(branches, factory))
		}
		browser := appui.Handler(appui.Deps{
			Gate: appui.NewGate(gc), AuthRoutes: authRoutes, AppTokenRoutes: appTokenRoutes, User: userMux,
			Bearer: appTokenBearer, SessionCookies: []string{browserauth.AppMount().IDCookie(), browserauth.AppMount().AccessCookie()},
			Admin:          adminui.Handler(st, log, sup, personalAllocator{alloc}, credExists, squawkReader, uiOpts...),
			TrustedOrigins: cfg.UIOrigins,
			Shell:          appui.Shell{Brand: appBrand(cfg.displayName()), SignOut: sessions != nil, AdminBadges: adminui.BadgeSource(st, sup, conds)},
		})
		log.Info("Jam app mounted", "path", "/app/")

		// Studio collaboration (SSH certs + tunnels); each plane names its caller.
		// The participant plane mounts the same routes under /me (/me/studios).
		collabDeps := func(principal func(*http.Request) (collab.Principal, string, error)) collabhttp.Deps {
			return collabhttp.Deps{
				Store: st, CA: sshCA, Tunnels: tunnels, Log: log, Now: time.Now, Principal: principal, Live: liveConns,
				// A request on an idled studio resumes it and waits for its stream.
				Resume: sup.Resume, Connected: rsrv.Connected,
				// Previews: what each session exposes, and its URLs (nil = off).
				Exposures: sup.Exposures, PreviewURL: previewURL,
				Notify: func(inst jam.Instance, body string) {
					if _, err := ic.Notify(inst, "", body); err != nil {
						log.Warn("collab: session notice failed", "actor", inst.ActorID, "err", err.Error())
					}
				},
			}
		}
		if mountParticipantStudios(browser, cfg.operatorLoginConfig(), auth, sshCA, st, log, func(p func(*http.Request) (collab.Principal, string, error)) http.Handler {
			return collabhttp.Routes("/me", collabDeps(p))
		}) {
			log.Info("Jam participant collaboration: bearer routes mounted", "path", "/me/studios/")
		} else {
			// Not mounted: answer the CLI with a plain 404, not the /me/ legacy
			// redirect (a 308 to the login page).
			browser.Handle("/me/studios/", http.NotFoundHandler())
		}
		mountVSCodeExtension(browser, vscodeext.Embedded(), participantBearer(cfg.operatorLoginConfig(), auth, st, log))
		if b := vscodeext.Embedded(); b.Available() {
			log.Info("Jam VS Code extension: served", "version", b.Version(), "path", "/me/vscode")
		}
		adminOpts := []jam.AdminOption{
			jam.WithAdminRoute("GET /admin/sessions/{actor_id}/events", sessionevents.ExportHandler(sessStore)),
			jam.WithAdminRoute("GET /admin/attention", condition.AdminHandler(conds)),
			jam.WithAdminRoute("/admin/vscode", vscodeext.Routes("/admin", vscodeext.Embedded(), nil)),
			jam.WithAdminRoute("/admin/vscode/jam.vsix", vscodeext.Routes("/admin", vscodeext.Embedded(), nil)),
			jam.WithAdminRoute("/admin/studios/", collabhttp.Routes("/admin", collabDeps(operatorPrincipal(st)))),
			jam.WithModelSpecs(st),
			jam.WithKitFetcher(st, kitFetcher, log),
			jam.WithConfig(st, configFetcher, cfgEnv, log),
			jam.WithSetupInspect(setupDeps, log),
		}
		if managerHub != nil {
			adminOpts = append(adminOpts, managers.AdminRoutes(managerHub)...)
		}
		admin := jam.NewAdminHandler(st, sup, personalAllocator{alloc}, auth, credExists, cfg.operatorLoginConfig(), log, browser, adminOpts...)
		var adminServe http.Handler = admin
		if cfg.Previews != nil {
			// The previews' login: sessions, single-use codes and the bounce
			// through the login host (validatePreviews ensured browser login).
			_, adminPort, _ := net.SplitHostPort(cfg.AdminListen)
			for _, s := range previewSharedSiteHosts(cfg.Previews.Domain, previewAdminHosts(&cfg)) {
				log.Warn("previews: the previews domain shares a registrable domain with the admin host; preview apps can set cookies the admin site receives — use a separate registrable domain for previews",
					"previews-domain", cfg.Previews.Domain, "admin-host", s.host, "registrable-domain", s.site)
			}
			if msg := previewUIOriginsWarning(&cfg); msg != "" {
				log.Warn(msg)
			}
			previewAuth, err := newPreviewAuth(cfg.Previews.Domain, adminPort, cfg.UIOrigins, frames, *cfg.browserAuthConfig(), cfg.operatorLoginConfig(), auth, st, log)
			if err != nil {
				fmt.Fprintln(stderr, "jam: previews:", err)
				return 1
			}
			go previewAuth.Run(context.Background()) // also prunes frames
			// The preview proxy serves the login host and every preview host;
			// it names its callers through previewAuth, not a plane's Principal.
			previewHandler := collabhttp.Preview(collabDeps(nil), previewAuth, cfg.Previews.Domain)
			inv := previewHandler.InvalidateLabels
			invalidatePreviewLabels.Store(&inv)
			adminServe = hostRouter(cfg.Previews.Domain, previewHandler, admin)
		}
		go func() {
			if adminTLSCfg != nil { // previews on: admin + wildcard preview cert by SNI
				log.Info("Jam admin API listening (TLS, previews)", "addr", cfg.AdminListen, "previews-domain", cfg.Previews.Domain)
				if err := (&http.Server{Addr: cfg.AdminListen, Handler: adminServe, TLSConfig: adminTLSCfg}).ListenAndServeTLS("", ""); err != nil {
					log.Error("admin API stopped", "err", err.Error())
				}
				return
			}
			if cfg.adminUsesTLS() {
				cert, key, _ := cfg.adminTLS()
				log.Info("Jam admin API listening (TLS)", "addr", cfg.AdminListen)
				if err := (&http.Server{Addr: cfg.AdminListen, Handler: adminServe}).ListenAndServeTLS(cert, key); err != nil {
					log.Error("admin API stopped", "err", err.Error())
				}
				return
			}
			log.Info("Jam admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, adminServe); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
	}

	// Cove-facing :443 listener: multiplex the broker (HTTP) and the Attach gRPC
	// on one TLS port (content-type demux), so a hardened cove reaches both
	// within its 443-only egress.
	cert, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	rawLis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	log.Info("Jam broker+Attach listening (mux)", "addr", cfg.Listen)
	if err := serveMux(rawLis, tlsCfg, gs, httpHandler); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	return 0
}

// logTailSeq returns the Seq of the last (newest) message in lg, or 0 when
// the Log is empty. Used to seed the egress low-water at cutover so already-
// delivered shadow history is skipped.
func logTailSeq(lg interface{ TailSeq() (int64, bool) }) int64 {
	seq, _ := lg.TailSeq()
	return seq
}

func main() {
	os.Exit(runAs(filepath.Base(os.Args[0]), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// runAs is the process entry: invokedAs is the basename the binary was run
// under. Before any command runs it copies the pre-rename directories across
// once (migrateDirs). See docs/usage/jam/renamed-from-at.md.
func runAs(invokedAs string, argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	migrateDirs(stderr)
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
		fmt.Fprintln(stderr, "jam pool: expected add|list")
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
		fmt.Fprintln(stderr, "jam pool: --store is required")
		return 2
	}
	ps, err := jam.NewFilePoolStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "jam pool:", err)
		return 1
	}
	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "jam pool add: --name is required")
			return 2
		}
		var data []byte
		if *fromFile == "" {
			if data, err = io.ReadAll(os.Stdin); err != nil {
				fmt.Fprintln(stderr, "jam pool add: read stdin:", err)
				return 1
			}
		} else if data, err = os.ReadFile(*fromFile); err != nil {
			fmt.Fprintln(stderr, "jam pool add:", err)
			return 1
		}
		acct, err := parseClaudeAiOauth(*name, data)
		if err != nil {
			fmt.Fprintln(stderr, "jam pool add:", err)
			return 1
		}
		if err := ps.SetAccount(acct); err != nil {
			fmt.Fprintln(stderr, "jam pool add:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added pool account", acct.Name) // never the tokens
	case "list":
		accts, err := ps.Accounts()
		if err != nil {
			fmt.Fprintln(stderr, "jam pool:", err)
			return 1
		}
		for _, a := range accts {
			fmt.Fprintf(stdout, "%s\texpires %s\n", a.Name, a.ExpiresAt.Format(time.RFC3339))
		}
	default:
		fmt.Fprintln(stderr, "jam pool: unknown subcommand", sub)
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

// resolveSSHCA resolves the ssh block's CA credential (in memory only) and
// parses it as Jam's SSH user CA. Absent ssh ⇒ nil, nil (SSH collaboration
// off). Errors name the credential, never its value.
func resolveSSHCA(cfg serveConfig, specs map[string]secret.Spec) (*sshca.CA, error) {
	if cfg.SSH == nil {
		return nil, nil
	}
	name := cfg.SSH.CACred
	v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[name]})
	if err != nil {
		return nil, fmt.Errorf("ssh.ca-cred: %w", err)
	}
	ca, err := sshca.Parse([]byte(v[name]))
	if err != nil {
		return nil, fmt.Errorf("ssh.ca-cred %q: %w", name, err)
	}
	return ca, nil
}

// resolveBlobs resolves the blobs block's keys (in memory only), builds the
// bucket's store and checks the bucket. Absent blobs ⇒ nil, nil (attachments
// off). Errors name the credential, never its value.
func resolveBlobs(cfg serveConfig, specs map[string]secret.Spec) (blobstore.Store, error) {
	b := cfg.Blobs
	if b == nil {
		return nil, nil
	}
	v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[b.AccessKeyCred], specs[b.SecretKeyCred]})
	if err != nil {
		return nil, fmt.Errorf("blobs credentials: %w", err)
	}
	s, err := blobstore.NewS3(blobstore.S3Config{Endpoint: b.Endpoint, Bucket: b.Bucket, Region: b.Region, PathStyle: b.PathStyle,
		AccessKey: v[b.AccessKeyCred], SecretKey: v[b.SecretKeyCred]})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Check(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveKitFetcher builds the one long-lived git fetcher for kit sources,
// resolving each kits.git credential (in memory only). Always non-nil: with no
// kits block it fetches anonymously. Errors name the credential, never its value.
func resolveKitFetcher(cfg serveConfig, specs map[string]secret.Spec) (*gitsource.Fetcher, error) {
	f := &gitsource.Fetcher{MaxBytes: 50 << 20, Timeout: 60 * time.Second}
	// The followed config repo's credential, for exactly its URL (the longest
	// prefix, so it wins over a kits.git prefix above it).
	if src := cfg.followedSource(); src.Repo != "" && cfg.configRepo().kind != "git" {
		host := cfg.configRepo()
		name := host.Cred
		spec := specs[name]
		resolve := func() (string, error) {
			v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{spec})
			if err != nil {
				return "", err
			}
			return v[spec.Name], nil
		}
		if _, err := resolve(); err != nil {
			return nil, fmt.Errorf("config.%s.cred %q: %w", host.kind, name, err)
		}
		cr := gitsource.Cred{Prefix: src.Repo, Name: name, Resolve: resolve, Key: "config." + host.kind}
		if host.kind == "gitlab" {
			cr.User = "oauth2" // GitLab's username for a token, on any host
		}
		f.Creds = append(f.Creds, cr)
	}
	if cfg.Kits == nil {
		return f, nil
	}
	for i, g := range cfg.Kits.Git {
		spec := specs[g.Cred]
		resolve := func() (string, error) {
			v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{spec})
			if err != nil {
				return "", err
			}
			return v[spec.Name], nil
		}
		// Resolve once now so a broken supply fails serve at startup, then
		// again on every fetch so a minted or rotated token stays current.
		if _, err := resolve(); err != nil {
			return nil, fmt.Errorf("kits.git[%d].cred %q: %w", i, g.Cred, err)
		}
		f.Creds = append(f.Creds, gitsource.Cred{Prefix: g.URL, Name: g.Cred, Resolve: resolve})
	}
	return f, nil
}

// newConfigRepo is the clone of the followed config repo, under dir. A
// github or gitlab repo authenticates through the kit fetcher's credential;
// a git repo carries its own Auth: none for a local path, the ssh key
// (resolved on every use, and once now so a broken supply fails serve) with
// the pinned host keys, written to dir, for ssh.
func newConfigRepo(cfg serveConfig, specs map[string]secret.Spec, f *gitsource.Fetcher, dir string) (*configrepo.Repo, error) {
	src, host := cfg.followedSource(), cfg.configRepo()
	repo := &configrepo.Repo{URL: src.Repo, Branch: src.Ref, Path: filepath.Join(dir, "config-repo")}
	if host.kind != "git" {
		repo.Creds = f.Creds
		return repo, nil
	}
	if configrepo.IsLocal(src.Repo) {
		repo.Auth = func() (transport.AuthMethod, error) { return nil, nil }
		return repo, nil
	}
	spec := specs[host.Cred]
	key := func() (string, error) {
		v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{spec})
		if err != nil {
			return "", err
		}
		return v[spec.Name], nil
	}
	if _, err := key(); err != nil {
		return nil, fmt.Errorf("config.git.cred %q: %w", host.Cred, err)
	}
	// Host keys are public; the file is the only thing written.
	khFile := filepath.Join(dir, "config-repo-known_hosts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(khFile, []byte(strings.TrimRight(host.HostKeys, "\n")+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("config.git.host-keys: %w", err)
	}
	repo.Auth = configrepo.SSHAuth(configrepo.SSHUser(src.Repo), key, khFile)
	return repo, nil
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

// collabLabel shows a collaboration policy value, spelling out the default.
func collabLabel(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// The tunnel broker is what the supervisor closes a studio's tunnels through,
// and what tells wake-on a studio has one in use.
var (
	_ jam.TunnelCloser = (*attach.TunnelBroker)(nil)
	_ wakeon.Tunnels   = (*attach.TunnelBroker)(nil)
)

// mountParticipantStudios mounts the participant plane's collaboration routes
// at /me/studios/ on mux and reports whether it did. They take a bearer
// credential — an app token (docs/usage/jam/app-tokens.md) or an ID token
// from `jam login --participant` — not the /app session cookie, so they sit
// outside the /app gate and RequireSameOrigin. They mount whenever the SSH CA
// is set; ID tokens are added only with device login (lc) and an OIDC
// operator authenticator, verified against the provider already discovered
// with the device client as audience. Without the CA nothing is mounted and
// the caller registers a plain 404 on /me/studios/ (it would otherwise match
// the /me/ legacy redirect): never open.
func mountParticipantStudios(mux *http.ServeMux, lc *jam.OperatorLoginConfig, auth jam.OperatorAuthenticator, ca *sshca.CA, store jam.Store, log *slog.Logger, routes func(func(*http.Request) (collab.Principal, string, error)) http.Handler) bool {
	if ca == nil {
		return false
	}
	mux.Handle("/me/studios/", routes(participantBearer(lc, auth, store, log)))
	return true
}

// participantBearer is the /me plane's bearer principal: an app token, or an
// ID token from `jam login --participant` when device login (lc) and an OIDC
// operator authenticator are configured.
func participantBearer(lc *jam.OperatorLoginConfig, auth jam.OperatorAuthenticator, store jam.Store, log *slog.Logger) func(*http.Request) (collab.Principal, string, error) {
	var idTok func(*http.Request) (collab.Principal, string, error)
	if oidcAuth, ok := auth.(*jam.OIDCAuthenticator); ok && lc != nil {
		idTok = collabhttp.ParticipantBearer(oidcAuth.IDTokenVerifier(lc.ClientID), store)
	}
	return participantStudiosBearer(collabhttp.AppTokenBearer(store, time.Now, log), idTok)
}

// mountVSCodeExtension serves the embedded VS Code extension (vscodeext) at
// /me/vscode and /me/vscode/jam.vsix to any /me bearer (principal); like
// /me/studios/ they sit outside the /app gate. The operator plane's copy is
// an admin route (/admin/vscode).
func mountVSCodeExtension(mux *http.ServeMux, b vscodeext.Bundle, principal func(*http.Request) (collab.Principal, string, error)) {
	h := vscodeext.Routes("/me", b, func(r *http.Request) error {
		_, _, err := principal(r)
		return err
	})
	mux.Handle("/me/vscode", h)
	mux.Handle("/me/vscode/jam.vsix", h)
}

// participantStudiosBearer sends a JWT-shaped bearer (exactly two dots) to
// idTok — ErrInvalidToken when ID tokens aren't accepted (idTok nil) — and
// anything else, no credential included, to appTok.
func participantStudiosBearer(appTok, idTok func(*http.Request) (collab.Principal, string, error)) func(*http.Request) (collab.Principal, string, error) {
	return func(r *http.Request) (collab.Principal, string, error) {
		scheme, tok, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		if strings.EqualFold(scheme, "Bearer") && strings.Count(strings.TrimSpace(tok), ".") == 2 {
			if idTok == nil {
				return collab.Principal{}, "", collabhttp.ErrInvalidToken
			}
			return idTok(r)
		}
		return appTok(r)
	}
}

// operatorPrincipal names an operator-plane caller (behind the admin
// authenticator): the operator, and the roster user its login links to when
// there is one, for audit and notice names.
func operatorPrincipal(st jam.Store) func(*http.Request) (collab.Principal, string, error) {
	return func(r *http.Request) (collab.Principal, string, error) {
		op := jam.OperatorID(r)
		if op == "" {
			return collab.Principal{}, "", collabhttp.ErrNoCredentials
		}
		p, name := collab.Principal{Operator: op}, op
		if u, ok := st.UserByLogin(op); ok {
			p.UserID, name = u.ID, u.Name
		}
		return p, name, nil
	}
}

// appBrand is the title-bar brand: "{display name} Jam", or "" (the bar then
// says "Jam") when no display name is set.
func appBrand(name string) string {
	if name == "" {
		return ""
	}
	return name + " Jam"
}
