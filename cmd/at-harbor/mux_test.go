package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestMessagesMuxRouting(t *testing.T) {
	msgH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "messages")
	})
	escH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "escalate")
	})
	broker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "broker")
	})
	mux := messagesMux(msgH, escH, broker)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/messages", "messages"},
		{"/messages/targets", "messages"},
		{"/escalate", "escalate"},
		{"/", "broker"},
		{"/git/some/repo", "broker"},
		{"/messages/extra", "broker"}, // exact-match only, not a prefix route
		{"/escalate/extra", "broker"}, // exact-match only, not a prefix route
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("path %q: body = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// selfSignedCert returns a TLS cert for "localhost"/127.0.0.1 and a pool trusting it.
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func TestServeMuxRoutesGRPCAndHTTP(t *testing.T) {
	cert, pool := selfSignedCert(t)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	gs := grpc.NewServer()
	healthpb.RegisterHealthServer(gs, health.NewServer())

	const brokerBody = "hello-from-broker"
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, brokerBody)
	})

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	go func() { _ = serveMux(lis, tlsCfg, gs, httpHandler) }()
	t.Cleanup(func() { _ = lis.Close(); gs.Stop() })

	// give the mux a moment to start serving
	time.Sleep(100 * time.Millisecond)

	// (a) gRPC over TLS reaches the gRPC server.
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost"})))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("grpc health check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health status = %v, want SERVING", resp.Status)
	}

	// (b) HTTPS reaches the broker handler.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	hr, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer hr.Body.Close()
	body, _ := io.ReadAll(hr.Body)
	if string(body) != brokerBody {
		t.Fatalf("broker body = %q, want %q", body, brokerBody)
	}

	// (c) HTTP/2 reaches the broker handler (regression guard: the mux must serve h2 to the broker).
	h2client := &http.Client{Transport: &http2.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	hr2, err := h2client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("http2 broker get: %v", err)
	}
	defer hr2.Body.Close()
	if hr2.ProtoMajor != 2 {
		t.Fatalf("broker response proto = %d, want 2 (h2)", hr2.ProtoMajor)
	}
	b2, _ := io.ReadAll(hr2.Body)
	if string(b2) != brokerBody {
		t.Fatalf("h2 broker body = %q, want %q", b2, brokerBody)
	}
}
