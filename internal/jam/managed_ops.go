package jam

import (
	"context"
	"fmt"
	"io"
)

// ManagedDraftBuilder builds kit drafts on named managers ((*fleet.Router)).
type ManagedDraftBuilder interface {
	DraftManagers() []string // available, draft-capable, in router order
	BuildKitDraftOn(ctx context.Context, manager string, def KitDefinition, tag string, log io.Writer) (string, error)
	HasKitDraftImageOn(manager, imageID string) (bool, error)
	// DraftManagerErr is why a named manager cannot build kit drafts now
	// (unknown: ErrUnknownManager; away or incapable: ErrManagerUnavailable;
	// both name it), nil when it can. It asks no manager anything.
	DraftManagerErr(manager string) error
}

// ManagedImageReader reads image facts on a named manager; "" = the first available reader.
type ManagedImageReader interface {
	ImageFactsOn(ctx context.Context, manager, image string) (ImageFacts, error)
}

// OneDraftManager presents a single KitDraftBuilder as a ManagedDraftBuilder
// whose only manager is name (for a Jam without a router, and for tests).
func OneDraftManager(name string, b KitDraftBuilder) ManagedDraftBuilder {
	return oneDraft{name: name, b: b}
}

type oneDraft struct {
	name string
	b    KitDraftBuilder
}

func (o oneDraft) DraftManagers() []string { return []string{o.name} }

func (o oneDraft) check(manager string) error {
	if manager != o.name {
		return fmt.Errorf("%w: %s", ErrUnknownManager, manager)
	}
	return nil
}

func (o oneDraft) DraftManagerErr(manager string) error { return o.check(manager) }

func (o oneDraft) BuildKitDraftOn(ctx context.Context, manager string, def KitDefinition, tag string, log io.Writer) (string, error) {
	if err := o.check(manager); err != nil {
		return "", err
	}
	return o.b.BuildKitDraft(ctx, def, tag, log)
}

func (o oneDraft) HasKitDraftImageOn(manager, imageID string) (bool, error) {
	if err := o.check(manager); err != nil {
		return false, err
	}
	return o.b.HasKitDraftImage(imageID)
}
