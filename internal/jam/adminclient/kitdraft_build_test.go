package adminclient

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKitDraftBuildClient(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI()+" ct="+r.Header.Get("Content-Type")+" if="+r.Header.Get("If-Match"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "POST":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","tree":"t","state":"running"}`)
		case "GET":
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","state":"running","log":"x","log_from":3,"log_end":4}`)
		case "DELETE":
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","state":"failed","error":"cancelled"}`)
		}
	}))
	defer ts.Close()
	c := New(ts.URL, "")
	if b, err := c.StartKitDraftBuild("app", `"t"`); err != nil || b.ID != "kdb_1" || b.State != "running" {
		t.Fatalf("start = %+v %v", b, err)
	}
	if b, err := c.KitDraftBuildStatus("app", 3); err != nil || b.Log != "x" || b.LogEnd != 4 {
		t.Fatalf("status = %+v %v", b, err)
	}
	if b, err := c.CancelKitDraftBuild("app"); err != nil || b.Error != "cancelled" {
		t.Fatalf("cancel = %+v %v", b, err)
	}
	want := []string{
		`POST /admin/kits/app/draft/build ct=application/json if="t"`,
		`GET /admin/kits/app/draft/build?since=3 ct= if=`,
		`DELETE /admin/kits/app/draft/build ct= if=`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(got, "\n"))
	}
}

// A refused start is the server's status and text.
func TestKitDraftBuildClientRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kit app already has a draft build running", http.StatusConflict)
	}))
	defer ts.Close()
	_, err := New(ts.URL, "").StartKitDraftBuild("app", `"t"`)
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Code != http.StatusConflict || !strings.Contains(se.Error(), "kit app already has a draft build running") {
		t.Fatalf("err = %#v", err)
	}
}
