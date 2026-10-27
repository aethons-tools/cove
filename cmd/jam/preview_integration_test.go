//go:build integration

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/preview"
	"github.com/aethons-tools/cove/internal/studioagent"
)

// TestStudioPreviewEndToEnd drives a studio preview through every real piece
// but the VM and the IdP: Supervisor.Expose → the exposure fan-out → the
// attach server's SetTunnelPorts push → jam-studio-agent's dynamic allow-list; a
// caller's HTTPS request (HTTP/2 to Jam, with the admin listener's SNI cert
// pick and host router) → the preview proxy → the TunnelBroker → the attach
// Tunnel RPC → jam-studio-agent's dial-back → a real app on loopback. The caller is
// an operator bearer (Bearer stands in for OIDC verification). Unexpose must
// close an open WebSocket and turn the host 404; a port Jam never pushed is
// never dialed by jam-studio-agent. Run: `go test -tags integration ./cmd/jam/ -run Preview`.
func TestStudioPreviewEndToEnd(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	const domain = testPreviewDomain

	// The studio's app on loopback: GET / = "ok" (recording what it was
	// sent), /ws = a WebSocket-style echo after a 101.
	var appSaw atomic.Pointer[http.Header]
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				http.Error(w, "want websocket", http.StatusBadRequest)
				return
			}
			conn, brw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = brw.Flush()
			_, _ = io.Copy(conn, brw)
			return
		}
		h := r.Header.Clone()
		appSaw.Store(&h)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(app.Close)
	appPort := app.Listener.Addr().(*net.TCPAddr).Port

	// A second loopback listener the studio never exposes: nothing may reach it.
	stray, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stray.Close() })
	var strayAccepts atomic.Int32
	go func() {
		for {
			c, err := stray.Accept()
			if err != nil {
				return
			}
			strayAccepts.Add(1)
			_ = c.Close()
		}
	}()
	strayPort := stray.Addr().(*net.TCPAddr).Port

	// Jam: the supervisor, the attach server with the tunnel broker, the live
	// registry and the exposure fan-out, wired as serve wires them.
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, quiet)
	sup.SetPreviewsEnabled(true)
	asrv := attach.NewServer(store, sup, quiet)
	sup.SetControlSink(asrv)
	tunnels := attach.NewTunnelBroker(asrv.PushOpenTunnel, attach.TunnelOptions{DialBack: 2 * time.Second})
	asrv.SetTunnelBroker(tunnels)
	live := collabhttp.NewLive()
	var previewHandler atomic.Pointer[collabhttp.PreviewHandler]
	sup.SetExposureSink(newExposureFanout(asrv, live, func() {
		if h := previewHandler.Load(); h != nil {
			h.InvalidateLabels()
		}
	}))
	asrv.SetExposureSource(sup.ExposedPorts)
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, asrv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	actor := ident.New(ident.Session).String()
	_, tok, secret, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: actor, Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}

	// The studio: jam-studio-agent over bufconn; its dial-backs go to loopback
	// as asked, recorded.
	var dialMu sync.Mutex
	var dialed []string
	var cmLog syncBuffer
	cm := studioagent.New(studioagent.Config{Addr: "bufnet", Token: tok, LaunchSecret: secret,
		DialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		},
		TunnelDial: func(ctx context.Context, addr string) (net.Conn, error) {
			dialMu.Lock()
			dialed = append(dialed, addr)
			dialMu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}, slog.New(slog.NewTextHandler(&cmLog, nil)))
	dialsTo := func(port int) int {
		dialMu.Lock()
		defer dialMu.Unlock()
		n := 0
		for _, a := range dialed {
			if a == "127.0.0.1:"+strconv.Itoa(port) {
				n++
			}
		}
		return n
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- cm.Run(ctx, waitingWorkload{}) }()
	t.Cleanup(func() { cancel(); <-runDone })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if inst, ok := store.GetInstance(actor); ok && inst.Activity == jam.ActivityWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("jam-studio-agent never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The preview proxy behind the admin listener's TLS config and host router.
	auth := collabhttp.NewPreviewAuth(collabhttp.PreviewAuthOptions{
		Domain: domain, Port: "443", Log: quiet,
		Bearer: func(r *http.Request) (collab.Principal, string, error) {
			switch r.Header.Get("Authorization") {
			case "":
				return collab.Principal{}, "", collabhttp.ErrNoCredentials
			case "Bearer op-token":
				return collab.Principal{Operator: "op1"}, "op1", nil
			}
			return collab.Principal{}, "", collabhttp.ErrInvalidToken
		},
	})
	h := collabhttp.Preview(collabhttp.Deps{
		Store: store, Tunnels: tunnels, Log: quiet, Live: live,
		Resume: sup.Resume, Connected: asrv.Connected,
		Exposures: sup.Exposures,
		PreviewURL: func(actorID, name string) (string, error) {
			host, err := preview.Host(domain, name, actorID)
			return "https://" + host + "/", err
		},
	}, auth, domain)
	previewHandler.Store(h)
	adminCert, previewCert := selfSigned(t, "jam.example"), selfSigned(t, "*."+domain)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:   hostRouter(domain, h, http.NotFoundHandler()),
		TLSConfig: adminTLSConfig(adminCert, previewCert, domain),
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })

	roots := x509.NewCertPool()
	roots.AddCert(previewCert.Leaf)
	toJam := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
	client := &http.Client{
		Transport: &http.Transport{DialContext: toJam, TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true},
		// The login bounce is asserted, not followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       20 * time.Second,
	}
	t.Cleanup(client.CloseIdleConnections)
	appHost, err := preview.Host(domain, "app", actor)
	if err != nil {
		t.Fatal(err)
	}
	if want := "app--" + strings.TrimPrefix(actor, "ses_") + "." + domain; appHost != strings.ToLower(want) {
		t.Fatalf("preview host %q, want %q", appHost, want)
	}
	get := func(hdr http.Header) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "https://"+appHost+"/", nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", appHost, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	bearer := http.Header{"Authorization": {"Bearer op-token"}}

	// Not exposed yet: 404, and nothing dialed.
	if resp, _ := get(bearer); resp.StatusCode != http.StatusNotFound || dialsTo(appPort) != 0 {
		t.Fatalf("before expose: %d (%d dial(s)), want 404 and none", resp.StatusCode, dialsTo(appPort))
	}

	if _, err := sup.Expose(actor, "app", appPort); err != nil {
		t.Fatal(err)
	}

	// A browser without a session is bounced to the login host.
	resp, _ := get(http.Header{"Accept": {"text/html"}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "https://"+preview.LoginHost(domain)+"/__jam/start?return_to=") {
		t.Fatalf("no-credentials browser: %d Location %q, want 302 to the login host", resp.StatusCode, loc)
	}

	// The operator bearer reaches the app over HTTP/2 to Jam; the app sees
	// the forwarding headers and the caller's name, never the bearer.
	resp, body := get(bearer)
	if resp.StatusCode != http.StatusOK || body != "ok" {
		t.Fatalf("GET through the preview: %d %q", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 2 {
		t.Fatalf("caller spoke %s to Jam, want HTTP/2", resp.Proto)
	}
	saw := appSaw.Load()
	if saw == nil {
		t.Fatal("the app saw no request")
	}
	if got := saw.Get("Authorization"); got != "" {
		t.Fatalf("the app received Authorization %q", got)
	}
	if saw.Get("X-Jam-User") != "op1" || saw.Get("X-Forwarded-Host") != appHost || saw.Get("X-Forwarded-Proto") != "https" {
		t.Fatalf("app headers: X-Jam-User %q X-Forwarded-Host %q X-Forwarded-Proto %q", saw.Get("X-Jam-User"), saw.Get("X-Forwarded-Host"), saw.Get("X-Forwarded-Proto"))
	}
	if n := dialsTo(appPort); n == 0 {
		t.Fatal("jam-studio-agent never dialed the app port")
	}

	// A port outside the pushed set: Jam asks, jam-studio-agent logs the refusal
	// and dials nothing.
	expectRefused(t, tunnels, actor, strayPort, &cmLog)

	// A WebSocket round trip through the proxy (HTTP/1.1 Upgrade).
	ws, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: appHost, RootCAs: roots, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	_ = ws.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(ws, "GET /ws HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer op-token\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", appHost)
	br := bufio.NewReader(ws)
	up, err := http.ReadResponse(br, nil)
	if err != nil || up.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade through the preview: %v %v", up, err)
	}
	fmt.Fprint(ws, "frame-1\n")
	if line, err := br.ReadString('\n'); err != nil || line != "frame-1\n" {
		t.Fatalf("echo through the preview: %q %v", line, err)
	}

	// Unexpose: the open socket closes at once, the host turns 404, and the
	// port leaves jam-studio-agent's allow-list (a new tunnel is refused undialed).
	dialsBefore := dialsTo(appPort)
	if err := sup.Unexpose(actor, "app"); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("WebSocket read after unexpose: %v, want EOF", err)
	}
	if resp, _ := get(bearer); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after unexpose: %d, want 404", resp.StatusCode)
	}
	expectRefused(t, tunnels, actor, appPort, &cmLog)
	if n := dialsTo(appPort); n != dialsBefore {
		t.Fatalf("jam-studio-agent dialed the app port %d more time(s) after unexpose", n-dialsBefore)
	}
	if n := dialsTo(strayPort); n != 0 || strayAccepts.Load() != 0 {
		t.Fatalf("the unexposed port was dialed %d time(s), accepted %d", n, strayAccepts.Load())
	}
}

