// Command gen-blessed snapshots the blessed cove-base-image digests from the
// container registry before `go build`, so at-cove embeds every current
// cove-base-image as blessed (COV-44 spec §4). It is the build-time wiring around
// internal/blessgen; the walk logic lives there and is hermetically tested.
//
// It reads the committed low-watermark (internal/basedigest/blessed/watermark.txt)
// and writes the snapshot to the sibling generated.txt — which is gitignored, so
// the working tree stays clean (goreleaser's dirty-tree check stays happy). The
// dir default can be overridden with a positional arg.
//
// With GITHUB_TOKEN set (CI) it lists the published cove-base-image digests and
// walks newest→oldest through the watermark. Any failure there — including a
// watermark pruned from the registry — is fatal, so a broken trust set can never
// ship silently. With no token (offline / local dev) it is a no-op: no
// generated.txt is written and at-cove embeds the committed watermark alone.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/blessgen"
)

func main() {
	dir := "internal/basedigest/blessed"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := run(dir, os.Getenv("GITHUB_TOKEN")); err != nil {
		fmt.Fprintf(os.Stderr, "gen-blessed: %v\n", err)
		os.Exit(1)
	}
}

func run(dir, token string) error {
	watermarkPath := filepath.Join(dir, "watermark.txt")
	generatedPath := filepath.Join(dir, "generated.txt")

	current, err := os.ReadFile(watermarkPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", watermarkPath, err)
	}
	watermark, err := blessgen.Watermark(string(current))
	if err != nil {
		return err
	}

	if token == "" {
		fmt.Printf("gen-blessed: no GITHUB_TOKEN — offline, embedding low-watermark %s alone\n", watermark)
		return nil
	}

	owner, pkg := ownerPackage(basedigest.Image)
	lister := blessgen.GHCR{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Token:   token,
		Owner:   owner,
		Package: pkg,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	listing, err := lister.Digests(ctx)
	if err != nil {
		return fmt.Errorf("list %s versions: %w", basedigest.Image, err)
	}
	blessed, err := blessgen.Walk(listing, watermark)
	if err != nil {
		return err
	}
	if err := os.WriteFile(generatedPath, []byte(blessgen.Render(blessed)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", generatedPath, err)
	}
	fmt.Printf("gen-blessed: wrote %d blessed digest(s) to %s (head %s, watermark %s)\n",
		len(blessed), generatedPath, blessed[0], watermark)
	return nil
}

// ownerPackage splits a ghcr.io/<owner>/<package> reference into its owner and
// package-name parts for the GitHub packages API.
func ownerPackage(image string) (owner, pkg string) {
	ref := strings.TrimPrefix(image, "ghcr.io/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}
