package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned is a throwaway self-signed cert for dnsNames.
func selfSigned(t *testing.T, dnsNames ...string) tls.Certificate {
	t.Helper()
	certPEM, keyPEM := selfSignedPEM(t, dnsNames...)
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func selfSignedPEM(t *testing.T, dnsNames ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}

const testPreviewDomain = "preview.jam.example"

func TestHostRouter(t *testing.T) {
	tag := func(s string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s) })
	}
	h := hostRouter(testPreviewDomain, tag("preview"), tag("rest"))
	label := strings.Repeat("a", 26)
	for host, want := range map[string]string{
		"web--" + label + "." + testPreviewDomain:                 "preview",
		"web--" + label + "." + testPreviewDomain + ":8443":       "preview",
		"jam." + testPreviewDomain:                                "preview", // the login host
		"jam." + testPreviewDomain + ":8443":                      "preview",
		"jam.example":                                             "rest", // the admin host
		"jam.example:8443":                                        "rest",
		"127.0.0.1:8443":                                          "rest",
		"web--" + label + ".other.example":                        "rest",
		"foo." + testPreviewDomain:                                "preview", // unparseable, still not the admin API
		"web--BAD_label." + testPreviewDomain:                     "preview",
		"a.b." + testPreviewDomain:                                "preview",
		testPreviewDomain:                                         "preview",
		"WEB--" + strings.ToUpper(label) + ".Preview.Jam.Example": "preview",
		"foo." + testPreviewDomain + ".":                          "preview",
		"":                                                        "rest",
		"[::1]:8443":                                              "rest",
	} {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if got := rec.Body.String(); got != want {
			t.Errorf("Host %q → %s, want %s", host, got, want)
		}
	}
}

func TestAdminTLSConfigPicksBySNI(t *testing.T) {
	adminCert := selfSigned(t, "jam.example")
	previewCert := selfSigned(t, "*."+testPreviewDomain)
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = adminTLSConfig(adminCert, previewCert, testPreviewDomain)
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	for sni, want := range map[string]tls.Certificate{
		"":                         adminCert, // no SNI
		"unknown.example":          adminCert,
		"foo." + testPreviewDomain: previewCert,
		"jam.example":              adminCert,
		"jam." + testPreviewDomain: previewCert,
		"web--" + strings.Repeat("b", 26) + "." + testPreviewDomain: previewCert,
	} {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: sni, InsecureSkipVerify: true}) //nolint:gosec // asserting which self-signed leaf is presented
		if err != nil {
			t.Fatalf("dial %s: %v", sni, err)
		}
		got := conn.ConnectionState().PeerCertificates[0].Raw
		_ = conn.Close()
		if string(got) != string(want.Certificate[0]) {
			t.Errorf("SNI %s: presented the wrong leaf", sni)
		}
	}
}

func TestLoadPreviewCert(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, k := selfSignedPEM(t, "*."+testPreviewDomain)
	if _, err := loadPreviewCert(write("ok.crt", c), write("ok.key", k), testPreviewDomain); err != nil {
		t.Fatalf("covering cert refused: %v", err)
	}
	c, k = selfSignedPEM(t, "*.other.example")
	_, err := loadPreviewCert(write("other.crt", c), write("other.key", k), testPreviewDomain)
	if err == nil || !strings.Contains(err.Error(), "*."+testPreviewDomain) {
		t.Fatalf("foreign cert → %v, want a coverage error naming *.%s", err, testPreviewDomain)
	}
	assertNoKeyMaterial(t, err, k)
	c, k = selfSignedPEM(t, "jam."+testPreviewDomain) // login host only, no wildcard
	if _, err := loadPreviewCert(write("login.crt", c), write("login.key", k), testPreviewDomain); err == nil {
		t.Fatal("non-wildcard cert accepted")
	}
	// A mismatched key: the load error must not echo the key either.
	c, _ = selfSignedPEM(t, "*."+testPreviewDomain)
	_, k2 := selfSignedPEM(t, "*."+testPreviewDomain)
	_, err = loadPreviewCert(write("ok2.crt", c), write("mismatch.key", k2), testPreviewDomain)
	if err == nil {
		t.Fatal("mismatched key accepted")
	}
	assertNoKeyMaterial(t, err, k2)
	if _, err := loadPreviewCert(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), testPreviewDomain); err == nil {
		t.Fatal("missing files accepted")
	}
}

// assertNoKeyMaterial fails if err's text holds the key PEM header or any
// 16-char slice of the key's base64 body.
func assertNoKeyMaterial(t *testing.T, err error, keyPEM []byte) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "PRIVATE KEY") {
		t.Fatalf("error carries a key PEM header: %s", msg)
	}
	blk, _ := pem.Decode(keyPEM)
	body := base64.StdEncoding.EncodeToString(blk.Bytes)
	for i := 0; i+16 <= len(body); i++ {
		if strings.Contains(msg, body[i:i+16]) {
			t.Fatalf("error carries key material: %s", msg)
		}
	}
}
