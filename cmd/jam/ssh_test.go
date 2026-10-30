package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// sshTestHome points HOME and the config dir at a temp dir and clears any
// operator token in the environment.
func sshTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("JAM_ADMIN_TOKEN", "")
	// The default admin URL is a closed port, so no test reaches a real Jam.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if err := saveSettings(defaultApp, clientSettings{AdminURL: "http://" + l.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	return home
}

// fakeClock pins sshNow for the test.
func fakeClock(t *testing.T, now time.Time) {
	t.Helper()
	old := sshNow
	sshNow = func() time.Time { return now }
	t.Cleanup(func() { sshNow = old })
}

// collabJam is a fake Jam serving the collaboration routes on both planes: it
// certifies keys (8 h from sshNow) and echoes tunnel bytes after the 101.
type collabJam struct {
	*httptest.Server
	mu         sync.Mutex
	certCalls  int
	paths      []string
	tokens     []string
	certStatus int // non-zero: answer cert requests with this status
	sshStatus  int // non-zero: answer tunnel requests with this status
	body       string
}

// snapshot reads the recorded requests under the lock.
func (j *collabJam) snapshot() (certCalls int, paths, tokens []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.certCalls, append([]string(nil), j.paths...), append([]string(nil), j.tokens...)
}

func newCollabJam(t *testing.T) *collabJam {
	t.Helper()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	j := &collabJam{}
	record := func(r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.paths = append(j.paths, r.Method+" "+r.URL.Path)
		j.tokens = append(j.tokens, r.Header.Get("Authorization"))
	}
	mux := http.NewServeMux()
	for _, prefix := range []string{"/admin", "/me"} {
		mux.HandleFunc("POST "+prefix+"/studios/{id}/ssh/cert", func(w http.ResponseWriter, r *http.Request) {
			record(r)
			j.mu.Lock()
			j.certCalls++
			status, body := j.certStatus, j.body
			j.mu.Unlock()
			if status != 0 {
				http.Error(w, body, status)
				return
			}
			var in struct {
				PublicKey string `json:"public_key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(in.PublicKey))
			if err != nil {
				http.Error(w, "bad key", 400)
				return
			}
			exp := sshNow().Add(8 * time.Hour)
			c := &ssh.Certificate{Key: pub, CertType: ssh.UserCert, ValidPrincipals: []string{"agent"},
				ValidAfter: uint64(sshNow().Add(-time.Minute).Unix()), ValidBefore: uint64(exp.Unix())}
			if err := c.SignCert(rand.Reader, ca); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"certificate": string(ssh.MarshalAuthorizedKey(c)), "expires_at": exp.UTC().Format(time.RFC3339)})
		})
		mux.HandleFunc("GET "+prefix+"/studios/{id}/ssh", func(w http.ResponseWriter, r *http.Request) {
			record(r)
			j.mu.Lock()
			status, body := j.sshStatus, j.body
			j.mu.Unlock()
			if status != 0 {
				http.Error(w, body, status)
				return
			}
			upgradeEcho(t, w, r)
		})
	}
	j.Server = httptest.NewServer(mux)
	t.Cleanup(j.Close)
	return j
}

// upgradeEcho answers the jam-tunnel upgrade with 101, then echoes every byte
// until the client half-closes.
func upgradeEcho(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "jam-tunnel" || !strings.EqualFold(r.Header.Get("Connection"), "upgrade") {
		http.Error(w, "expected upgrade", http.StatusUpgradeRequired)
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	defer conn.Close()
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n")
	_ = brw.Flush()
	_, _ = io.Copy(conn, brw)
}

func upgradeEchoServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/studios/ses_a/ssh" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "wrong request", 400)
			return
		}
		upgradeEcho(t, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSSHConfigBlock(t *testing.T) {
	home := sshTestHome(t)
	stubSSHSelf(t, "/opt/jam/dist/linux-amd64/jam", nil)
	var out, errb bytes.Buffer
	if code := run([]string{"ssh-config", "ses_a"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"Host jam-ses_a", "ProxyCommand /opt/jam/dist/linux-amd64/jam ssh-proxy ", "User agent", "IdentityFile ", "CertificateFile ", "StrictHostKeyChecking accept-new", "PreferredAuthentications publickey,gssapi-with-mic"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "ssh-proxy --operator ses_a\n") {
		t.Errorf("ProxyCommand should name the operator plane and the studio:\n%s", out.String())
	}
	if fi, err := os.Stat(filepath.Join(home, ".config", "jam", "ssh", "id_ed25519")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key not created 0600: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); err == nil {
		t.Fatal("ssh-config without --write must not touch ~/.ssh/config")
	}
}

// With a cached participant login, ssh-config defaults to the participant
// plane, pins it in the ProxyCommand, and fetches the cert with the ID token.
func TestSSHConfigParticipantFetchesCert(t *testing.T) {
	home := sshTestHome(t)
	fakeClock(t, time.Now())
	j := newCollabJam(t)
	if err := saveTokenAt(participantTokenPath(defaultApp), cachedToken{IDToken: "idtok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"ssh-config", "--admin-url", j.URL, "ses_a"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ssh-proxy --admin-url '"+j.URL+"' --participant ses_a\n") {
		t.Errorf("ProxyCommand should pin the admin URL and participant plane:\n%s", out.String())
	}
	if calls, paths, tokens := j.snapshot(); calls != 1 || paths[0] != "POST /me/studios/ses_a/ssh/cert" || tokens[0] != "Bearer idtok" {
		t.Fatalf("cert calls=%d paths=%v", calls, paths)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "jam", "ssh", "ses_a-cert.pub")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+errb.String(), "idtok") {
		t.Fatal("token printed")
	}
}

// --write installs the block in ~/.ssh/config once, between markers, keeping
// the rest of the file; a rerun replaces it in place.
func TestSSHConfigWriteIsIdempotent(t *testing.T) {
	home := sshTestHome(t)
	cfg := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("Host other\n  User bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		var out, errb bytes.Buffer
		if code := run([]string{"ssh-config", "--write", "ses_a"}, func(string) string { return "" }, &out, &errb); code != 0 {
			t.Fatalf("run %d: exit %d: %s", i, code, errb.String())
		}
	}
	data, _ := os.ReadFile(cfg)
	s := string(data)
	if !strings.HasSuffix(s, "\nHost other\n  User bob\n") || strings.Count(s, "Host jam-ses_a") != 1 || strings.Count(s, "# >>> jam ses_a >>>") != 1 {
		t.Fatalf("config:\n%s", s)
	}
	// A different studio gets its own block.
	if code := run([]string{"ssh-config", "--write", "ses_b"}, func(string) string { return "" }, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	data, _ = os.ReadFile(cfg)
	if strings.Count(string(data), "Host jam-ses_a") != 1 || strings.Count(string(data), "Host jam-ses_b") != 1 {
		t.Fatalf("config:\n%s", data)
	}
}

func TestSSHConfigRejectsUnsafeStudio(t *testing.T) {
	sshTestHome(t)
	var errb bytes.Buffer
	if code := run([]string{"ssh-config", "ses;rm -rf"}, func(string) string { return "" }, io.Discard, &errb); code != 2 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSSHProxyPipesThroughUpgrade(t *testing.T) {
	srv := upgradeEchoServer(t) // 101 then echoes bytes
	stdin := strings.NewReader("hello\n")
	var stdout bytes.Buffer
	err := proxyOnce(context.Background(), proxyOpts{BaseURL: srv.URL, Prefix: "/me", Studio: "ses_a", Token: "tok", In: stdin, Out: &stdout})
	if err != nil || stdout.String() != "hello\n" {
		t.Fatalf("err=%v out=%q", err, stdout.String())
	}
}

// Over TLS the proxy speaks HTTP/1.1 even when the server offers h2, which
// could not be upgraded.
func TestSSHProxyTLSForcesHTTP1(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			http.Error(w, "not http/1.1", 400)
			return
		}
		upgradeEcho(t, w, r)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	var stdout bytes.Buffer
	err := proxyOnce(context.Background(), proxyOpts{BaseURL: srv.URL, Prefix: "/admin", Studio: "ses_a", In: strings.NewReader("over tls"), Out: &stdout, TLS: &tls.Config{RootCAs: roots}})
	if err != nil || stdout.String() != "over tls" {
		t.Fatalf("err=%v out=%q", err, stdout.String())
	}
}

// Bytes the server sends right behind the 101 reach stdout too.
func TestSSHProxyKeepsBytesBufferedWithThe101(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, _ := http.NewResponseController(w).Hijack()
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\nSSH-2.0-banner\r\n")
		_ = brw.Flush()
		_, _ = io.Copy(io.Discard, brw)
	}))
	t.Cleanup(srv.Close)
	var stdout bytes.Buffer
	if err := proxyOnce(context.Background(), proxyOpts{BaseURL: srv.URL, Prefix: "/admin", Studio: "ses_a", In: strings.NewReader(""), Out: &stdout}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "SSH-2.0-banner\r\n" {
		t.Fatalf("out=%q", stdout.String())
	}
}

// writeCertExpiring leaves a cert for the local key in the cert file that
// expires at exp (signed by a throwaway CA; only its validity matters here).
func writeCertExpiring(t *testing.T, studio string, exp time.Time) []byte {
	t.Helper()
	pub, err := ensureSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, _ := ssh.NewSignerFromKey(caPriv)
	c := &ssh.Certificate{Key: pub, CertType: ssh.UserCert, ValidPrincipals: []string{"agent"}, ValidBefore: uint64(exp.Unix())}
	if err := c.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	data := ssh.MarshalAuthorizedKey(c)
	if err := os.WriteFile(sshCertPath(defaultApp, studio), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// ssh-proxy renews the cert when under an hour remains and leaves it alone
// otherwise, then pipes the tunnel.
func TestSSHProxyRenewsCertNearExpiry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  time.Duration
		renew bool
	}{
		{"30m left renews", 30 * time.Minute, true},
		{"2h left keeps", 2 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sshTestHome(t)
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			fakeClock(t, now)
			j := newCollabJam(t)
			old := writeCertExpiring(t, "ses_a", now.Add(tc.left))
			oldIn := sshStdin
			sshStdin = strings.NewReader("ping")
			t.Cleanup(func() { sshStdin = oldIn })
			var out, errb bytes.Buffer
			if code := run([]string{"ssh-proxy", "--admin-url", j.URL, "--operator", "ses_a"}, func(string) string { return "" }, &out, &errb); code != 0 {
				t.Fatalf("exit %d: %s", code, errb.String())
			}
			if out.String() != "ping" {
				t.Fatalf("stdout=%q", out.String())
			}
			got, _ := os.ReadFile(sshCertPath(defaultApp, "ses_a"))
			calls, _, _ := j.snapshot()
			if tc.renew {
				if calls != 1 || bytes.Equal(got, old) {
					t.Fatalf("expected a renewal: calls=%d", calls)
				}
				if until, ok := certValidUntil(got, mustKey(t)); !ok || !until.Equal(now.Add(8*time.Hour)) {
					t.Fatalf("renewed cert until %v ok=%v", until, ok)
				}
			} else if calls != 0 || !bytes.Equal(got, old) {
				t.Fatalf("expected no renewal: calls=%d", calls)
			}
		})
	}
}

func mustKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	k, err := ensureSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Server refusals become one friendly line on stderr and exit 1.
func TestSSHProxyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		participnt bool
		certStatus int
		sshStatus  int
		body       string
		want       string
	}{
		{"403 cert", []string{"--operator"}, false, 403, 0, "you may not use ssh on this studio", "you don't have SSH access to this studio (ask an operator: jam studio share …)"},
		{"403 tunnel", []string{"--operator"}, false, 0, 403, "you may not use ssh on this studio", "you don't have SSH access to this studio (ask an operator: jam studio share …)"},
		{"401 participant", []string{"--participant"}, true, 401, 0, "invalid or expired token", "run: jam login --participant"},
		{"409", []string{"--operator"}, false, 0, 409, "studio is not running", "studio ses_a is not running"},
		{"503", []string{"--operator"}, false, 0, 503, "studio not connected", "studio ses_a is not connected to Jam"},
		{"504", []string{"--operator"}, false, 0, 504, "timeout", "studio ses_a did not open the tunnel in time"},
		{"429", []string{"--operator"}, false, 0, 429, "too many", "too many SSH tunnels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sshTestHome(t)
			fakeClock(t, time.Now())
			j := newCollabJam(t)
			j.certStatus, j.sshStatus, j.body = tc.certStatus, tc.sshStatus, tc.body
			if tc.participnt {
				if err := saveTokenAt(participantTokenPath(defaultApp), cachedToken{IDToken: "idtok", Expiry: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			oldIn := sshStdin
			sshStdin = strings.NewReader("")
			t.Cleanup(func() { sshStdin = oldIn })
			args := append([]string{"ssh-proxy", "--admin-url", j.URL}, tc.args...)
			var out, errb bytes.Buffer
			code := run(append(args, "ses_a"), func(string) string { return "" }, &out, &errb)
			if code != 1 || !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("exit %d, stderr %q, want %q", code, errb.String(), tc.want)
			}
			if out.Len() != 0 || strings.Contains(errb.String(), "idtok") {
				t.Fatalf("stdout=%q stderr=%q", out.String(), errb.String())
			}
		})
	}
}

// jam ssh renews the cert before exec'ing ssh with the Host options and the
// pass-through args, and never puts a token on argv.
func TestSSHExecsSSHAfterFreshCert(t *testing.T) {
	sshTestHome(t)
	stubSSHSelf(t, "/usr/local/bin/jam", nil)
	fakeClock(t, time.Now())
	j := newCollabJam(t)
	t.Setenv("JAM_ADMIN_TOKEN", "optok")
	var gotPath string
	var gotArgv []string
	oldExec, oldLook := sshExec, sshLookPath
	sshExec = func(path string, argv []string, _ []string) error { gotPath, gotArgv = path, argv; return nil }
	sshLookPath = func(string) (string, error) { return "/usr/bin/ssh", nil }
	t.Cleanup(func() { sshExec, sshLookPath = oldExec, oldLook })

	var errb bytes.Buffer
	if code := run([]string{"ssh", "--admin-url", j.URL, "ses_a", "-L", "3000:localhost:3000", "ls"}, func(string) string { return "" }, io.Discard, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if calls, _, tokens := j.snapshot(); calls != 1 || tokens[0] != "Bearer optok" {
		t.Fatalf("cert calls=%d tokens=%v", calls, tokens)
	}
	if gotPath != "/usr/bin/ssh" || gotArgv[0] != "ssh" {
		t.Fatalf("exec %s %v", gotPath, gotArgv)
	}
	joined := strings.Join(gotArgv, "\x00")
	for _, want := range []string{"User=agent", "ProxyCommand=/usr/local/bin/jam ssh-proxy --admin-url '" + j.URL + "' --operator ses_a", "CertificateFile=", "IdentityFile=", "StrictHostKeyChecking=accept-new", "jam-ses_a\x00-L\x003000:localhost:3000\x00ls"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %q", want, gotArgv)
		}
	}
	if strings.Contains(joined, "optok") {
		t.Fatal("token on argv")
	}
}

func TestSSHFailsBeforeExecOnRefusal(t *testing.T) {
	sshTestHome(t)
	fakeClock(t, time.Now())
	j := newCollabJam(t)
	j.certStatus, j.body = 403, "you may not use ssh on this studio"
	oldExec := sshExec
	sshExec = func(string, []string, []string) error { t.Fatal("exec'd despite refusal"); return nil }
	t.Cleanup(func() { sshExec = oldExec })
	var errb bytes.Buffer
	if code := run([]string{"ssh", "--admin-url", j.URL, "ses_a"}, func(string) string { return "" }, io.Discard, &errb); code != 1 || !strings.Contains(errb.String(), "you don't have SSH access") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSSHPlaneFlagsExclusive(t *testing.T) {
	sshTestHome(t)
	var errb bytes.Buffer
	if code := run([]string{"ssh-config", "--participant", "--operator", "ses_a"}, func(string) string { return "" }, io.Discard, &errb); code != 2 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// echoTunnels opens a tunnel whose studio end echoes what it reads.
type echoTunnels struct{}

func (e echoTunnels) OpenPreview(ctx context.Context, a string, p int32) (net.Conn, error) {
	return e.Open(ctx, a, p)
}

func (echoTunnels) Open(context.Context, string, int32) (net.Conn, error) {
	human, studio := net.Pipe()
	go func() { defer studio.Close(); _, _ = io.Copy(studio, studio) }()
	return human, nil
}

// End to end against Jam's real collaboration routes: the cert the CLI gets
// is for its key, and the tunnel carries bytes both ways.
func TestSSHAgainstCollabRoutes(t *testing.T) {
	sshTestHome(t)
	st := jam.NewMemStore()
	if err := st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "default", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(collabhttp.Routes("/admin", collabhttp.Deps{
		Store: st, CA: ca, Tunnels: echoTunnels{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Principal: func(*http.Request) (collab.Principal, string, error) {
			return collab.Principal{Operator: "local"}, "op", nil
		},
	}))
	t.Cleanup(srv.Close)

	key := mustKey(t)
	tgt := sshTarget{app: defaultApp, adminURL: srv.URL, studio: "ses_a"}
	if _, err := ensureCert(tgt, key); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(sshCertPath(defaultApp, "ses_a"))
	if until, ok := certValidUntil(data, key); !ok || time.Until(until) < 7*time.Hour {
		t.Fatalf("cert until %v ok=%v", until, ok)
	}

	// The real server ends the tunnel when the client half-closes, so keep
	// stdin open until the echo is back.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- proxyOnce(context.Background(), proxyOpts{BaseURL: srv.URL, Prefix: "/admin", Studio: "ses_a", In: inR, Out: outW})
		_ = outW.Close()
	}()
	_, _ = inW.Write([]byte("SSH-2.0-test\r\n"))
	buf := make([]byte, len("SSH-2.0-test\r\n"))
	if _, err := io.ReadFull(outR, buf); err != nil || string(buf) != "SSH-2.0-test\r\n" {
		t.Fatalf("echo %q err=%v", buf, err)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// --write puts a new block above an existing `Host *` (OpenSSH takes the
// first value), after leading comments; a rerun keeps the block's position;
// symlinked and CRLF configs keep their link and line endings.
func TestSSHConfigWriteGoesOnTop(t *testing.T) {
	home := sshTestHome(t)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "dotfiles-ssh-config")
	if err := os.WriteFile(target, []byte("# mine\r\nHost *\r\n  User bob\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var errb bytes.Buffer
		if code := run([]string{"ssh-config", "--write", "ses_a"}, func(string) string { return "" }, io.Discard, &errb); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, "config")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", fi, err)
	}
	data, _ := os.ReadFile(target)
	s := string(data)
	if !strings.HasPrefix(s, "# mine\r\n# >>> jam ses_a >>>\r\nHost jam-ses_a\r\n") || !strings.HasSuffix(s, "# <<< jam ses_a <<<\r\n\r\nHost *\r\n  User bob\r\n") {
		t.Fatalf("config:\n%q", s)
	}
	if strings.Count(s, "Host jam-ses_a") != 1 || strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Fatalf("duplicate block or mixed line endings:\n%q", s)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// A begin marker without its end marker is refused rather than duplicated.
func TestSSHConfigWriteRefusesBrokenMarkers(t *testing.T) {
	home := sshTestHome(t)
	cfg := filepath.Join(home, ".ssh", "config")
	_ = os.MkdirAll(filepath.Dir(cfg), 0o700)
	orig := "# >>> jam ses_a >>>\nHost jam-ses_a\n  User agent\n"
	_ = os.WriteFile(cfg, []byte(orig), 0o600)
	var errb bytes.Buffer
	if code := run([]string{"ssh-config", "--write", "ses_a"}, func(string) string { return "" }, io.Discard, &errb); code != 1 || !strings.Contains(errb.String(), "fix the file by hand") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if data, _ := os.ReadFile(cfg); string(data) != orig {
		t.Fatalf("config changed:\n%s", data)
	}
}

// Concurrent first runs agree on one whole key.
func TestEnsureSSHKeyConcurrent(t *testing.T) {
	sshTestHome(t)
	const n = 8
	keys := make([]ssh.PublicKey, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { keys[i], errs[i] = ensureSSHKey() })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || !bytes.Equal(keys[i].Marshal(), keys[0].Marshal()) {
			t.Fatalf("run %d: err=%v", i, errs[i])
		}
	}
	if left, _ := filepath.Glob(filepath.Join(sshDir(), ".tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// An existing key readable by others is made private.
func TestEnsureSSHKeyTightensMode(t *testing.T) {
	sshTestHome(t)
	mustKey(t)
	if err := os.Chmod(sshKeyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	mustKey(t)
	if fi, _ := os.Stat(sshKeyPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// A non-default app's certs live apart from the default app's.
func TestSSHCertPathPerApp(t *testing.T) {
	sshTestHome(t)
	if a, b := sshCertPath(defaultApp, "ses_a"), sshCertPath("dev", "ses_a"); a == b || filepath.Dir(b) != filepath.Join(sshDir(), "dev") {
		t.Fatalf("default %s, dev %s", a, b)
	}
}

func stubSSHSelf(t *testing.T, path string, err error) {
	t.Helper()
	old := sshSelf
	sshSelf = func() (string, error) { return path, err }
	t.Cleanup(func() { sshSelf = old })
}

// The ProxyCommand runs in ssh's shell, which may not have jam on PATH
// (a dist/ build, a dev checkout), so it names this binary by absolute path.
func TestProxyCommandNamesThisBinary(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		err        error
		want       string
	}{
		{"absolute", "/home/me/cove/dist/darwin-arm64/jam", nil, "/home/me/cove/dist/darwin-arm64/jam ssh-proxy --operator ses_a"},
		{"needs quoting", "/Users/me/My Tools/jam", nil, "'/Users/me/My Tools/jam' ssh-proxy --operator ses_a"},
		{"percent escaped", "/opt/100%/jam", nil, "'/opt/100%%/jam' ssh-proxy --operator ses_a"},
		{"unknown", "", errors.New("no /proc"), "jam ssh-proxy --operator ses_a"},
		{"relative", "jam", nil, "jam ssh-proxy --operator ses_a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSSHSelf(t, tc.path, tc.err)
			got := sshTarget{app: defaultApp, studio: "ses_a"}.proxyCommand()
			if got != tc.want {
				t.Errorf("proxyCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// A 404 for "jam-<x>" hints that the ssh host alias was used as the studio id.
func TestSSHNotFoundHintsAtHostAlias(t *testing.T) {
	nf := &adminclient.StatusError{Code: http.StatusNotFound}
	got := sshErrMessage(nf, sshTarget{app: defaultApp, studio: "jam-ses_a"})
	if !strings.Contains(got, "did you mean studio ses_a?") {
		t.Errorf("no alias hint: %q", got)
	}
	if got := sshErrMessage(nf, sshTarget{app: defaultApp, studio: "ses_a"}); strings.Contains(got, "did you mean") {
		t.Errorf("hint for a plain id: %q", got)
	}
}

// A second studio's block goes before the first one, not inside it: the first
// block opens with a comment (its marker), which the leading-comment skip must
// not walk past.
func TestSpliceSSHConfigBlockBeforeAnotherStudiosBlock(t *testing.T) {
	a := sshTarget{studio: "ses_a"}.sshConfigBlock()
	b := sshTarget{studio: "ses_b"}.sshConfigBlock()
	cfg := "# my config\n" + a + "\nHost *\n  ServerAliveInterval 30\n"
	got, err := spliceSSHConfigBlock(cfg, "ses_b", b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# my config\n" + b + "\n" + a + "\nHost *\n  ServerAliveInterval 30\n"; got != want {
		t.Fatalf("spliced config =\n%s\nwant\n%s", got, want)
	}
	again, err := spliceSSHConfigBlock(got, "ses_a", a) // rerun for ses_a: replaced in place
	if err != nil || again != got {
		t.Fatalf("rerun changed the file (%v):\n%s", err, again)
	}
}

// --prune asks each block's own Jam (per its ProxyCommand) about its studio and
// removes only those Jam definitely no longer has — with their cert and pinned
// host key. A studio it cannot ask about is kept.
func TestSSHConfigPruneRemovesGoneStudios(t *testing.T) {
	home := sshTestHome(t)
	st := jam.NewMemStore()
	if err := st.PutInstance(jam.Instance{ActorID: "ses_live", Project: "default", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(collabhttp.Routes("/admin", collabhttp.Deps{
		Store: st, CA: ca, Tunnels: echoTunnels{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Principal: func(*http.Request) (collab.Principal, string, error) {
			return collab.Principal{Operator: "local"}, "op", nil
		},
	}))
	t.Cleanup(srv.Close)
	off := httptest.NewServer(http.NotFoundHandler()) // a Jam with ssh: off — every route 404s
	t.Cleanup(off.Close)

	block := func(studio, adminURL string) string {
		return sshTarget{app: defaultApp, adminURLFlag: adminURL, studio: studio}.sshConfigBlock()
	}
	cfg := "# mine\n" + block("ses_gone", srv.URL) + "\n" + block("ses_live", srv.URL) + "\n" +
		block("ses_unknown", off.URL) + "\nHost *\n  ServerAliveInterval 30\n"
	path := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ses_gone", "ses_live"} {
		if err := os.MkdirAll(filepath.Dir(sshCertPath(defaultApp, s)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sshCertPath(defaultApp, s), []byte("cert"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var forgot []string
	old := sshForgetHost
	sshForgetHost = func(alias string) error { forgot = append(forgot, alias); return nil }
	t.Cleanup(func() { sshForgetHost = old })

	// --dry-run reports and changes nothing.
	var out, errb bytes.Buffer
	if code := cmdSSHConfig([]string{"--prune", "--dry-run"}, cli.Globals{}, &out, &errb); code != 0 {
		t.Fatalf("dry run exit %d: %s", code, errb.String())
	}
	if got, _ := os.ReadFile(path); string(got) != cfg || len(forgot) != 0 || !strings.Contains(out.String(), "would remove jam-ses_gone") {
		t.Fatalf("dry run changed things or did not report:\n%s\nforgot=%v out=%s", got, forgot, out.String())
	}

	out.Reset()
	errb.Reset()
	if code := cmdSSHConfig([]string{"--prune"}, cli.Globals{}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "# mine\n" + block("ses_live", srv.URL) + "\n" + block("ses_unknown", off.URL) + "\nHost *\n  ServerAliveInterval 30\n"
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("pruned config =\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(sshCertPath(defaultApp, "ses_gone")); !os.IsNotExist(err) {
		t.Errorf("gone studio's cert kept: %v", err)
	}
	if _, err := os.Stat(sshCertPath(defaultApp, "ses_live")); err != nil {
		t.Errorf("live studio's cert removed: %v", err)
	}
	if len(forgot) != 1 || forgot[0] != "jam-ses_gone" {
		t.Errorf("forgot host keys %v, want [jam-ses_gone]", forgot)
	}
	if !strings.Contains(out.String(), "removed jam-ses_gone") || !strings.Contains(errb.String(), "ses_unknown") {
		t.Errorf("stdout=%q stderr=%q", out.String(), errb.String())
	}
}

func TestSSHConfigPruneTakesNoStudio(t *testing.T) {
	sshTestHome(t)
	var out, errb bytes.Buffer
	if code := cmdSSHConfig([]string{"--prune", "ses_a"}, cli.Globals{}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// shellWords reads back what proxyCommand writes, quoting included.
func TestShellWordsReadsProxyCommand(t *testing.T) {
	in := []string{"/opt/my jam/jam", "ssh-proxy", "--admin-url", "https://j/x'y", "--operator", "ses_a"}
	line := shellQuote(in[0]) + " " + in[1] + " " + in[2] + " " + shellQuote(in[3]) + " " + in[4] + " " + in[5]
	got, err := shellWords(line)
	if err != nil || strings.Join(got, "|") != strings.Join(in, "|") {
		t.Fatalf("shellWords(%q) = %q, %v", line, got, err)
	}
	if _, err := shellWords("a 'b"); err == nil {
		t.Fatal("unterminated quote accepted")
	}
}
