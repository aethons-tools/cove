// Command cove-master is the in-cove primary process (first limb): it connects to
// Jam's Attach stream and supervises the cove's workload — now the real agent
// wrapper, running claude `-p` headless in stream-json episodes. cove-master becoming the
// image entrypoint in its own non-root account is a later slice. It reads its
// configuration from the environment (no SSH, no host orchestration):
//
//	AT_JAM_RUNTIME_ADDR      Jam's cove-facing :443 address (host:443), dialed
//	                         over TLS through the cove's squid CONNECT proxy
//	AT_JAM_IDENTITY_TOKEN    the cove's identity token
//	AT_JAM_LAUNCH_SECRET     the per-instance launch secret
//
// Each AT_JAM_* variable falls back to its deprecated AT_HARBOR_* name, which
// an older Jam launcher sets (docs/usage/jam/renamed-from-harbor.md).
//
//	AT_COVE_WORKDIR          the agent's cwd (default /home/agent/workspace)
//	AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
//	AT_COVE_RESIDENT         "1"/"true" keeps the agent resident between turns
//	                         (personal sessions): it waits for a Wake after every turn
//	AT_JAM_BASE_URL          https://<jam host>; when set, the agent's connector is re-fetched
//	                         (GET /connector) before every spawn
//	AT_JAM_CONNECTOR         the raise-time connector (JSON, no token): fallback + owned env keys
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
	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// clientTransportCreds dials Jam over TLS, validating against the system trust
// store. Jam serves the Attach gRPC on its cove-facing :443 TLS listener; the
// cove reaches it through the squid CONNECT proxy (grpc-go's built-in dialer
// honors https_proxy). ServerName is filled from the dial target authority.
func clientTransportCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{})
}

// jamEnv reads the AT_JAM_<suffix> variable, falling back to the deprecated
// AT_HARBOR_<suffix> name. Values are never logged.
func jamEnv(getenv func(string) string, suffix string) string {
	if v := getenv("AT_JAM_" + suffix); v != "" {
		return v
	}
	return getenv("AT_HARBOR_" + suffix)
}

func buildConfig(getenv func(string) string) (covemaster.Config, error) {
	addr := jamEnv(getenv, "RUNTIME_ADDR")
	token := jamEnv(getenv, "IDENTITY_TOKEN")
	secret := jamEnv(getenv, "LAUNCH_SECRET")
	switch {
	case addr == "":
		return covemaster.Config{}, fmt.Errorf("AT_JAM_RUNTIME_ADDR is required")
	case token == "":
		return covemaster.Config{}, fmt.Errorf("AT_JAM_IDENTITY_TOKEN is required")
	case secret == "":
		return covemaster.Config{}, fmt.Errorf("AT_JAM_LAUNCH_SECRET is required")
	}
	return covemaster.Config{
		Addr: addr, Token: token, LaunchSecret: secret,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(clientTransportCreds())},
	}, nil
}

func buildAgentConfig(getenv func(string) string) (agentrun.Config, error) {
	workdir := getenv("AT_COVE_WORKDIR")
	if workdir == "" {
		workdir = "/home/agent/workspace"
	}
	promptFile := getenv("AT_COVE_AGENT_PROMPT_FILE")
	if promptFile == "" {
		return agentrun.Config{}, fmt.Errorf("AT_COVE_AGENT_PROMPT_FILE is required")
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return agentrun.Config{}, fmt.Errorf("reading AT_COVE_AGENT_PROMPT_FILE: %w", err)
	}
	resident := getenv("AT_COVE_RESIDENT")
	cfg := agentrun.Config{WorkDir: workdir, Prompt: string(prompt), Resident: resident == "1" || resident == "true"}
	if base := getenv("AT_JAM_BASE_URL"); base != "" {
		token := jamEnv(getenv, "IDENTITY_TOKEN")
		var initial snippet.Connector
		if raw := getenv("AT_JAM_CONNECTOR"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &initial); err != nil {
				initial = snippet.Connector{} // the first successful fetch fills it
			}
		}
		cfg.Connector = &agentrun.ConnectorConfig{
			Source: agentrun.HTTPConnectorSource(base, token), BaseURL: base, Token: token, Initial: initial,
		}
	}
	if cf := getenv("AT_COVE_AGENT_CONTEXT_FILE"); cf != "" {
		// Not fatal: a missing or bad bundle runs the agent without context.
		if raw, err := os.ReadFile(cf); err == nil {
			var b sessionctx.Bundle
			if json.Unmarshal(raw, &b) == nil {
				cfg.Context = &b
			}
		}
	}
	cfg.SessionKind = getenv("AT_COVE_SESSION_KIND")
	// Live refresh (GET /context) needs a raise-time bundle to start from.
	if base := getenv("AT_JAM_BASE_URL"); base != "" && cfg.Context != nil {
		cfg.ContextSource = agentrun.HTTPContextSource(base, jamEnv(getenv, "IDENTITY_TOKEN"))
	}
	return cfg, nil
}

func run(getenv func(string) string, stderr *os.File) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := buildConfig(getenv)
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
	if err := covemaster.New(cfg, log).Run(ctx, agentrun.New(agentCfg, log)); err != nil {
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
