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

func TestIsLoopbackRequest(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"203.0.113.7:5555", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.addr
		if got := IsLoopbackRequest(r); got != c.want {
			t.Errorf("IsLoopbackRequest(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
