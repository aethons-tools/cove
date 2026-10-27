package sshca

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestIssueCertTrustedBySSH(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	pemBlock, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := Parse(pem.EncodeToMemory(pemBlock))
	if err != nil {
		t.Fatal(err)
	}
	userPub, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	sshUser, _ := ssh.NewPublicKey(userPub)
	now := time.Unix(1_000_000, 0)
	certBytes, exp, err := ca.Issue(ssh.MarshalAuthorizedKey(sshUser), "user=usr_1 studio=ses_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(Validity)) {
		t.Fatalf("expiry %v", exp)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		t.Fatal(err)
	}
	cert := pk.(*ssh.Certificate)
	if cert.KeyId != "user=usr_1 studio=ses_a" || !slices.Equal(cert.ValidPrincipals, []string{"agent"}) || cert.CertType != ssh.UserCert {
		t.Fatalf("cert = %+v", cert)
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey(ca.PublicKey())
	checker := ssh.CertChecker{SupportedCriticalOptions: []string{"source-address"}, IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), caPub.Marshal()) }, Clock: func() time.Time { return now.Add(time.Hour) }}
	if err := checker.CheckCert("agent", cert); err != nil {
		t.Fatalf("cert not accepted by an ssh checker: %v", err)
	}
	checker.Clock = func() time.Time { return now.Add(Validity + time.Minute) }
	if checker.CheckCert("agent", cert) == nil {
		t.Fatal("expired cert accepted")
	}
	if _, ok := cert.Permissions.Extensions["permit-port-forwarding"]; !ok {
		t.Fatal("port forwarding not permitted")
	}
	if _, ok := cert.Permissions.Extensions["permit-agent-forwarding"]; ok {
		t.Fatal("agent forwarding must not be permitted")
	}
	for _, ext := range []string{"permit-pty", "permit-user-rc"} {
		if _, ok := cert.Permissions.Extensions[ext]; !ok {
			t.Fatalf("%s not permitted", ext)
		}
	}
	if checker.CheckCert("root", cert) == nil {
		t.Fatal("cert accepted for a principal other than agent")
	}

	// source-address pins the cert to the in-studio loopback path jam-studio-agent
	// dials: a client reaching sshd any other way (a host-published port) is
	// refused.
	if got := cert.CriticalOptions["source-address"]; got != "127.0.0.1/32,::1/128" {
		t.Fatalf("source-address = %q", got)
	}
	checker.Clock = func() time.Time { return now.Add(time.Hour) }
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:40000", true},
		{"[::1]:40000", true},
		{"172.17.0.1:40000", false},
		{"10.0.0.5:40000", false},
	} {
		err := sshLogin(t, &checker, cert, userPriv, tc.addr)
		if (err == nil) != tc.ok {
			t.Errorf("from %s: err = %v, want accepted=%v", tc.addr, err, tc.ok)
		}
	}
}

// sshLogin runs an SSH handshake whose server authenticates with checker and
// sees the client as coming from remote, and returns the client's error.
func sshLogin(t *testing.T, checker *ssh.CertChecker, cert *ssh.Certificate, userPriv ed25519.PrivateKey, remote string) error {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: checker.Authenticate}
	cfg.AddHostKey(hostSigner)
	userSigner, _ := ssh.NewSignerFromKey(userPriv)
	certSigner, err := ssh.NewCertSigner(cert, userSigner)
	if err != nil {
		t.Fatal(err)
	}
	// A real socket (net.Pipe is unbuffered and both sides write first).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr, _ := net.ResolveTCPAddr("tcp", remote)
	go func() {
		srv, err := ln.Accept()
		if err != nil {
			return
		}
		defer srv.Close()
		if c, _, _, err := ssh.NewServerConn(remoteConn{srv, addr}, cfg); err == nil {
			_ = c.Close()
		}
	}()
	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	c, _, _, err := ssh.NewClientConn(cli, "studio", &ssh.ClientConfig{
		User: "agent", Auth: []ssh.AuthMethod{ssh.PublicKeys(certSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test server
	})
	if err == nil {
		_ = c.Close()
	}
	return err
}

// remoteConn is a net.Conn that reports remote as its peer.
type remoteConn struct {
	net.Conn
	remote net.Addr
}

func (r remoteConn) RemoteAddr() net.Addr { return r.remote }

// A PKCS#8 PEM ed25519 key (`openssl genpkey -algorithm ed25519`) parses too.
func TestParseAcceptsPKCS8(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := Parse(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ssh.NewPublicKey(caPriv.Public())
	if !bytes.Equal(ca.PublicKey(), ssh.MarshalAuthorizedKey(want)) {
		t.Fatalf("public key = %q", ca.PublicKey())
	}
}

func TestParseRejectsNonEd25519AndGarbage(t *testing.T) {
	if _, err := Parse([]byte("not a key")); err == nil {
		t.Fatal("garbage accepted")
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	b, _ := ssh.MarshalPrivateKey(rsaKey, "")
	if _, err := Parse(pem.EncodeToMemory(b)); err == nil {
		t.Fatal("non-ed25519 CA accepted")
	}
}

// A key Parse rejects is never echoed into the error.
func TestParseErrorNeverEchoesKey(t *testing.T) {
	secret := "-----BEGIN OPENSSH PRIVATE KEY-----\nc2VjcmV0LWtleS1tYXRlcmlhbA==\n-----END OPENSSH PRIVATE KEY-----\n"
	_, err := Parse([]byte(secret))
	if err == nil {
		t.Fatal("corrupt key accepted")
	}
	if strings.Contains(err.Error(), "c2VjcmV0") {
		t.Fatalf("error echoes key material: %v", err)
	}
}

func TestIssueRejectsBadUserKeys(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	pemBlock, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := Parse(pem.EncodeToMemory(pemBlock))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ca.Issue([]byte("garbage"), "k", time.Now()); err == nil {
		t.Fatal("garbage user key accepted")
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaPub, _ := ssh.NewPublicKey(&rsaKey.PublicKey)
	if _, _, err := ca.Issue(ssh.MarshalAuthorizedKey(rsaPub), "k", time.Now()); err == nil {
		t.Fatal("non-ed25519 user key accepted")
	}
}
