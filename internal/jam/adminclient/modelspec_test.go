package adminclient

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestClientModelSpecRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(n string) bool { return n == "anthropic" }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithModelSpecs(store, creds, false, log)))
	t.Cleanup(ts.Close)
	c := New(ts.URL, "")

	m := jam.ModelSpec{Name: "claude-default", Type: jam.HarnessClaude, Version: "2.x",
		Principal: jam.ModelPrincipal{Credential: "anthropic"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}
	if err := c.CreateModelSpec(m); err != nil {
		t.Fatalf("CreateModelSpec: %v", err)
	}
	if err := c.CreateModelSpec(m); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateModelSpec = %v, want ErrConflict", err)
	}
	m.Version = "2.1.x"
	if err := c.UpdateModelSpec(m); err != nil {
		t.Fatalf("UpdateModelSpec: %v", err)
	}
	got, err := c.GetModelSpec(m.Name)
	if err != nil || got.Version != "2.1.x" {
		t.Fatalf("GetModelSpec = %+v, %v", got, err)
	}
	list, err := c.ListModelSpecs()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListModelSpecs = %+v, %v", list, err)
	}
	if err := c.DeleteModelSpec(m.Name); err != nil {
		t.Fatalf("DeleteModelSpec: %v", err)
	}
	if _, err := c.GetModelSpec(m.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetModelSpec after delete = %v, want ErrNotFound", err)
	}
}
