// Command at-harbor is the central credential broker. `serve` runs the
// credential-injecting reverse proxy; `enroll`/`revoke` manage identities.
// See docs/superpowers/specs/2026-09-10-harbor-broker-guest-mvp-design.md.
package main

import (
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
	"github.com/aethons-tools/cove/internal/runner"
)

var version = "dev"

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-harbor",
		Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the credential broker", Run: cmdServe},
			{Name: "enroll", Brief: "mint an identity and print its Guest snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "remove an identity", Run: cmdRevoke},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	store := fs.String("store", "", "path to the identity store")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	baseURL := fs.String("base-url", "", "harbor base URL for the printed snippet")
	ttl := fs.Duration("ttl", 0, "identity lifetime (0 = no expiry)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor enroll: takes no positional arguments")
		return 2
	}
	if *store == "" || *id == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "at-harbor enroll: --store, --id and --base-url are required")
		return 2
	}
	st, err := harbor.NewFileStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	tok, err := harbor.Enroll(st, *id, *project, *role, splitCSV(*dests), splitCSV(*repos), *ttl, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprint(stdout, harbor.RenderEnrollSnippet(*baseURL, tok))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	store := fs.String("store", "", "path to the identity store")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *store == "" || *id == "" {
		fmt.Fprintln(stderr, "at-harbor revoke: --store and --id are required")
		return 2
	}
	st, err := harbor.NewFileStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if err := st.Remove(*id); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
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
	creds := harbor.NewSecretResolver(runner.OS{}, cfg.credSpecs())
	broker := harbor.NewBroker(st, creds, log)
	srv := &http.Server{Addr: cfg.Listen, Handler: broker}
	log.Info("harbor listening", "addr", cfg.Listen)
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
