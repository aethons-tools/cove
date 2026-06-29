package backend

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

type stub struct{}

func (stub) Create(CreateContext) (Instance, error) { return Instance{}, nil }
func (stub) Dial(string) (Endpoint, func(), error)  { return Endpoint{}, func() {}, nil }
func (stub) Destroy(Instance, bool) error           { return nil }
func (stub) GetStatus(string) (State, error)        { return StateAbsent, nil }

func TestRegistryGetKnown(t *testing.T) {
	Register("stub", func(runner.Runner) Backend { return stub{} })
	f, err := Get("stub")
	if err != nil {
		t.Fatal(err)
	}
	if f(&runner.Fake{}) == nil {
		t.Fatal("factory produced nil backend")
	}
}

func TestRegistryGetUnknownListsSupported(t *testing.T) {
	Register("stub", func(runner.Runner) Backend { return stub{} })
	_, err := Get("nope")
	if err == nil || !strings.Contains(err.Error(), "stub") {
		t.Fatalf("error should list supported backends, got %v", err)
	}
}
