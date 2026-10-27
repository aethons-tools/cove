package studiomanager

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
	"github.com/aethons-tools/cove/internal/jam/managers"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/runner"
)

// recordingReconciler records each manager inventory Jam reconciles.
type recordingReconciler struct {
	mu  sync.Mutex
	got []jam.ManagerInventory
}

func (r *recordingReconciler) ReconcileManager(_ string, inv jam.ManagerInventory) jam.ManagerReconcile {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, inv)
	return jam.ManagerReconcile{}
}

func (r *recordingReconciler) inventories() []jam.ManagerInventory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// jamEnv is a real Hub, served over a swappable bufconn listener.
type jamEnv struct {
	hub    *managers.Hub
	router *fleet.Router
	rec    *recordingReconciler

	mu  sync.Mutex
	lis *bufconn.Listener
	srv *grpc.Server
}

func newJamEnv(t *testing.T, sshCA []byte) *jamEnv {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	e := &jamEnv{router: fleet.New(log), rec: &recordingReconciler{}}
	e.hub = managers.NewHub(managers.Config{
		Store: jam.NewMemStore(), Router: e.router, Reconciler: e.rec,
		Version: "v1", Log: log, SSHUserCA: sshCA,
	})
	if err := e.hub.Start(); err != nil {
		t.Fatal(err)
	}
	e.start()
	t.Cleanup(e.stop)
	return e
}

// start serves the hub on a fresh listener, which dial then returns.
func (e *jamEnv) start() {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	managerpb.RegisterStudioManagerServer(srv, e.hub)
	go func() { _ = srv.Serve(lis) }()
	e.mu.Lock()
	e.lis, e.srv = lis, srv
	e.mu.Unlock()
}

func (e *jamEnv) stop() {
	e.mu.Lock()
	srv := e.srv
	e.mu.Unlock()
	srv.Stop()
}

func (e *jamEnv) dial(Config) (grpc.ClientConnInterface, error) {
	e.mu.Lock()
	lis := e.lis
	e.mu.Unlock()
	return grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func (e *jamEnv) connected(name string) bool {
	for _, s := range e.hub.Statuses() {
		if s.Name == name {
			return s.Connected
		}
	}
	return false
}

// eventually polls cond for up to 3s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startServe adds m1 to the hub, writes its token and runs Serve for it
// against e with ops, logging to logs (nil: discard); it returns the Config
// and m1's token once Serve has been started.
func startServe(t *testing.T, e *jamEnv, ops *fakeBackend, logs *syncBuffer) (Config, string) {
	t.Helper()
	tok, err := e.hub.Add("m1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := Config{
		Name: "m1", JamAddr: "jam.example.com:443", Capacity: 2, LogLevel: "info",
		TokenFile: filepath.Join(dir, "token"),
		Colima: ColimaConfig{
			RuntimeAddr: "jam.example.com:443", JamHost: "jam.example.com",
			IdentityFile:  filepath.Join(dir, "keys", "id_ed25519"),
			KnownHostsDir: filepath.Join(dir, "known_hosts.d"),
		},
	}
	if err := os.WriteFile(cfg.TokenFile, []byte(tok.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An existing key pair: EnsureFile reads it rather than running ssh-keygen.
	if err := os.MkdirAll(filepath.Dir(cfg.Colima.IdentityFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Colima.IdentityFile, []byte("priv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Colima.IdentityFile+".pub", []byte("ssh-ed25519 AAAA jam-launcher\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	if logs != nil {
		log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, cfg, "v1", log, Deps{
			Runner: &runner.Fake{},
			Dial:   e.dial,
			Ops:    ops,
			Sleep: func(ctx context.Context, d time.Duration) {
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Millisecond):
				}
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return cfg, tok.Token
}

func TestServeReportsRunningStudiosInHello(t *testing.T) {
	e := newJamEnv(t, nil)
	ops := &fakeBackend{containers: map[string]map[string]string{"cove-w1": studioOf("w1", "m1")}}
	var logs syncBuffer
	cfg, token := startServe(t, e, ops, &logs)
	eventually(t, "m1 connected", func() bool { return e.connected("m1") })
	eventually(t, "connected logged", func() bool { return strings.Contains(logs.String(), "manager: connected to Jam") })
	if strings.Contains(logs.String(), token) {
		t.Fatalf("token logged:\n%s", logs.String())
	}
	if fi, err := os.Stat(cfg.Colima.KnownHostsDir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("known-hosts dir: %v %v", fi, err)
	}
	invs := e.rec.inventories()
	if len(invs) != 1 {
		t.Fatalf("inventories = %+v, want one", invs)
	}
	if want := []jam.ManagedStudio{{ActorID: "w1", Location: "cove-w1"}}; !slices.Equal(invs[0].Studios, want) {
		t.Fatalf("studios = %+v, want %+v", invs[0].Studios, want)
	}
}

func TestServeReconnectsAfterJamRestart(t *testing.T) {
	e := newJamEnv(t, nil)
	ops := &fakeBackend{containers: map[string]map[string]string{"cove-w1": studioOf("w1", "m1")}}
	startServe(t, e, ops, nil)
	eventually(t, "m1 connected", func() bool { return e.connected("m1") })

	e.stop()
	eventually(t, "m1 disconnected", func() bool { return !e.connected("m1") })
	e.start()
	eventually(t, "m1 reconnected", func() bool { return e.connected("m1") })

	if n := len(e.rec.inventories()); n < 2 {
		t.Fatalf("hellos = %d, want a second after the restart", n)
	}
	ops.snapshot(func(f *fakeBackend) {
		if len(f.removed)+len(f.paused)+len(f.unpaused) != 0 {
			t.Fatalf("studios touched across the restart: removed %v paused %v unpaused %v", f.removed, f.paused, f.unpaused)
		}
	})
}

func TestServeSetsSSHUserCAFromWelcome(t *testing.T) {
	e := newJamEnv(t, []byte("ca-line"))
	ops := &fakeBackend{}
	startServe(t, e, ops, nil)
	eventually(t, "m1 connected", func() bool { return e.connected("m1") })

	placed, err := e.router.RaiseOn(context.Background(), jam.RaiseSpec{
		ActorID: "w9", Kit: testKit, Placement: jam.Placement{Managers: []string{"m1"}, Timeout: 2 * time.Second},
	}, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ops.snapshot(func(f *fakeBackend) {
		if got := string(f.caFor[placed.Location]); got != "ca-line" {
			t.Fatalf("CA installed in %q = %q, want ca-line (all: %v)", placed.Location, got, f.caFor)
		}
	})
}
