package sshca

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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
	userPub, _, _ := ed25519.GenerateKey(rand.Reader)
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
	checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), caPub.Marshal()) }, Clock: func() time.Time { return now.Add(time.Hour) }}
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
}

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
