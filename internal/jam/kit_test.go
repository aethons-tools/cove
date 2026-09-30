package jam

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

func TestKitRefStringIsStableKey(t *testing.T) {
	if got := (KitRef{ID: "managed", Version: 3}).String(); got != "managed@v3" {
		t.Fatalf("KitRef.String() = %q, want managed@v3", got)
	}
}

func TestErrKitNotReadyIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("raise spider@v1"), ErrKitNotReady)
	if !errors.Is(wrapped, ErrKitNotReady) {
		t.Fatal("ErrKitNotReady must be matchable with errors.Is when wrapped")
	}
}

func TestKitStateReadyDistinctFromPreparing(t *testing.T) {
	if KitReady == KitPreparing {
		t.Fatal("KitReady and KitPreparing must differ")
	}
}

// KitDefinition carries a KitRef + the full studio.StudioKit (the chunky payload
// sent only on a miss). Compile-level guard that the shape is what later tasks
// expect.
func TestKitDefinitionCarriesRefAndKit(t *testing.T) {
	def := KitDefinition{Ref: KitRef{ID: "web", Version: 1}, Kit: studio.StudioKit{Kind: studio.Kind, Name: "web"}}
	if def.Ref.ID != "web" || def.Kit.Name != "web" {
		t.Fatalf("KitDefinition = %+v", def)
	}
}
