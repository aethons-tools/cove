package launcher

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/jam"
)

const draftTagPrefix = "cove-kit-draft:"

// BuildKitDraft is jam.KitDraftBuilder: a kit draft's test build
// (docs/usage/jam/kits-drafts.md#test-build). It builds def exactly as
// PrepareKit does: the same assemble (harness, hardening, egress, MCP
// servers, key), the chain's parent stages cached and reused, and the same
// gate and build args. Only the tags differ: the draft's own stage is
// tagged jam.KitDraftStageTag(tag) and the image tag. Neither is ever a kit
// version's stage or image tag. With a backend.LoggedKitBuilder, the docker
// output goes to log and a done ctx stops the build. It returns the image
// id. The launcher's own log lines never print build-arg values.
func (l *Launcher) BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (string, error) {
	if !strings.HasPrefix(tag, draftTagPrefix) {
		return "", fmt.Errorf("draft build %s: %q is not a draft image tag", def.Ref.ID, tag)
	}
	var ops backend.KitImageBuilder = l.cfg.Ops
	if lb, ok := l.cfg.Ops.(backend.LoggedKitBuilder); ok {
		ops = lb.WithBuildLog(ctx, log)
	}
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
func (l *Launcher) HasKitDraftImage(id string) (bool, error) { return l.cfg.Ops.HasKitImage(id) }

var _ jam.KitDraftBuilder = (*Launcher)(nil)
