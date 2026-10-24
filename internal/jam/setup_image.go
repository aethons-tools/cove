package jam

import (
	"context"
	"errors"
)

// ImageFacts is what setup inspection reads from a built image on the
// launcher host, without starting it: its id, ENV, labels and the files it
// seeds into /agent-data (capped; SeedNote says why a listing is partial).
type ImageFacts struct {
	ID       string
	Env      []string // "KEY=VALUE" as baked; redacted at render
	Labels   map[string]string
	Seed     []SeedFile // sorted by Path, relative to the seed root
	SeedNote string     // "" = complete
	// SeedErr is set when the seed could not be read (a docker error on the
	// launcher host): Seed is nil and the seed section renders unavailable.
	SeedErr bool
}

// SeedFile is one regular file of the image's /agent-data seed. Text is set
// (HasText) for valid UTF-8 files within the text cap.
type SeedFile struct {
	Path    string
	Size    int64
	Text    string
	HasText bool
}

// ImageFactsReader reads an image's facts on the launcher host
// (internal/jam/imagefacts). nil (the placeholder launcher) leaves every
// image part unavailable.
type ImageFactsReader interface {
	ImageFacts(ctx context.Context, image string) (ImageFacts, error)
}

// Image-read failures, shown as their fixed text.
var (
	ErrImageNotPresent = errors.New("image not present on the launcher host")
	ErrImageUnreadable = errors.New("docker error on the launcher host")
)
