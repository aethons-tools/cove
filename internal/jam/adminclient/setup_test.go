package adminclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSetup(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"subject":"x","sections":[{"name":"egress","source":"config","body":"b"}]}`)
	}))
	defer ts.Close()
	c := New(ts.URL, "")
	s, err := c.RoleSetup("my proj", "dev", []string{"egress", "kit"}, "")
	if err != nil || s.Subject != "x" || s.Sections[0].Body != "b" {
		t.Fatalf("RoleSetup = %+v, %v", s, err)
	}
	if _, err := c.StudioSetup("w1", nil, true); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/admin/roles/my%20proj/dev/setup?sections=egress%2Ckit" || got[1] != "/admin/instances/w1/setup?diff=role" {
		t.Fatalf("paths = %v", got)
	}
}
