# adopt-base Recipe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `just adopt-base <tag> [--breaking] [--pr]` recipe that resolves a published base image's `@sha256` index digest and pins it — rewriting `.at-cove/config.yml`'s `image.base` (always) and the blessed watermark (`--breaking` only).

**Architecture:** A pure-transform package (`internal/adoptbase`) does the file rewrites; a new resolver method on the existing `internal/blessgen` GHCR client turns a tag into an index digest; a thin `cmd/adopt-base` execution shell wires token → resolve → rewrite → optional PR. Preserves the repo's plan/execution split: all logic is hermetically testable; only I/O and git/gh live in `cmd`.

**Tech Stack:** Go 1.26 (stdlib only — `flag`, `net/http`, `os/exec`), `just`, GitHub packages REST API (via the existing `blessgen.GHCR`).

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Import the blessgen client as `github.com/aethons-tools/cove/internal/blessgen`.
- **No new dependencies.** Stdlib only.
- **Tests are hermetic** — no network/docker. `blessgen` tests are white-box (`package blessgen`) and build `[]version{}` fixtures directly; follow that pattern. `cmd/adopt-base` stays thin and is NOT unit-tested (matches `cmd/gen-blessed`).
- **Image refs (exact):** `ghcr.io/aethons-tools/cove-image` (the `image.base` repo) and `ghcr.io/aethons-tools/cove-base-image` (the watermark repo). Owner `aethons-tools`; packages `cove-image` / `cove-base-image`.
- **File paths (exact, repo-root-relative):** `.at-cove/config.yml`, `internal/basedigest/blessed/watermark.txt`.
- **Digest form:** `sha256:` + exactly 64 lowercase hex chars.
- **Token:** `GITHUB_TOKEN` with `read:packages`. Missing/unauthorized is a hard error (the tool cannot no-op offline).
- **Build/test commands:** `just test` (hermetic), `just lint` (vet + gofmt + shell/Dockerfile lint).
- **Commit trailer:** end every commit body with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_012wmteyfw6iv3mi5g6s2XA4
  ```

---

### Task 1: `blessgen.DigestForTag` — resolve a release tag to its index digest

**Files:**
- Modify: `internal/blessgen/blessgen.go` (extract the paging loop; add `DigestForTag` + pure `digestForTag`)
- Test: `internal/blessgen/blessgen_test.go` (append `TestDigestForTag`)

**Interfaces:**
- Consumes: existing unexported `version` struct (`Name`, `CreatedAt`, `Tags`) and `hasReleaseTag(tags []string) bool`.
- Produces: `func (g GHCR) DigestForTag(ctx context.Context, tag string) (string, error)` — returns the `sha256:…` digest of the release-tagged index manifest carrying `tag`; errors if the tag is absent or names only a per-arch child. Also the pure `func digestForTag(vs []version, tag string) (string, error)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/blessgen/blessgen_test.go`:

```go
// digestForTag maps a release tag to its index-manifest digest, and refuses tags
// that are absent or name only a per-arch child manifest.
func TestDigestForTag(t *testing.T) {
	vs := []version{
		{Name: "sha256:idx", CreatedAt: "2026-08-08T00:00:00Z", Tags: []string{"527-0808", "latest"}},
		{Name: "sha256:amd", CreatedAt: "2026-08-08T00:00:00Z", Tags: []string{"sha-abc123-amd64"}},
		{Name: "sha256:old", CreatedAt: "2026-07-18T00:00:00Z", Tags: []string{"400-0718"}},
	}
	if got, err := digestForTag(vs, "527-0808"); err != nil || got != "sha256:idx" {
		t.Fatalf("digestForTag(527-0808) = %q, %v; want sha256:idx", got, err)
	}
	if got, err := digestForTag(vs, "latest"); err != nil || got != "sha256:idx" {
		t.Fatalf("digestForTag(latest) = %q, %v; want sha256:idx", got, err)
	}
	if _, err := digestForTag(vs, "999-0101"); err == nil {
		t.Fatal("expected error for an absent tag")
	}
	if _, err := digestForTag(vs, "sha-abc123-amd64"); err == nil {
		t.Fatal("expected error for a per-arch intermediate tag")
	}
	if _, err := digestForTag(vs, ""); err == nil {
		t.Fatal("expected error for an empty tag")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/blessgen/ -run TestDigestForTag -v`
Expected: FAIL — `undefined: digestForTag`.

- [ ] **Step 3: Refactor the paging loop and add the resolver**

In `internal/blessgen/blessgen.go`, change `Digests` to delegate to a new `versions` helper, and add `DigestForTag` + `digestForTag`. Replace the existing `Digests` method body:

```go
// Digests implements Lister: it fetches every version page and returns the
// tagged manifest digests newest-first.
func (g GHCR) Digests(ctx context.Context) ([]string, error) {
	vs, err := g.versions(ctx)
	if err != nil {
		return nil, err
	}
	return digestsNewestFirst(vs), nil
}

// DigestForTag returns the sha256 digest of the released index manifest carrying
// tag (e.g. "527-0808" or "latest"). It errors if tag names no version or names
// only a per-arch child manifest — a kit must never pin those.
func (g GHCR) DigestForTag(ctx context.Context, tag string) (string, error) {
	vs, err := g.versions(ctx)
	if err != nil {
		return "", err
	}
	return digestForTag(vs, tag)
}

// digestForTag is the pure resolver behind DigestForTag.
func digestForTag(vs []version, tag string) (string, error) {
	if tag == "" {
		return "", fmt.Errorf("blessgen: empty tag")
	}
	var matches []version
	for _, v := range vs {
		for _, t := range v.Tags {
			if t == tag {
				matches = append(matches, v)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("blessgen: tag %q not found among %d published version(s)", tag, len(vs))
	case 1:
		if !hasReleaseTag(matches[0].Tags) {
			return "", fmt.Errorf("blessgen: tag %q resolves to a per-arch child manifest; pin a release tag (a version like 527-0808, or latest)", tag)
		}
		return matches[0].Name, nil
	default:
		return "", fmt.Errorf("blessgen: tag %q is ambiguous — matched %d versions", tag, len(matches))
	}
}

// versions fetches every page of the container package's versions from the GitHub
// packages REST API.
func (g GHCR) versions(ctx context.Context) ([]version, error) {
	httpc := g.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	var all []version
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/orgs/%s/packages/container/%s/versions?per_page=100&page=%d",
			ghAPI, g.Owner, g.Package, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if g.Token != "" {
			req.Header.Set("Authorization", "Bearer "+g.Token)
		}
		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		var pageVs []version
		dec := json.NewDecoder(resp.Body)
		derr := dec.Decode(&pageVs)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ghcr: list %s/%s versions: http %d", g.Owner, g.Package, resp.StatusCode)
		}
		if derr != nil {
			return nil, derr
		}
		if len(pageVs) == 0 {
			break
		}
		all = append(all, pageVs...)
	}
	return all, nil
}
```

Delete the old `Digests` body's inline paging loop (it now lives in `versions`). The package comment, imports, and all other functions are unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/blessgen/ -v`
Expected: PASS — `TestDigestForTag` plus all existing tests (`TestWalk*`, `TestWatermark`, `TestRender*`, `TestDigestsNewestFirstFiltersAndSorts`).

- [ ] **Step 5: Commit**

```bash
git add internal/blessgen/blessgen.go internal/blessgen/blessgen_test.go
git commit -m "feat(blessgen): DigestForTag resolves a release tag to its index digest

Extract the version paging into GHCR.versions and add DigestForTag (+ pure
digestForTag): map a release tag to its index-manifest digest, erroring on an
absent tag or a per-arch-only child. Reused by cmd/adopt-base.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012wmteyfw6iv3mi5g6s2XA4"
```

---

### Task 2: `internal/adoptbase` — pure file transforms

**Files:**
- Create: `internal/adoptbase/adoptbase.go`
- Test: `internal/adoptbase/adoptbase_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `const CoveImageRepo = "ghcr.io/aethons-tools/cove-image"`
  - `func RewriteImageBase(configYAML, digest string) (string, error)` — returns the config with `image.base` set to `CoveImageRepo@<digest>`.
  - `func RewriteWatermark(watermarkTxt, tag, digest, reason string) (string, error)` — returns the watermark file with its digest line and `# Current watermark:` comment updated; `reason` defaults when empty.

- [ ] **Step 1: Write the failing tests**

Create `internal/adoptbase/adoptbase_test.go`:

```go
package adoptbase

import (
	"strings"
	"testing"
)

const sampleConfig = `image:
  # This repo builds out its toolchain image in CI rather than locally.
  base: ghcr.io/aethons-tools/cove-image:2026.07.18-9528dd2
  allowed-domains:
    - gopkg.in
`

const goodDigest = "sha256:9292e42429bd7a93f67e35a100c7a1b533e0a0911c3a9f326eb2374ca738ae2f"
const baseDigest = "sha256:b675a81c589e96fac6423ed759e727b523b8f850c6ac5c0fa139aceeb7b91726"

func TestRewriteImageBase(t *testing.T) {
	out, err := RewriteImageBase(sampleConfig, goodDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "  base: ghcr.io/aethons-tools/cove-image@" + goodDigest
	if !strings.Contains(out, want) {
		t.Fatalf("output missing %q\n---\n%s", want, out)
	}
	if strings.Contains(out, "2026.07.18-9528dd2") {
		t.Fatal("old tag still present")
	}
	if !strings.Contains(out, "# This repo builds out its toolchain image") {
		t.Fatal("surrounding comment not preserved")
	}
	// Idempotent: rewriting the result with the same digest is a no-op.
	again, err := RewriteImageBase(out, goodDigest)
	if err != nil || again != out {
		t.Fatalf("not idempotent: err=%v", err)
	}
}

func TestRewriteImageBaseErrors(t *testing.T) {
	if _, err := RewriteImageBase("no base here\n", goodDigest); err == nil {
		t.Fatal("expected error when no image.base line is present")
	}
	if _, err := RewriteImageBase(sampleConfig, "sha256:short"); err == nil {
		t.Fatal("expected error for a malformed digest")
	}
}

const sampleWatermark = `# Low-watermark: the OLDEST cove-base-image still blessed.
#
# Current watermark: cove-base-image:2026.07.18-9528dd2 (post-at-task-shed, COV-46).
sha256:7ba377681641d4828af92e05ad97a42a6246347337908414aa4687feed73e6bb
`

func TestRewriteWatermark(t *testing.T) {
	out, err := RewriteWatermark(sampleWatermark, "527-0808", baseDigest, "Docker+systemd, drop podman — COV-116")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\n"+baseDigest+"\n") {
		t.Fatalf("new digest line missing\n---\n%s", out)
	}
	if strings.Contains(out, "7ba377681641") {
		t.Fatal("old digest still present")
	}
	wantComment := "# Current watermark: cove-base-image:527-0808 (Docker+systemd, drop podman — COV-116)"
	if !strings.Contains(out, wantComment) {
		t.Fatalf("comment not updated; want %q\n---\n%s", wantComment, out)
	}
	if !strings.HasPrefix(out, "# Low-watermark:") {
		t.Fatal("header comment not preserved")
	}
}

func TestRewriteWatermarkDefaultReason(t *testing.T) {
	out, err := RewriteWatermark(sampleWatermark, "527-0808", baseDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "(breaking base change)") {
		t.Fatalf("default reason missing\n---\n%s", out)
	}
}

func TestRewriteWatermarkErrors(t *testing.T) {
	if _, err := RewriteWatermark("# just a comment\n", "527-0808", baseDigest, ""); err == nil {
		t.Fatal("expected error when the file has no digest line")
	}
	if _, err := RewriteWatermark(sampleWatermark, "527-0808", "nope", ""); err == nil {
		t.Fatal("expected error for a malformed digest")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adoptbase/ -v`
Expected: FAIL — build error, `internal/adoptbase/adoptbase.go` does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/adoptbase/adoptbase.go`:

```go
// Package adoptbase holds the pure file transforms that adopt a new base image:
// pinning .at-cove/config.yml's image.base and raising the blessed watermark.
// All functions operate on file *contents* (no I/O) so they are hermetically
// testable; the cmd/adopt-base shell reads/writes the files.
package adoptbase

import (
	"fmt"
	"regexp"
	"strings"
)

// CoveImageRepo is the toolchain-image repository image.base names.
const CoveImageRepo = "ghcr.io/aethons-tools/cove-image"

const defaultReason = "breaking base change"

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateDigest(d string) error {
	if !digestRE.MatchString(d) {
		return fmt.Errorf("adoptbase: %q is not a sha256:<64-hex> digest", d)
	}
	return nil
}

// leadingWhitespace returns the indentation prefix of a line.
func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// RewriteImageBase returns configYAML with the image.base value set to
// CoveImageRepo@digest, preserving indentation and surrounding comments. It
// errors unless exactly one image.base line for CoveImageRepo is present.
func RewriteImageBase(configYAML, digest string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	lines := strings.Split(configYAML, "\n")
	matches := 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "base:") || !strings.Contains(trimmed, CoveImageRepo) {
			continue
		}
		lines[i] = leadingWhitespace(ln) + "base: " + CoveImageRepo + "@" + digest
		matches++
	}
	if matches != 1 {
		return "", fmt.Errorf("adoptbase: expected exactly 1 image.base line for %s, found %d", CoveImageRepo, matches)
	}
	return strings.Join(lines, "\n"), nil
}