// expectRefused asks the broker for a tunnel to port and waits for
// jam-studio-agent's own "tunnel refused" log line for that port — the direct proof
// it declined without dialing — then abandons the open rather than waiting out
// the dial-back deadline. The open must not succeed.
func expectRefused(t *testing.T, tunnels *attach.TunnelBroker, actor string, port int, cmLog *syncBuffer) {
	t.Helper()
	refusals := func() int {
		n := 0
		for line := range strings.SplitSeq(cmLog.String(), "\n") {
			if strings.Contains(line, `msg="tunnel refused: port not allowed"`) && slices.Contains(strings.Fields(line), "port="+strconv.Itoa(port)) {
				n++
			}
		}
		return n
	}
	before := refusals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		c   net.Conn
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := tunnels.Open(ctx, actor, int32(port))
		done <- result{c, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for refusals() == before {
		select {
		case r := <-done:
			if r.err == nil {
				_ = r.c.Close()
				t.Fatalf("a tunnel to unexposed port %d opened", port)
			}
			t.Fatalf("open of port %d failed (%v) before jam-studio-agent logged a refusal; log:\n%s", port, r.err, cmLog.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("jam-studio-agent logged no refusal for port %d; log:\n%s", port, cmLog.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if r := <-done; r.err == nil {
		_ = r.c.Close()
		t.Fatalf("a tunnel to unexposed port %d opened", port)
	}
}
