// Command at-mint mints short-lived tokens (GitHub App installation tokens,
// Anthropic oauth tokens) for use inside a cove sandbox. Non-secret inputs come
// from flags; secret inputs come only from env. Exactly one token is printed to
// stdout on success; on any error, nothing is printed to stdout and a diagnostic
// goes to stderr with a non-zero exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, http.DefaultClient, os.Stdout, os.Stderr))
}

// run dispatches the at-mint subcommands. env and httpc are injected for tests.
func run(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-mint",
		Version: version,
		Commands: []cli.Command{
			{Name: "github", Brief: "mint a repo-scoped GitHub App installation token", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doGitHub(a, env, httpc, out, errw)
			}},
			{Name: "anthropic", Brief: "mint an Anthropic oauth token via Auth0 WIF", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doAnthropic(a, env, httpc, out, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}

// getenv reads a var via the injected lookup.
func getenv(env func(string) (string, bool), k string) string { v, _ := env(k); return v }

func doGitHub(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("github", flag.ContinueOnError)
	fs.SetOutput(stderr)
	appID := fs.String("app-id", "", "GitHub App id (non-secret)")
	installID := fs.String("install-id", "", "GitHub App installation id (non-secret)")
	appKeyFile := fs.String("app-key-file", "", "path to the App private-key PEM (a path is non-secret)")
	repo := fs.String("repo", "", "owner/name to scope the token to (non-secret)")
	if _, err := cli.ParseInterspersed(fs, args); err != nil {
		return 2
	}
	keyPEM, err := readKeyPEM(*appKeyFile, getenv(env, "AT_MINT_GITHUB_APP_KEY"))
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	in := githubInput{AppID: *appID, InstallID: *installID, KeyPEM: keyPEM, Repo: *repo}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := mintGitHub(ctx, httpc, in, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, tok)
	return 0
}

// readKeyPEM prefers the file path (non-secret) and falls back to env content.
func readKeyPEM(path, envContent string) ([]byte, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading --app-key-file: %w", err)
		}
		return b, nil
	}
	if envContent != "" {
		return []byte(envContent), nil
	}
	return nil, fmt.Errorf("no App private key: set --app-key-file or AT_MINT_GITHUB_APP_KEY")
}

func doAnthropic(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("anthropic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tenant := fs.String("auth0-tenant", "", "Auth0 tenant domain, e.g. tenant.us.auth0.com (non-secret)")
	clientID := fs.String("auth0-client-id", "", "Auth0 M2M client id (non-secret)")
	audience := fs.String("auth0-audience", "", "Auth0 API identifier / token aud (non-secret)")
	org := fs.String("anthropic-org", "", "Anthropic organization id (non-secret)")
	rule := fs.String("anthropic-rule", "", "Anthropic federation rule id fdrl_... (non-secret)")
	svc := fs.String("anthropic-service-account", "", "Anthropic service account id svac_... (non-secret)")
	workspace := fs.String("anthropic-workspace", "", "Anthropic workspace id (optional, non-secret)")
	if _, err := cli.ParseInterspersed(fs, args); err != nil {
		return 2
	}
	secret := getenv(env, "AT_MINT_AUTH0_CLIENT_SECRET")
	if secret == "" {
		fmt.Fprintln(stderr, "at-mint: AT_MINT_AUTH0_CLIENT_SECRET is not set")
		return 1
	}
	in := anthropicInput{
		Tenant: *tenant, ClientID: *clientID, Audience: *audience, ClientSecret: secret,
		Org: *org, Rule: *rule, ServiceAccount: *svc, Workspace: *workspace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := mintAnthropic(ctx, httpc, in)
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	// Surface the lifetime we used to discard: the token's TTL is set by the
	// federation rule server-side, and a token shorter than the run silently 401s
	// mid-way. refresh_token presence flags whether an in-VM refresh path exists.
	refresh := "absent"
	if resp.RefreshToken != "" {
		refresh = "present"
	}
	fmt.Fprintf(stderr, "at-mint: anthropic token minted (expires_in=%ds, refresh_token=%s)\n", resp.ExpiresIn, refresh)
	// Fail closed when the token would expire before the run finishes: at-cove work
	// passes COVE_RUN_TIMEOUT to the resolver, and a bearer whose TTL is below that
	// is guaranteed to 401 mid-run (build/prepare overhead makes it worse). Catch it
	// here, before any VM is built, instead of burning a run on a doomed token.
	if to := getenv(env, "COVE_RUN_TIMEOUT"); to != "" && resp.ExpiresIn > 0 {
		if d, perr := time.ParseDuration(to); perr == nil && time.Duration(resp.ExpiresIn)*time.Second < d {
			fmt.Fprintf(stderr, "at-mint: minted token TTL %ds is shorter than the run timeout %s; it would expire mid-run — raise the Auth0 API token expiration or the federation rule token lifetime\n", resp.ExpiresIn, d)
			return 1
		}
	}
	fmt.Fprintln(stdout, resp.AccessToken)
	return 0
}
