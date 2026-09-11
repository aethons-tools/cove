// Command at-harbor is the central credential broker + control plane. `serve`
// runs the credential-injecting reverse proxy and a loopback admin API;
// `enroll`/`revoke`/`destination` are admin-API clients. See the harbor specs
// under docs/superpowers/specs/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminclient"
	"github.com/aethons-tools/cove/internal/harbor/deviceflow"
	"github.com/aethons-tools/cove/internal/runner"
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
			{Name: "login", Brief: "sign in via OIDC device flow and cache the operator token", Run: cmdLogin},
			{Name: "logout", Brief: "clear the cached operator token", Run: cmdLogout},
			{Name: "whoami", Brief: "show the cached operator identity", Run: cmdWhoami},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdLogin(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	settings := loadSettings()
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor login: unexpected arguments")
		return 2
	}
	lc, err := adminclient.New(*adminURL, "").LoginConfig()
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
	if err := saveToken(cachedToken{AccessToken: tok.AccessToken, Sub: sub, Expiry: exp, AdminURL: *adminURL}); err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s; token expires %s\n", sub, exp.Format(time.RFC3339))
	return 0
}

func cmdLogout(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if err := clearToken(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "logged out")
	return 0
}

func cmdWhoami(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if t, ok := loadToken(); ok {
		fmt.Fprintf(stdout, "%s @ %s (expires %s)\n", t.Sub, t.AdminURL, t.Expiry.Format(time.RFC3339))
		return 0
	}
	if _, err := os.Stat(tokenPath()); err == nil {
		fmt.Fprintln(stdout, "session expired; run `at-harbor login`")
	} else {
		fmt.Fprintln(stdout, "not logged in")
	}
	return 0
}

// resolveToken applies the operator-token precedence: an explicit flag/env value
// wins, else the cached login token — but only when it was minted against this
// same adminURL, so a token is never replayed to a different harbor.
func resolveToken(flagVal, adminURL string) string {
	if flagVal != "" {
		return flagVal
	}
	if ct, ok := loadToken(); ok && ct.AdminURL == adminURL {
		return ct.AccessToken
	}
	return ""
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	settings := loadSettings()
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	baseURL := fs.String("base-url", settings.BaseURL, "harbor broker base URL for the printed snippet")
	ttl := fs.Duration("ttl", 0, "identity lifetime (0 = no expiry)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *id == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "at-harbor enroll: --id and --base-url are required")
		return 2
	}
	res, err := adminclient.New(*adminURL, resolveToken(*token, *adminURL)).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
		Destinations: splitCSV(*dests), Repos: splitCSV(*repos), TTL: *ttl,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprint(stdout, harbor.RenderEnrollSnippet(*baseURL, res.Token))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	settings := loadSettings()
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *id == "" {
		fmt.Fprintln(stderr, "at-harbor revoke: --id is required")
		return 2
	}
	if err := adminclient.New(*adminURL, resolveToken(*token, *adminURL)).Revoke(*id); err != nil {
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
	settings := loadSettings()
	fs := flag.NewFlagSet("destination "+sub, flag.ContinueOnError)
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
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
	c := adminclient.New(*adminURL, resolveToken(*token, *adminURL))
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
	st, err := harbor.NewFileStore(cfg.Store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	specs := cfg.credSpecs()
	creds := harbor.NewSecretResolver(runner.OS{}, specs)
	broker := harbor.NewBroker(st, creds, log)

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
		admin := harbor.NewAdminHandler(st, auth, credExists, cfg.operatorLoginConfig(), log)
		go func() {
			log.Info("harbor admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, admin); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
	}

	srv := &http.Server{Addr: cfg.Listen, Handler: broker}
	log.Info("harbor broker listening", "addr", cfg.Listen)
	if err := srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key); err != nil {
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
