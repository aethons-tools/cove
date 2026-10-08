// attention_test.go
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestAttentionList(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "cred.unavailable:vertex-gcp", Severity: condition.Critical, Summary: "credential vertex-gcp cannot be resolved", Fix: "gcloud auth application-default login"})
	tr.Raise(condition.Condition{Key: "k:old", Severity: condition.Warning, Summary: "old one"})
	tr.Clear("k:old")
	h := jam.NewAdminHandler(jam.NewMemStore(), nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, jam.WithAdminRoute("GET /admin/attention", condition.AdminHandler(tr)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"attention", "list", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "critical") || !strings.Contains(got, "cred.unavailable:vertex-gcp") || !strings.Contains(got, "fix: gcloud auth application-default login") {
		t.Fatalf("list output:\n%s", got)
	}
	if strings.Contains(got, "old one") {
		t.Fatal("resolved shown without --all")
	}
	out.Reset()
	_ = run([]string{"attention", "list", "--all", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if !strings.Contains(out.String(), "old one") || !strings.Contains(out.String(), "resolved") {
		t.Fatalf("--all output:\n%s", out.String())
	}
	_ = http.StatusOK
}