// RewriteWatermark returns watermarkTxt with its single digest line set to digest
// and its "# Current watermark:" comment rewritten to name tag and reason. reason
// defaults when empty. It errors unless exactly one of each line is present.
func RewriteWatermark(watermarkTxt, tag, digest, reason string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	if reason == "" {
		reason = defaultReason
	}
	lines := strings.Split(watermarkTxt, "\n")
	comments, digests := 0, 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "# Current watermark:"):
			lines[i] = fmt.Sprintf("# Current watermark: cove-base-image:%s (%s)", tag, reason)
			comments++
		case trimmed != "" && !strings.HasPrefix(trimmed, "#"):
			lines[i] = digest
			digests++
		}
	}
	if comments != 1 || digests != 1 {
		return "", fmt.Errorf("adoptbase: unexpected watermark.txt shape: %d 'Current watermark' comment(s) and %d digest line(s), want 1 and 1", comments, digests)
	}
	return strings.Join(lines, "\n"), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/adoptbase/ -v`
Expected: PASS — all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/adoptbase/
git commit -m "feat(adoptbase): pure transforms to pin image.base and raise the watermark

RewriteImageBase and RewriteWatermark operate on file contents (no I/O),
preserving comments and validating the digest; hermetically tested.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012wmteyfw6iv3mi5g6s2XA4"
```

