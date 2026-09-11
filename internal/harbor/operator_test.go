package harbor

import (
	"net/http/httptest"
	"testing"
)

func TestLoopbackAuthenticator(t *testing.T) {
	a := LoopbackAuthenticator{}

	r := httptest.NewRequest("GET", "/admin/healthz", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if op, err := a.Authenticate(r); err != nil || op.ID != "local" {
		t.Fatalf("loopback: op=%+v err=%v", op, err)
	}

	r2 := httptest.NewRequest("GET", "/admin/healthz", nil)
	r2.RemoteAddr = "10.0.0.5:9999"
	if _, err := a.Authenticate(r2); err == nil {
		t.Fatal("non-loopback request should be rejected")
	}
}
