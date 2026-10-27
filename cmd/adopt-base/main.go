// Command adopt-base raises the blessed watermark to a newly-published,
// breaking base image. It resolves a release tag to jam-studio-base's @sha256
// index digest via the GitHub packages API and rewrites
// internal/basedigest/blessed/watermark.txt, so kits on older bases fail the
// provenance gate until they move. With --pr it branches, commits, and opens a
// PR; otherwise it edits the file and prints next steps. (A routine base needs
// no adoption: the release pipeline's blessed-list snapshot makes the newest
// base Jam's default.)
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
	basePkg       = "jam-studio-base"
	watermarkPath = "internal/basedigest/blessed/watermark.txt"
)

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintln(os.Stderr, `usage: adopt-base <tag> [--pr] [--reason "..."]`)
		os.Exit(2)
	}
	tag := os.Args[1]
	fs := flag.NewFlagSet("adopt-base", flag.ExitOnError)
	pr := fs.Bool("pr", false, "branch, commit, push, and open a PR")
	reason := fs.String("reason", "", `parenthetical for the watermark comment`)
	_ = fs.Parse(os.Args[2:])

	if err := run(tag, *pr, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "adopt-base: %v\n", err)
		os.Exit(1)
	}
}

func run(tag string, pr bool, reason string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required (needs read:packages); export a token and retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpc := &http.Client{Timeout: 30 * time.Second}

	// Resolve BEFORE writing, so a resolution failure (bad/absent tag, auth,
	// transient error) never leaves a partial edit.
	baseDigest, err := blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: basePkg}.
		DigestForTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("resolve %s:%s: %w", basePkg, tag, err)
	}
	if err := rewriteFile(watermarkPath, func(s string) (string, error) {
		return adoptbase.RewriteWatermark(s, tag, baseDigest, reason)
	}); err != nil {
		return err
	}
	fmt.Printf("watermark -> jam-studio-base@%s (blessed floor raised; run `just gen-blessed` to preview the set)\n", baseDigest)

	if pr {
		return openPR(tag)
	}
	fmt.Println("done — file edited, not committed. Review the diff, then commit (or re-run with --pr).")
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

// openPR branches, commits the edited watermark, pushes, and opens a PR via gh.
func openPR(tag string) error {
	branch := "chore/adopt-base-" + tag
	title := "chore(base-image): raise blessed watermark to " + tag + " (breaking)"
	body := fmt.Sprintf("Raise the blessed watermark to the base image published as `%s`, pinned by `@sha256` digest.\n\n**Breaking:** kits still pinned to an older base are rejected by the provenance gate until they adopt.", tag)
	steps := [][]string{
		{"git", "checkout", "-b", branch},
		{"git", "add", watermarkPath},
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
