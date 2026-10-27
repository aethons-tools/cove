package jam

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestValidManagerName(t *testing.T) {
	if err := ValidManagerName("mac-1"); err != nil {
		t.Fatalf("mac-1: %v", err)
	}
	if err := ValidManagerName("local"); err == nil || err.Error() != `manager name "local" is reserved` {
		t.Fatalf("local: %v", err)
	}
	if err := ValidManagerName("Bad"); err == nil || err.Error() != `invalid manager name "Bad"` {
		t.Fatalf("Bad: %v", err)
	}
}

type tagDraft struct{ tag string }

func (o *tagDraft) BuildKitDraft(_ context.Context, _ KitDefinition, tag string, _ io.Writer) (string, error) {
	o.tag = tag
	return "id", nil
}
func (o *tagDraft) HasKitDraftImage(string) (bool, error) { return true, nil }

func TestOneDraftManager(t *testing.T) {
	b := &tagDraft{}
	m := OneDraftManager("x", b)
	if got := m.DraftManagers(); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("DraftManagers = %v", got)
	}
	if id, err := m.BuildKitDraftOn(context.Background(), "x", KitDefinition{}, "t", nil); err != nil || id != "id" || b.tag != "t" {
		t.Fatalf("build: %q %v %q", id, err, b.tag)
	}
	if ok, err := m.HasKitDraftImageOn("x", "id"); err != nil || !ok {
		t.Fatalf("has: %v %v", ok, err)
	}
	if _, err := m.BuildKitDraftOn(context.Background(), "y", KitDefinition{}, "t", nil); !errors.Is(err, ErrUnknownManager) {
		t.Fatalf("other: %v", err)
	}
	if _, err := m.HasKitDraftImageOn("y", "id"); !errors.Is(err, ErrUnknownManager) {
		t.Fatalf("other has: %v", err)
	}
}
