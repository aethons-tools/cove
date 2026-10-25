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
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
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

func TestIDTokenShaped(t *testing.T) {
	jwt := func(hdr, claims string) string {
		enc := base64.RawURLEncoding.EncodeToString
		return enc([]byte(hdr)) + "." + enc([]byte(claims)) + ".sig"
	}
	for name, tok := range map[string]string{
		"id token":           jwt(`{"alg":"RS256","typ":"JWT"}`, `{"aud":"cli","sub":"a"}`),
		"single-element aud": jwt(`{"alg":"RS256"}`, `{"aud":["cli"],"sub":"a"}`),
	} {
		if err := idTokenShaped(tok, "cli"); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, tok := range map[string]string{
		"at+jwt":             jwt(`{"alg":"RS256","typ":"at+jwt"}`, `{"aud":"cli"}`),
		"AT+JWT":             jwt(`{"alg":"RS256","typ":"AT+JWT"}`, `{"aud":"cli"}`),
		"application/at+jwt": jwt(`{"alg":"RS256","typ":"application/at+jwt"}`, `{"aud":"cli"}`),
		"api audience":       jwt(`{"alg":"RS256"}`, `{"aud":["https://api","cli"]}`),
		"other client":       jwt(`{"alg":"RS256"}`, `{"aud":"browser"}`),
		"no aud":             jwt(`{"alg":"RS256"}`, `{"sub":"a"}`),
		"not a jwt":          "opaque-token",
		"bad base64":         "!!.!!.!!",
	} {
		if idTokenShaped(tok, "cli") == nil {
			t.Errorf("%s accepted as a participant ID token", name)
		}
	}
}

type recordSink struct{ calls []string }

func (r *recordSink) ExposuresChanged(actorID string, ports []int32) {
	r.calls = append(r.calls, fmt.Sprint(actorID, " ", ports))
}

type recordCloser struct{ calls []string }

func (r *recordCloser) CloseUnexposed(session string, exposed []int32) int {
	r.calls = append(r.calls, fmt.Sprint(session, " ", exposed))
	return 0
}

// The fan-out forwards every change and has the registry close the session's
// preview connections to any port outside the new set — told on every change,
// the first included, so a fan-out that has seen nothing since a Jam restart
// still closes an unexposed port's connections.
func TestExposureFanoutClosesUnexposedPorts(t *testing.T) {
	sink, closer := &recordSink{}, &recordCloser{}
	f := newExposureFanout(sink, closer, nil)
	f.ExposuresChanged("ses_a", []int32{5173}) // first change since a restart: 3000 was unexposed
	f.ExposuresChanged("ses_b", []int32{3000})
	f.ExposuresChanged("ses_a", nil) // torn down
	want := []string{"ses_a [5173]", "ses_b [3000]", "ses_a []"}
	if !slices.Equal(sink.calls, want) {
		t.Fatalf("forwarded %q, want %q", sink.calls, want)
	}
	if !slices.Equal(closer.calls, want) {
		t.Fatalf("closed outside %q, want %q", closer.calls, want)
	}
}

// The admin listener speaks h2 to preview hosts but does not advertise RFC
// 8441 extended CONNECT (SETTINGS_ENABLE_CONNECT_PROTOCOL), so a browser opens
// a preview's WebSocket as an HTTP/1.1 Upgrade — which the preview proxy
// carries — never as an h2 extended CONNECT, which httputil.ReverseProxy
// cannot. Go keeps it off unless GODEBUG=http2xconnect=1 (golang/go#71128);
// this pins that for the server main builds.
func TestAdminListenerDoesNotAdvertiseExtendedConnect(t *testing.T) {
	if strings.Contains(os.Getenv("GODEBUG"), "http2xconnect=1") {
		t.Skip("GODEBUG=http2xconnect=1 turns extended CONNECT on")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: adminTLSConfig(selfSigned(t, "jam.example"), selfSigned(t, "*."+testPreviewDomain), testPreviewDomain)}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	defer srv.Close()
	host := "app--" + strings.Repeat("b", 26) + "." + testPreviewDomain
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: host, NextProtos: []string{"h2", "http/1.1"}, InsecureSkipVerify: true}) //nolint:gosec // self-signed test server
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := conn.ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Fatalf("negotiated %q, want h2 (the test needs an h2 server)", p)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	fr := http2.NewFramer(conn, conn)
	if err := fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		sf, ok := f.(*http2.SettingsFrame)
		if !ok || sf.IsAck() {
			continue
		}
		if v, set := sf.Value(http2.SettingEnableConnectProtocol); set && v != 0 {
			t.Fatalf("server advertises SETTINGS_ENABLE_CONNECT_PROTOCOL=%d", v)
		}
		return
	}
}

