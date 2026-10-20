//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// TestStudioSSHEndToEnd drives studio collaboration through every real piece
// but the VM: an x/crypto/ssh client speaks through proxyOnce (at-jam
// ssh-proxy's body) → collabhttp's jam-tunnel upgrade → the TunnelBroker →
// the real attach server's Tunnel RPC → cove-master's dial-back → a real sshd
// on loopback that trusts the Jam CA. The cert comes from the cert route
// (principal "agent"); sshd maps it to the current user with an
// AuthorizedPrincipalsFile, since an unprivileged sshd can only log in as the
// user it runs as. Run: `just integration-jam`.
func TestStudioSSHEndToEnd(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	sshdPort := startCollabSSHD(t, ca.PublicKey())

	// Jam: a raised studio, the real attach server with the tunnel broker.
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, quiet)
	_, tok, secret, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	asrv := attach.NewServer(store, sup, quiet)
	sup.SetControlSink(asrv)
	tunnels := attach.NewTunnelBroker(asrv.PushOpenTunnel, attach.TunnelOptions{})
	asrv.SetTunnelBroker(tunnels)
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, asrv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	mux := http.NewServeMux()
	mux.Handle("/admin/studios/", collabhttp.Routes("/admin", collabhttp.Deps{
		Store: store, CA: ca, Tunnels: tunnels, Log: quiet,
		Principal: func(*http.Request) (collab.Principal, string, error) {
			return collab.Principal{Operator: "op1"}, "op1", nil
		},
	}))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	// The studio: cove-master attached over bufconn; its dial-back for the
	// studio's :2222 lands on the test sshd.
	dialed := make(chan string, 4)
	cm := covemaster.New(covemaster.Config{Addr: "bufnet", Token: tok, LaunchSecret: secret,
		DialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		},
		TunnelDial: func(ctx context.Context, addr string) (net.Conn, error) {
			dialed <- addr
			var d net.Dialer
			return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", sshdPort))
		},
	}, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- cm.Run(ctx, waitingWorkload{}) }()
	t.Cleanup(func() { cancel(); <-runDone })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if inst, ok := store.GetInstance("w1"); ok && inst.Activity == jam.ActivityWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cove-master never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The collaborator: a key, a cert from the cert route, a tunnel.
	_, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(userPriv)
	if err != nil {
		t.Fatal(err)
	}
	certLine, _, err := adminclient.New(hs.URL, "").IssueSSHCert("/admin", "w1", ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err != nil {
		t.Fatalf("issue cert: %v", err)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(certLine)
	if err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(pk.(*ssh.Certificate), signer)
	if err != nil {
		t.Fatal(err)
	}

	clientEnd, proxyEnd := net.Pipe()
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- proxyOnce(context.Background(), proxyOpts{BaseURL: hs.URL, Prefix: "/admin", Studio: "w1", In: proxyEnd, Out: proxyEnd})
		_ = proxyEnd.Close()
	}()

	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(clientEnd, "jam-w1", &ssh.ClientConfig{
		User:            u.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(certSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // throwaway sshd
		Timeout:         20 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh handshake through the tunnel: %v", err)
	}
	client := ssh.NewClient(sc, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	// Run returns only after the exit-status and the channel close arrive, so
	// a clean nil also proves the session's tail frames made it through.
	if err := sess.Run("echo ok"); err != nil {
		t.Fatalf("remote echo: %v (stderr %q)", err, stderr.String())
	}
	if got := stdout.String(); got != "ok\n" {
		t.Fatalf("remote stdout = %q, want %q", got, "ok\n")
	}
	if addr := <-dialed; addr != "127.0.0.1:2222" {
		t.Fatalf("cove-master dialed %q, want the studio's loopback sshd 127.0.0.1:2222", addr)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("closing the ssh client: %v", err)
	}
	select {
	case err := <-proxyDone:
		if err != nil {
			t.Fatalf("proxyOnce: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("proxyOnce did not return after the ssh client closed")
	}
}

type waitingWorkload struct{}

func (waitingWorkload) Run(ctx context.Context, h covemaster.Handle) error {
	h.Report(covemaster.Waiting)
	<-ctx.Done()
	return ctx.Err()
}
func (waitingWorkload) Control(covemaster.Control) {}

// startCollabSSHD runs an unprivileged sshd on a free loopback port that
// accepts only certs signed by caPub carrying principal "agent" (the studio's
// trust setup), and returns the port. It skips when sshd cannot run here.
func startCollabSSHD(t *testing.T, caPub []byte) int {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
	}
	if _, err := os.Stat(sshd); err != nil {
		t.Skipf("sshd not available: %v", err)
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skipf("ssh-keygen not available: %v", err)
	}
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "hostkey")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	caFile := write("ca.pub", string(caPub))
	principals := write("principals", sshca.Principal+"\n")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfg := write("sshd_config", fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
TrustedUserCAKeys %s
AuthorizedPrincipalsFile %s
AuthorizedKeysFile none
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
`, port, hostKey, filepath.Join(dir, "sshd.pid"), caFile, principals))

	var logBuf syncBuffer
	cmd := exec.Command(sshd, "-D", "-e", "-f", cfg)
	cmd.Stderr = &logBuf
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("sshd log:\n%s", logBuf.String())
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return port
		}
		if time.Now().After(deadline) {
			t.Skipf("sshd did not start listening: %v\n%s", err, logBuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuffer is a bytes.Buffer safe for sshd's stderr copier and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
