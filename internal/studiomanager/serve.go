package studiomanager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/imagefacts"
	"github.com/aethons-tools/cove/internal/jam/launcher"
	"github.com/aethons-tools/cove/internal/jam/managers/client"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/runner"
)

// Deps are Serve's seams; zero values are the real ones.
type Deps struct {
	Runner runner.Runner                                      // nil: runner.OS{}
	Dial   func(cfg Config) (grpc.ClientConnInterface, error) // nil: TLS grpc.NewClient
	Ops    launcher.Backend                                   // nil: colima.NewWithContext(Runner, cfg.Colima.DockerContext)
	Images jam.ImageFactsReader                               // nil: imagefacts.New(Runner, the colima backend)
	Sleep  func(ctx context.Context, d time.Duration)         // nil: Loop's default; tests shorten the backoff
}

// Serve runs the manager named by cfg until ctx ends: it builds the Colima
// launcher behind the capacity gate and keeps one session with Jam open,
// reconnecting with backoff. It returns an error only for a setup failure;
// once running, it returns nil after ctx ends.
func Serve(ctx context.Context, cfg Config, version string, log *slog.Logger, deps Deps) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Fail fast on an unreadable token; each session rereads it, so after a
	// rotate updating token-file is enough.
	if _, err := cfg.Token(); err != nil {
		return err
	}
	r := deps.Runner
	if r == nil {
		r = runner.OS{}
	}
	pub, err := keys.EnsureFile(r, cfg.Colima.IdentityFile)
	if err != nil {
		return fmt.Errorf("launcher identity key: %w", err)
	}
	if err := os.MkdirAll(cfg.Colima.KnownHostsDir, 0o700); err != nil {
		return fmt.Errorf("known-hosts dir: %w", err)
	}
	ops, images := deps.Ops, deps.Images
	if ops == nil || images == nil {
		col := colima.NewWithContext(r, cfg.Colima.DockerContext)
		if ops == nil {
			ops = col
		}
		if images == nil {
			images = imagefacts.New(r, col)
		}
	}
	dial := deps.Dial
	if dial == nil {
		tc, err := tlsConfig(cfg)
		if err != nil {
			return err
		}
		dial = func(cfg Config) (grpc.ClientConnInterface, error) {
			return grpc.NewClient(cfg.JamAddr, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
		}
	}

	l := launcher.New(launcher.Config{
		Ops: ops, Runner: r, Manager: cfg.Name,
		JamHost: cfg.Colima.JamHost, RuntimeAddr: cfg.Colima.RuntimeAddr,
		IdentityFile: cfg.Colima.IdentityFile, KnownHostsDir: cfg.Colima.KnownHostsDir,
		DNS: cfg.Colima.DNS, Docker: cfg.Colima.Docker,
		PublicKey: pub, Subscription: false, Log: log,
	})
	g := &gated{Launcher: l, capacity: cfg.Capacity}

	loop := Loop{Sleep: deps.Sleep, Log: log, Run: func(ctx context.Context, welcomed func()) error {
		token, err := cfg.Token()
		if err != nil {
			return err
		}
		cc, err := dial(cfg)
		if err != nil {
			return fmt.Errorf("dial %s: %w", cfg.JamAddr, err)
		}
		if c, ok := cc.(io.Closer); ok {
			defer c.Close()
		}
		return client.Run(ctx, cc, client.Config{
			Name: cfg.Name, Token: token, Version: version, Capacity: cfg.Capacity,
			Launcher: g, Images: images, Inventory: l.Studios, Log: log,
			OnWelcome: func(w *managerpb.Welcome) {
				l.SetSSHUserCA(w.GetSshUserCa())
				log.Info("manager: connected to Jam", "jam", cfg.JamAddr, "name", cfg.Name)
				welcomed()
			},
		})
	}}
	return loop.Serve(ctx)
}

// tlsConfig trusts the system roots plus cfg.CAFile, if any.
func tlsConfig(cfg Config) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca-file: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca-file %s holds no PEM certificate", cfg.CAFile)
		}
	}
	return &tls.Config{RootCAs: roots, ServerName: cfg.ServerName, MinVersion: tls.VersionTLS12}, nil
}
