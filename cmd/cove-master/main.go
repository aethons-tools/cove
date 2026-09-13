// Command cove-master is the in-cove primary process (first limb): it connects to
// harbor's Attach stream and supervises the cove's workload. This slice ships a
// stub workload; the real agent wrapper — and cove-master becoming the image
// entrypoint in its own non-root account — are later slices. It reads its
// configuration from the environment (no SSH, no host orchestration):
//
//	AT_HARBOR_RUNTIME_ADDR   harbor's runtime (Attach) listener, host:port
//	AT_HARBOR_IDENTITY_TOKEN the cove's identity token
//	AT_HARBOR_LAUNCH_SECRET  the per-instance launch secret
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aethons-tools/cove/internal/covemaster"
)

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
		// Plaintext TCP this slice; :443/TLS mux is deferred.
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}, nil
}

// stubWorkload is the placeholder supervised unit for this slice: it reports
// Running and waits for teardown. The real agent wrapper replaces it next slice.
type stubWorkload struct{ log *slog.Logger }

func (s stubWorkload) Run(ctx context.Context, h covemaster.Handle) error {
	h.Report(covemaster.Running)
	<-ctx.Done()
	return ctx.Err()
}
func (s stubWorkload) Control(c covemaster.Control) {
	if s.log != nil {
		s.log.Info("control", "kind", c.Kind)
	}
}

func run(getenv func(string) string, stderr *os.File) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := buildConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := covemaster.New(cfg, log).Run(ctx, stubWorkload{log: log}); err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Getenv, os.Stderr)) }
