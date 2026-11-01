package adminclient

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestClientModelSpecReads(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(n string) bool { return n == "anthropic" }, nil, log, nil,
		jam.WithModelSpecs(store)))
	t.Cleanup(ts.Close)
	c := New(ts.URL, "")

	m := jam.ModelSpec{Name: "claude-default", Type: jam.HarnessClaude, Version: "2.1.1",
		Principal: jam.ModelPrincipal{Credential: "anthropic"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}
	if err := store.PutModelSpec(m); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetModelSpec(m.Name)
	if err != nil || got.Version != "2.1.1" {
		t.Fatalf("GetModelSpec = %+v, %v", got, err)
	}
	list, err := c.ListModelSpecs()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListModelSpecs = %+v, %v", list, err)
	}
	if _, err := c.GetModelSpec("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetModelSpec of an absent spec = %v, want ErrNotFound", err)
	}
}