---

### Task 3: `cmd/adopt-base` execution shell + `just adopt-base` recipe

**Files:**
- Create: `cmd/adopt-base/main.go`
- Modify: `justfile` (add the `adopt-base` recipe after `gen-blessed`)

**Interfaces:**
- Consumes: `blessgen.GHCR{...}.DigestForTag`, `adoptbase.RewriteImageBase`, `adoptbase.RewriteWatermark`, `adoptbase.CoveImageRepo`.
- Produces: the `adopt-base` binary (via `go run ./cmd/adopt-base`), not shipped.

- [ ] **Step 1: Write the command**

Create `cmd/adopt-base/main.go`:

```go
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

	// 1. image.base — always.
	imgDigest, err := blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: coveImagePkg}.
		DigestForTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("resolve %s:%s: %w", coveImagePkg, tag, err)
	}
	if err := rewriteFile(configPath, func(s string) (string, error) {
		return adoptbase.RewriteImageBase(s, imgDigest)
	}); err != nil {
		return err
	}
	fmt.Printf("image.base -> %s@%s\n", adoptbase.CoveImageRepo, imgDigest)

	// 2. watermark — breaking only.
	if breaking {
		baseDigest, err := blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: coveBasePkg}.
			DigestForTag(ctx, tag)
		if err != nil {
			return fmt.Errorf("resolve %s:%s: %w", coveBasePkg, tag, err)
		}
		if err := rewriteFile(watermarkPath, func(s string) (string, error) {
			return adoptbase.RewriteWatermark(s, tag, baseDigest, reason)
		}); err != nil {
			return err
		}
		fmt.Printf("watermark -> cove-base-image@%s (blessed floor raised; run `just gen-blessed` to preview the set)\n", baseDigest)
	}

	// 3. --pr, else print next steps.
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
```

