// Command adopt-base pins a newly-published base image. It resolves a release
// tag to its @sha256 index digest via the GitHub packages API and rewrites
// .at-cove/config.yml's image.base (always) and, with --breaking, the blessed
// watermark (internal/basedigest/blessed/watermark.txt). With --pr it branches,
// commits, and opens a PR; otherwise it edits the files and prints next steps.
//
// It needs GITHUB_TOKEN with read:packages (same as gen-blessed) and — unlike
// gen-blessed — cannot no-op offline, since it has nothing to resolve.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/adoptbase"
	"github.com/aethons-tools/cove/internal/blessgen"
)

const (
	owner         = "aethons-tools"
	coveImagePkg  = "cove-image"
	coveBasePkg   = "cove-base-image"
	configPath    = ".at-cove/config.yml"
	watermarkPath = "internal/basedigest/blessed/watermark.txt"
)

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintln(os.Stderr, `usage: adopt-base <tag> [--breaking] [--pr] [--reason "..."]`)
		os.Exit(2)
	}
	tag := os.Args[1]
	fs := flag.NewFlagSet("adopt-base", flag.ExitOnError)
	breaking := fs.Bool("breaking", false, "also raise the blessed watermark (breaking base change)")
	pr := fs.Bool("pr", false, "branch, commit, push, and open a PR")
	reason := fs.String("reason", "", `parenthetical for the watermark comment (with --breaking)`)
	_ = fs.Parse(os.Args[2:])

	if err := run(tag, *breaking, *pr, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "adopt-base: %v\n", err)
		os.Exit(1)
	}
}

func run(tag string, breaking, pr bool, reason string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required (needs read:packages); export a token and retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpc := &http.Client{Timeout: 30 * time.Second}

	// Resolve every digest BEFORE writing any file, so a resolution failure
	// (bad/absent tag, auth, transient error) never leaves a partial edit.
	imgDigest, err := blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: coveImagePkg}.
		DigestForTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("resolve %s:%s: %w", coveImagePkg, tag, err)
	}
	var baseDigest string
	if breaking {
		baseDigest, err = blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: coveBasePkg}.
			DigestForTag(ctx, tag)
		if err != nil {
			return fmt.Errorf("resolve %s:%s: %w", coveBasePkg, tag, err)
		}
	}

	// All digests resolved; now write.
	if err := rewriteFile(configPath, func(s string) (string, error) {
		return adoptbase.RewriteImageBase(s, imgDigest)
	}); err != nil {
		return err
	}
	fmt.Printf("image.base -> %s@%s\n", adoptbase.CoveImageRepo, imgDigest)

	if breaking {
		if err := rewriteFile(watermarkPath, func(s string) (string, error) {
			return adoptbase.RewriteWatermark(s, tag, baseDigest, reason)
		}); err != nil {
			return err
		}
		fmt.Printf("watermark -> cove-base-image@%s (blessed floor raised; run `just gen-blessed` to preview the set)\n", baseDigest)
	}

	if pr {
		return openPR(tag, breaking)
	}
	fmt.Println("done — files edited, not committed. Review the diff, then commit (or re-run with --pr).")
	return nil
}

// rewriteFile reads path, applies transform to its contents, and writes it back.
func rewriteFile(path string, transform func(string) (string, error)) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := transform(string(b))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// openPR branches, commits the edited files, pushes, and opens a PR via gh.
func openPR(tag string, breaking bool) error {
	branch := "chore/adopt-base-" + tag
	files := []string{configPath}
	title := "chore(base-image): adopt " + tag
	if breaking {
		files = append(files, watermarkPath)
		title += " + raise blessed watermark (breaking)"
	}
	body := fmt.Sprintf("Adopt the base image published as `%s`, pinned by `@sha256` digest.", tag)
	if breaking {
		body += "\n\n**Breaking:** the blessed watermark is raised to the new base, so kits still pinned to an older base are rejected by the provenance gate until they adopt."
	}
	steps := [][]string{
		{"git", "checkout", "-b", branch},
		append([]string{"git", "add"}, files...),
		{"git", "commit", "-m", title + "\n\n" + body},
		{"git", "push", "-u", "origin", branch},
		{"gh", "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body},
	}
	for _, args := range steps {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}
