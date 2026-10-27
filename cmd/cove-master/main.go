// Command cove-master is the in-cove primary process (first limb): it connects to
// Jam's Attach stream and supervises the cove's workload — now the real agent
// wrapper, running claude `-p` headless in stream-json episodes. cove-master becoming the
// image entrypoint in its own non-root account is a later slice. It reads its
// configuration from the environment (no SSH, no host orchestration):
//
//	JAM_RUNTIME_ADDR       Jam's cove-facing :443 address (host:443), dialed
//	                       over TLS through the cove's squid CONNECT proxy
//	JAM_IDENTITY_TOKEN     the cove's identity token
//	JAM_LAUNCH_SECRET      the per-instance launch secret
//
//	JAM_WORKDIR            the agent's cwd (default /home/agent/workspace)
//	JAM_AGENT_PROMPT_FILE  path to the file holding the agent's prompt (required)
//	JAM_RESIDENT           "1"/"true" keeps the agent resident between turns
//	                       (personal sessions): it waits for a Wake after every turn
//	JAM_BASE_URL           https://<jam host>; when set, the agent's connector is re-fetched
//	                       (GET /connector) before every spawn
//	JAM_CONNECTOR          the raise-time connector (JSON, no token): fallback + owned env keys
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/aethons-tools/cove/internal/agentrun"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studioagent"
)

// clientTransportCreds dials Jam over TLS, validating against the system trust
// store. Jam serves the Attach gRPC on its cove-facing :443 TLS listener; the
// cove reaches it through the squid CONNECT proxy (grpc-go's built-in dialer
// honors https_proxy). ServerName is filled from the dial target authority.
func clientTransportCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{})
}

// jamEnv reads the JAM_<suffix> variable. Values are never logged.
func jamEnv(getenv func(string) string, suffix string) string {
	return getenv("JAM_" + suffix)
}

func buildConfig(getenv func(string) string, log *slog.Logger) (studioagent.Config, error) {
	addr := jamEnv(getenv, "RUNTIME_ADDR")
	token := jamEnv(getenv, "IDENTITY_TOKEN")
	secret := jamEnv(getenv, "LAUNCH_SECRET")
	switch {
	case addr == "":
		return studioagent.Config{}, fmt.Errorf("JAM_RUNTIME_ADDR is required")
	case token == "":
		return studioagent.Config{}, fmt.Errorf("JAM_IDENTITY_TOKEN is required")
	case secret == "":
		return studioagent.Config{}, fmt.Errorf("JAM_LAUNCH_SECRET is required")
	}
	return studioagent.Config{
		Addr: addr, Token: token, LaunchSecret: secret,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(clientTransportCreds())},
		IDE:         studioagent.NewIDE(studioagent.IDEConfig{}, log),
	}, nil
}

func buildAgentConfig(getenv func(string) string) (agentrun.Config, error) {
	workdir := getenv("JAM_WORKDIR")
	if workdir == "" {
		workdir = "/home/agent/workspace"
	}
	promptFile := getenv("JAM_AGENT_PROMPT_FILE")
	if promptFile == "" {
		return agentrun.Config{}, fmt.Errorf("JAM_AGENT_PROMPT_FILE is required")
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return agentrun.Config{}, fmt.Errorf("reading JAM_AGENT_PROMPT_FILE: %w", err)
	}
	resident := getenv("JAM_RESIDENT")
	cfg := agentrun.Config{WorkDir: workdir, Prompt: string(prompt), Resident: resident == "1" || resident == "true"}
	if base := getenv("JAM_BASE_URL"); base != "" {
		token := jamEnv(getenv, "IDENTITY_TOKEN")
		var initial snippet.Connector
		if raw := getenv("JAM_CONNECTOR"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &initial); err != nil {
				initial = snippet.Connector{} // the first successful fetch fills it
			}
		}
		cfg.Connector = &agentrun.ConnectorConfig{
			Source: agentrun.HTTPConnectorSource(base, token), BaseURL: base, Token: token, Initial: initial,
		}
	}
	if cf := getenv("JAM_AGENT_CONTEXT_FILE"); cf != "" {
		// Not fatal: a missing or bad bundle runs the agent without context.
		if raw, err := os.ReadFile(cf); err == nil {
			var b sessionctx.Bundle
			if json.Unmarshal(raw, &b) == nil {
				cfg.Context = &b
			}
		}
	}
	cfg.SessionKind = getenv("JAM_SESSION_KIND")
	// Live refresh (GET /context) needs a raise-time bundle to start from.
	if base := getenv("JAM_BASE_URL"); base != "" && cfg.Context != nil {
		cfg.ContextSource = agentrun.HTTPContextSource(base, jamEnv(getenv, "IDENTITY_TOKEN"))
	}
	return cfg, nil
}

func run(getenv func(string) string, stderr *os.File) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := buildConfig(getenv, log)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	agentCfg, err := buildAgentConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := studioagent.New(cfg, log).Run(ctx, agentrun.New(agentCfg, log)); err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 1
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runMCP(os.Getenv, os.Stderr))
		return
	}
	os.Exit(run(os.Getenv, os.Stderr))
}
