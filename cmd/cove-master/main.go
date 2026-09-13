// Command cove-master is the in-cove primary process (first limb): it connects to
// harbor's Attach stream and supervises the cove's workload — now the real agent
// wrapper, running claude `-p` as a headless one-shot. cove-master becoming the
// image entrypoint in its own non-root account is a later slice. It reads its
// configuration from the environment (no SSH, no host orchestration):
//
//	AT_HARBOR_RUNTIME_ADDR   harbor's cove-facing :443 address (host:443), dialed
//	                         over TLS through the cove's squid CONNECT proxy
//	AT_HARBOR_IDENTITY_TOKEN the cove's identity token
//	AT_HARBOR_LAUNCH_SECRET  the per-instance launch secret
//	AT_COVE_WORKDIR          the agent's cwd + where .at-task/worker-result.json is read (default /home/agent/workspace)
//	AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/aethons-tools/cove/internal/agentrun"
	"github.com/aethons-tools/cove/internal/covemaster"
)

// clientTransportCreds dials harbor over TLS, validating against the system trust
// store. harbor serves the Attach gRPC on its cove-facing :443 TLS listener; the
// cove reaches it through the squid CONNECT proxy (grpc-go's built-in dialer
// honors https_proxy). ServerName is filled from the dial target authority.
func clientTransportCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{})
}

func buildConfig(getenv func(string) string) (covemaster.Config, error) {
	addr := getenv("AT_HARBOR_RUNTIME_ADDR")
	token := getenv("AT_HARBOR_IDENTITY_TOKEN")
	secret := getenv("AT_HARBOR_LAUNCH_SECRET")
	switch {
	case addr == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_RUNTIME_ADDR is required")
	case token == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_IDENTITY_TOKEN is required")
	case secret == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_LAUNCH_SECRET is required")
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
	return agentrun.Config{WorkDir: workdir, Prompt: string(prompt)}, nil
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

func main() { os.Exit(run(os.Getenv, os.Stderr)) }
