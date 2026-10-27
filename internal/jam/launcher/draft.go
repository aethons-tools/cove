package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/jam"
)

const draftTagPrefix = "jam-kit-draft:"

// errDraftImageID refuses a draft image id that is not a docker image id.
// The id reaches `docker run` and `docker image inspect` argv, and colima
// runs the mutable tag when it is empty, so only sha256:<64 hex> passes.
var errDraftImageID = errors.New("kit draft image id must be sha256:<64 lowercase hex>")

var draftImageIDRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// checkDraftImageID is errDraftImageID unless id is a docker image id.
func checkDraftImageID(id string) error {
	if !draftImageIDRe.MatchString(id) {
		return errDraftImageID
	}
	return nil
}

// BuildKitDraft is jam.KitDraftBuilder: a kit draft's test build
// (docs/usage/jam/kits-drafts.md#test-build). It builds def exactly as
// PrepareKit does: the same assemble (harness, hardening, egress, MCP
// servers, key), the chain's parent stages cached and reused, and the same
// gate and build args. Only the tags differ: the draft's own stage is
// tagged jam.KitDraftStageTag(tag) and the image tag. Neither is ever a kit
// version's stage or image tag. With a backend.LoggedKitBuilder, the docker
// output goes to log and a done ctx stops the build. Backends other than
// colima are not LoggedKitBuilders: their builds write no docker output to
// log, and ctx is checked only between steps, never mid-step. It returns
// the image id. The launcher's own log lines never print build-arg values.
func (l *Launcher) BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (string, error) {
	if !strings.HasPrefix(tag, draftTagPrefix) {
		return "", fmt.Errorf("draft build %s: %q is not a draft image tag", def.Ref.ID, tag)
	}
	var ops backend.KitImageBuilder = l.cfg.Ops
	if lb, ok := l.cfg.Ops.(backend.LoggedKitBuilder); ok {
		ops = lb.WithBuildLog(ctx, log)
	}
	// One dir per tag: two builds of the same tree would share (and one
	// would remove) it. Safe only because jam.KitDraftBuilds runs at most
	// one build per kit at a time, and a tag names one kit.
	buildDir := filepath.Join(l.cfg.BuildRoot, "draft", strings.TrimPrefix(tag, draftTagPrefix))
	defer os.RemoveAll(buildDir)
	fmt.Fprintf(log, "== kit draft %s: %s (harness %s %s)\n", def.Ref.ID, chainText(def.Chain), def.Harness.Type, def.Harness.Version)
	if err := l.cfg.assemble(def, buildDir); err != nil {
		return "", fmt.Errorf("draft build %s: assemble: %w", def.Ref.ID, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	base, err := l.resolveStudioBase(ops, def, jam.KitDraftStageTag(tag))
	if err != nil {
		return "", fmt.Errorf("draft build %s: resolve base: %w", def.Ref.ID, err)
	}
	fmt.Fprintf(log, "== image %s FROM %s\n", tag, base)
	id, err := ops.BuildKitImage(buildDir, tag, base, def.Kit.BuildArgs, false)
	if err != nil {
		return "", fmt.Errorf("draft build %s: build: %w", def.Ref.ID, err)
	}
	if err := checkDraftImageID(id); err != nil {
		return "", fmt.Errorf("draft build %s: docker reported image id %q: %w", def.Ref.ID, id, err)
	}
	l.cfg.Log.Info("built kit draft", "kit", def.Ref.ID, "tag", tag, "asm", shortAsm(l.asm))
	return id, nil
}

// chainText is a draft's chain root first, the draft last: "web@v2 → app (draft)".
func chainText(chain []jam.KitLink) string {
	names := make([]string, len(chain))
	for i, c := range chain {
		names[i] = c.Ref.String()
	}
	if n := len(names); n > 0 {
		names[n-1] = chain[n-1].Ref.ID + " (draft)"
	}
	return strings.Join(names, " → ")
}

// HasKitDraftImage is jam.KitDraftBuilder's presence check: the image id on
// the substrate (the daemon BuildKitDraft and Raise use).
// A malformed id is refused before any inspect.
func (l *Launcher) HasKitDraftImage(id string) (bool, error) {
	if err := checkDraftImageID(id); err != nil {
		return false, err
	}
	return l.cfg.Ops.HasKitImage(id)
}

var _ jam.KitDraftBuilder = (*Launcher)(nil)
