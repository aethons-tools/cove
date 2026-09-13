//go:build integration

package covemaster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/attach"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

// realListenerHarness raises one instance and starts an attach server on a real
// localhost TCP listener (not bufconn), so the client's dial actually exercises
// the "passthrough:///host:port" resolver/dialer path over a real socket. It
// mirrors client_test.go's serverHarness (store + guest role + Supervisor +
// aliveLauncher + Raise) but binds net.Listen instead of bufconn.Listen, since
// that's the one axis this test needs to differ on.
func realListenerHarness(t *testing.T) (store harbor.Store, addr, token, secret string) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutRole("default", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}})
	sup := harbor.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, tok, sec, err := sup.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	srv := attach.NewServer(store, sup, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sup.SetControlSink(srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return store, lis.Addr().String(), tok, sec
}

// TestIntegrationRealListener connects the client to a real TCP attach.Server
// and verifies activity flows end-to-end over a real grpc dial (not bufconn) —
// exercising the "passthrough:///"+addr resolver against an actual host:port.
func TestIntegrationRealListener(t *testing.T) {
	store, addr, tok, secret := realListenerHarness(t)
	c := New(Config{
		Addr: addr, Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, &blockWorkload{first: Waiting, controls: make(chan Control, 4)}) }()

	if !eventually(func() bool {
		inst, ok := store.GetInstance("w1")
		return ok && inst.Activity == harbor.ActivityWaiting
	}) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("activity never reached waiting: %+v", inst)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
