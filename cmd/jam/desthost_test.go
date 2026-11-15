package main

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/jam"
)

const testDestDomain = "jam.example"

func TestValidateDestinations(t *testing.T) {
	const block = "destinations:\n  domain: jam.example\n  tls: { cert: /d.crt, key: /d.key }\n"
	ok, err := parseServeConfig([]byte("listen: jam.example:443\n" + block))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateDestinations(); err != nil {
		t.Fatalf("valid destinations refused: %v", err)
	}
	if d := ok.Destinations; d == nil || d.Domain != "jam.example" || d.TLS == nil || d.TLS.Cert != "/d.crt" || d.TLS.Key != "/d.key" {
		t.Fatalf("destinations parsed as %+v", d)
	}
	const previews = "admin-listen: 0.0.0.0:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n"
	for name, yml := range map[string]string{
		"missing tls":        "destinations:\n  domain: jam.example\n",
		"missing tls key":    "destinations:\n  domain: jam.example\n  tls: { cert: /d.crt }\n",
		"no domain":          "destinations:\n  tls: { cert: /d.crt, key: /d.key }\n",
		"uppercase domain":   "destinations:\n  domain: Jam.Example\n  tls: { cert: /d.crt, key: /d.key }\n",
		"single label":       "destinations:\n  domain: example\n  tls: { cert: /d.crt, key: /d.key }\n",
		"previews same":      previews + "previews:\n  domain: jam.example\n  tls: { cert: /p.crt, key: /p.key }\n" + block,
		"previews under":     previews + "previews:\n  domain: preview.jam.example\n  tls: { cert: /p.crt, key: /p.key }\n" + block,
		"previews over":      previews + "previews:\n  domain: example.com\n  tls: { cert: /p.crt, key: /p.key }\ndestinations:\n  domain: d.example.com\n  tls: { cert: /d.crt, key: /d.key }\n",
		"listen host under":  "listen: broker.jam.example:443\n" + block,
		"listen host folded": "listen: Broker.Jam.Example:443\n" + block,
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validateDestinations(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	apart, _ := parseServeConfig([]byte(previews + "previews:\n  domain: preview.example\n  tls: { cert: /p.crt, key: /p.key }\n" + block))
	if err := apart.validateDestinations(); err != nil {
		t.Fatalf("previews on another domain refused: %v", err)
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateDestinations(); err != nil {
		t.Fatalf("absent destinations must validate: %v", err)
	}
	if unknown := unknownServeKeys([]byte(block)); len(unknown) != 0 {
		t.Fatalf("destinations flagged unknown: %v", unknown)
	}
}

func TestLoadDestinationsCert(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, k := selfSignedPEM(t, "*."+testDestDomain)
	if _, err := loadDestinationsCert(write("ok.crt", c), write("ok.key", k), testDestDomain); err != nil {
		t.Fatalf("covering cert refused: %v", err)
	}
	c, k = selfSignedPEM(t, testDestDomain) // the domain only, no wildcard
	_, err := loadDestinationsCert(write("apex.crt", c), write("apex.key", k), testDestDomain)
	if err == nil || !strings.Contains(err.Error(), "*."+testDestDomain) {
		t.Fatalf("non-wildcard cert → %v, want a coverage error naming *.%s", err, testDestDomain)
	}
	assertNoKeyMaterial(t, err, k)
	c, _ = selfSignedPEM(t, "*."+testDestDomain)
	_, k2 := selfSignedPEM(t, "*."+testDestDomain)
	if _, err = loadDestinationsCert(write("ok2.crt", c), write("mismatch.key", k2), testDestDomain); err == nil {
		t.Fatal("mismatched key accepted")
	}
	assertNoKeyMaterial(t, err, k2)
	if _, err := loadDestinationsCert(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), testDestDomain); err == nil {
		t.Fatal("missing files accepted")
	}
}

func TestCoveTLSConfigPicksBySNI(t *testing.T) {
	jamCert := selfSigned(t, "jam.example")
	destCert := selfSigned(t, "*."+testDestDomain)
	dial := func(cfg *tls.Config, sni string) []byte {
		srv := httptest.NewUnstartedServer(http.NotFoundHandler())
		srv.TLS = cfg
		srv.StartTLS()
		defer srv.Close()
		conn, err := tls.Dial("tcp", strings.TrimPrefix(srv.URL, "https://"), &tls.Config{ServerName: sni, InsecureSkipVerify: true}) //nolint:gosec // asserting which self-signed leaf is presented
		if err != nil {
			t.Fatalf("dial %s: %v", sni, err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Raw
	}
	for sni, want := range map[string]tls.Certificate{
		"":                         jamCert, // no SNI
		"jam.example":              jamCert, // the domain itself is the Jam host
		"other.example":            jamCert,
		"gh." + testDestDomain:     destCert,
		"a.gh." + testDestDomain:   destCert, // under the domain: never the Jam cert
		"Linear." + testDestDomain: destCert,
	} {
		if got := dial(coveTLSConfig(jamCert, &destCert, testDestDomain), sni); string(got) != string(want.Certificate[0]) {
			t.Errorf("SNI %s: presented the wrong leaf", sni)
		}
	}
	if got := dial(coveTLSConfig(jamCert, nil, ""), "gh."+testDestDomain); string(got) != string(jamCert.Certificate[0]) {
		t.Error("without a destinations domain: presented the wrong leaf")
	}
}

// A request on a subdomain host goes to the broker ahead of the gRPC demux
// and the cove mux, so no path Jam serves (/connector, /squawks, gRPC
// Attach, …) is reachable there; the Jam host is served as before.
func TestDestHostRouterKeepsJamPathsOffSubdomains(t *testing.T) {
	st := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker := jam.NewBroker(st, nil, log)
	broker.SetDestinationsDomain(testDestDomain)
	gs := grpc.NewServer()
	defer gs.Stop()
	h := destHostRouter(testDestDomain, broker, grpcMux(gs, coveHTTPHandler(broker, st, nil, nil, false, nil, nil, log)))
	do := func(host, path string, grpcReq bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = host
		if grpcReq {
			r.ProtoMajor, r.ProtoMinor = 2, 0
			r.Header.Set("Content-Type", "application/grpc")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for _, host := range []string{"gh." + testDestDomain, "gh." + testDestDomain + ":443", "a.gh." + testDestDomain, "GH.Jam.Example."} {
		for _, path := range []string{"/connector", "/context", "/squawks", "/metrics", "/jam.v1.Attach/Attach"} {
			if rec := do(host, path, strings.HasPrefix(path, "/jam.")); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such destination") {
				t.Errorf("%s%s: %d %q, want the broker's 404", host, path, rec.Code, rec.Body.String())
			}
		}
	}
	// On the Jam host, /connector is Jam's own (it asks for an identity).
	if rec := do(testDestDomain, "/connector", false); strings.Contains(rec.Body.String(), "no such destination") {
		t.Errorf("Jam host /connector reached the broker: %d %q", rec.Code, rec.Body.String())
	}
}

// A destinations: list — destination definitions, as serve configs held them
// before the config repo did — is ignored with a warning, not a parse error.
func TestStaleDestinationsList(t *testing.T) {
	c, err := parseServeConfig([]byte("listen: :443\ndestinations:\n  - {name: git, route: /git/}\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Destinations != nil || !c.staleDestinations {
		t.Fatalf("destinations = %+v, stale = %v; want nil, true", c.Destinations, c.staleDestinations)
	}
	if err := c.validateDestinations(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