// Every change, whatever its ports, tells the proxy to drop its label cache.
func TestExposureFanoutCallsOnChange(t *testing.T) {
	n := 0
	f := newExposureFanout(&recordSink{}, &recordCloser{}, func() { n++ })
	f.ExposuresChanged("ses_a", []int32{3000})
	f.ExposuresChanged("ses_a", []int32{3000})
	f.ExposuresChanged("ses_a", nil)
	if n != 3 {
		t.Fatalf("onChange called %d times, want 3", n)
	}
}

// A previews domain on the admin host's registrable domain lets preview apps
// set cookies the admin site receives: serve warns about each such host.
func TestPreviewSharedSiteHosts(t *testing.T) {
	for _, tc := range []struct {
		domain string
		hosts  []string
		want   []string // "<host> <registrable domain>"
	}{
		{"preview.jam.example", []string{"jam.example"}, []string{"jam.example jam.example"}},
		{"preview.jam.example", []string{"admin.jam.example", "ui.other.example"}, []string{"admin.jam.example jam.example"}},
		{"previews.example.co.uk", []string{"Admin.Example.co.uk."}, []string{"Admin.Example.co.uk. example.co.uk"}},
		{"a.co.uk", []string{"b.co.uk"}, nil},
		{"preview.jam-previews.example", []string{"jam.example"}, nil},
		{"preview.jam.example", []string{"", "127.0.0.1", "::1", "localhost"}, nil},
	} {
		var got []string
		for _, s := range previewSharedSiteHosts(tc.domain, tc.hosts) {
			got = append(got, s.host+" "+s.site)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s %q: %q, want %q", tc.domain, tc.hosts, got, tc.want)
		}
	}
}

// The admin hosts serve judges are admin-listen's (when a name) and ui-hosts.
func TestPreviewAdminHosts(t *testing.T) {
	got := previewAdminHosts(&serveConfig{AdminListen: "jam.example:8443", UIHosts: []string{"ui.example"}})
	if !slices.Equal(got, []string{"jam.example", "ui.example"}) {
		t.Fatalf("%q", got)
	}
	if got := previewAdminHosts(&serveConfig{AdminListen: ":8443"}); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}

// Previews ride the admin listener, so a non-443 admin port must be in the
// URL; otherwise the browser lands on the cove-facing :443 listener and its cert.
func TestPreviewURLNamesAdminPort(t *testing.T) {
	const id = "ses_01m4bc9fmg5v4bc7cq3at6jp6n"
	for listen, want := range map[string]string{
		":8443":            "https://app--01m4bc9fmg5v4bc7cq3at6jp6n.p.example:8443/",
		"0.0.0.0:443":      "https://app--01m4bc9fmg5v4bc7cq3at6jp6n.p.example/",
		"jam.example:9443": "https://app--01m4bc9fmg5v4bc7cq3at6jp6n.p.example:9443/",
	} {
		got, err := previewURLFunc("p.example", listen)(id, "app")
		if err != nil || got != want {
			t.Errorf("listen %q: %q, %v; want %q", listen, got, err, want)
		}
	}
}

// Previews without ui-origins: serve warns that the Jam pane can't frame them.
func TestPreviewUIOriginsWarning(t *testing.T) {
	const want = "previews: ui-origins is empty, so the Jam pane can't frame previews; add the UI's own origin"
	if got := previewUIOriginsWarning(&serveConfig{Previews: &previewsConfig{Domain: "p.example"}}); got != want {
		t.Fatalf("%q", got)
	}
	if got := previewUIOriginsWarning(&serveConfig{Previews: &previewsConfig{Domain: "p.example"}, UIOrigins: []string{"https://jam.example"}}); got != "" {
		t.Fatalf("with origins: %q", got)
	}
	if got := previewUIOriginsWarning(&serveConfig{}); got != "" {
		t.Fatalf("previews off: %q", got)
	}
}