- [ ] **Step 2: Add the justfile recipe**

In `justfile`, after the `gen-blessed` recipe (the block ending `    go run ./cmd/gen-blessed`), add:

```makefile

# adopt a published base image: resolve <tag> to its @sha256 index digest and pin
# it in .at-cove/config.yml (image.base). Add --breaking to also raise the blessed
# watermark, and --pr to branch+commit+open a PR. Needs GITHUB_TOKEN (read:packages).
# e.g. `just adopt-base 527-0808 --breaking --pr`
adopt-base *ARGS:
    go run ./cmd/adopt-base {{ARGS}}
```

- [ ] **Step 3: Verify it builds and vets**

Run: `go build ./... && go vet ./cmd/adopt-base/`
Expected: no output (success).

- [ ] **Step 4: Smoke-test the arg/token guards (no network)**

Run: `go run ./cmd/adopt-base`
Expected: prints the `usage:` line to stderr, exits 2.

Run: `env -u GITHUB_TOKEN go run ./cmd/adopt-base 527-0808`
Expected: prints `adopt-base: GITHUB_TOKEN is required (needs read:packages); export a token and retry`, exits 1. (Confirm `.at-cove/config.yml` is unchanged: `git diff --quiet .at-cove/config.yml && echo clean`.)

- [ ] **Step 5: Commit**

```bash
git add cmd/adopt-base/main.go justfile
git commit -m "feat(adopt-base): cmd + just recipe to resolve and pin a new base

go run ./cmd/adopt-base <tag> [--breaking] [--pr]: resolve the @sha256 index
digest via blessgen and rewrite image.base (+ watermark on --breaking); --pr
branches, commits, and opens the PR. Thin execution shell over the tested
internal/adoptbase transforms and blessgen.DigestForTag.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012wmteyfw6iv3mi5g6s2XA4"
```

---

### Task 4: Document the workflow in `docs/DEVELOPMENT.md`

**Files:**
- Modify: `docs/DEVELOPMENT.md` (add an `### Adopting a new base` subsection after the `### Versioning` block)

**Interfaces:**
- Consumes: nothing. Produces: docs only.

- [ ] **Step 1: Add the subsection**

In `docs/DEVELOPMENT.md`, immediately after the `### Versioning` block (the paragraph ending `…falling back to the bare name on PATH when no sibling is present.`) and before `## Verified `claude` CLI facts`, insert:

```markdown
### Adopting a new base

Publishing is automatic (above), but **adopting** a published image — pointing
`.at-cove/config.yml`'s `image.base` at it — is a deliberate manual step, and a
**breaking** base change (one that shifts the OCI layer-diff-id prefix) also needs
the blessed watermark raised so older bases stop being trusted. `just adopt-base`
does both:

```
just adopt-base <tag>              # routine: pin image.base to <tag>'s @sha256 digest
just adopt-base <tag> --breaking   # also raise internal/basedigest/blessed/watermark.txt
just adopt-base <tag> --pr         # after editing, branch + commit + open a PR
```

It resolves `<tag>` (e.g. `527-0808`, or `latest`) to the multi-arch **index**
digest via the GitHub packages API and pins by digest (never a moving tag). It
needs `GITHUB_TOKEN` with `read:packages` (like `gen-blessed`) and cannot run
offline. `--breaking` prints the new blessed floor; preview the resulting set with
`just gen-blessed`. Only raise the watermark for a genuinely breaking base — doing
it on a routine bump wrongly evicts still-valid older bases. Design:
[adopt-base-recipe](superpowers/specs/2026-08-09-adopt-base-recipe-design.md).
```

- [ ] **Step 2: Verify the doc links resolve and lint passes**

Run: `test -f docs/superpowers/specs/2026-08-09-adopt-base-recipe-design.md && echo "link ok"`
Expected: `link ok`.

Run: `just lint`
Expected: exit 0 (no output on success).

- [ ] **Step 3: Commit**

```bash
git add docs/DEVELOPMENT.md
git commit -m "docs(dev): document the just adopt-base workflow

Add an 'Adopting a new base' subsection: routine pin vs --breaking watermark
raise, the read:packages token, and --pr.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012wmteyfw6iv3mi5g6s2XA4"
```

---

### Task 5: Full-suite verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full hermetic suite**

Run: `just test`
Expected: all packages `ok`, including `internal/blessgen` and `internal/adoptbase`.

- [ ] **Step 2: Run lint**

Run: `just lint`
Expected: exit 0.

- [ ] **Step 3: Confirm no stray working-tree changes**

Run: `git status --porcelain`
Expected: empty (every change is committed across Tasks 1–4).

---

## Notes for the implementer

- The plan lands on the existing `feat/adopt-base-recipe` branch (which already holds the design spec and the `ghcr.io` egress commit). Do **not** open the PR until the human decides — this is a self-contained feature but its own PR.
- The `--pr` path in `cmd/adopt-base` is intentionally untested (it shells out to git/gh), matching `cmd/gen-blessed`. Everything it depends on (`DigestForTag`, both `Rewrite*`) is tested.
- Do not touch `internal/basedigest/blessed/generated.txt` — it is gitignored and owned by `gen-blessed`.
