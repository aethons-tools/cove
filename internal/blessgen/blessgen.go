// Package blessgen generates the blessed cove-base-image list at build time from
// the container registry, so at-cove's provenance gate (COV-34) trusts every
// current cove-base-image without a hand-maintained list.
//
// The repo commits exactly one digest — the low-watermark, the oldest base still
// blessed. Before `go build`, a build step lists the published cove-base-image
// digests newest-first and Walks from the head down to and including the
// watermark; the result is written to internal/basedigest/blessed/generated.txt
// for go:embed. With no registry access (offline / local dev) the step is a no-op
// and the committed watermark is embedded alone. The registry query sits behind
// Lister so the walk logic is hermetically testable; see cmd/gen-blessed.
package blessgen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Walk returns the blessed list: the digests of `listing` — which MUST be ordered
// newest-first — from the head down to AND INCLUDING `watermark`, dropping
// everything older. A watermark that is empty or absent from the listing is an
// error: a pruned or mistyped watermark must fail the build loudly, never
// silently shrink the trust set to an implicit prefix.
func Walk(listing []string, watermark string) ([]string, error) {
	if watermark == "" {
		return nil, fmt.Errorf("blessgen: empty watermark")
	}
	for i, d := range listing {
		if d == watermark {
			return listing[:i+1], nil
		}
	}
	return nil, fmt.Errorf("blessgen: watermark %s not found in the registry listing of %d image(s) — "+
		"the watermark was pruned from the registry or is wrong; refusing to shrink the trust set", watermark, len(listing))
}

// Watermark returns the low-watermark from a watermark.txt body: the oldest
// (last) meaningful digest, ignoring #-comments and blank lines. It errors when
// the body carries no digest.
func Watermark(content string) (string, error) {
	digests := parse(content)
	if len(digests) == 0 {
		return "", fmt.Errorf("blessgen: no watermark digest in the committed file")
	}
	return digests[len(digests)-1], nil
}

// Render produces a generated.txt body from digests ordered newest-first:
// a self-describing #-comment header followed by one digest per line. The last
// line is the watermark the next build reads back via Watermark.
func Render(digests []string) string {
	var b strings.Builder
	b.WriteString("# Blessed cove-base-image manifest digests (sha256), newest first.\n")
	b.WriteString("#\n")
	b.WriteString("# GENERATED at build time by cmd/gen-blessed from the container registry\n")
	b.WriteString("# (COV-44 spec §4). Do NOT commit or hand-edit — this file is gitignored and\n")
	b.WriteString("# rewritten on every build. To move the trust floor, bump the committed\n")
	b.WriteString("# blessed/watermark.txt (the last line here). Offline builds embed the\n")
	b.WriteString("# watermark alone.\n")
	for _, d := range digests {
		b.WriteString(d)
		b.WriteByte('\n')
	}
	return b.String()
}

func parse(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Lister enumerates published cove-base-image manifest digests, newest first.
// It is the network seam: GHCR satisfies it in production; tests use a fake.
type Lister interface {
	Digests(ctx context.Context) ([]string, error)
}

// version is one GHCR container package version (its Name is the manifest digest).
type version struct {
	Name      string   `json:"name"`
	CreatedAt string   `json:"created_at"`
	Tags      []string `json:"-"`
}

func (v *version) UnmarshalJSON(b []byte) error {
	var raw struct {
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
		Metadata  struct {
			Container struct {
				Tags []string `json:"tags"`
			} `json:"container"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	v.Name = raw.Name
	v.CreatedAt = raw.CreatedAt
	v.Tags = raw.Metadata.Container.Tags
	return nil
}

// digestsNewestFirst keeps only the published multi-arch index manifests a kit
// can select, ordered by publish time, newest first. Dropped: untagged per-arch
// child manifests (a kit never pins them), and the per-arch *intermediate* tags
// the publish step leaves behind (`sha-<sha>-amd64` / `-arm64`) — a version
// survives only if it carries a real release tag (`latest` or a version tag).
// RFC3339 timestamps sort correctly as strings, keeping this pure and hermetic.
func digestsNewestFirst(vs []version) []string {
	kept := make([]version, 0, len(vs))
	for _, v := range vs {
		if hasReleaseTag(v.Tags) {
			kept = append(kept, v)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].CreatedAt > kept[j].CreatedAt
	})
	out := make([]string, len(kept))
	for i, v := range kept {
		out[i] = v.Name
	}
	return out
}

// hasReleaseTag reports whether any tag names a released image rather than a
// per-arch build intermediate (`sha-<sha>-<arch>`). The index manifest carries
// `latest` and the version tag; only the arch children carry the intermediates.
func hasReleaseTag(tags []string) bool {
	for _, t := range tags {
		if t == "" || strings.HasPrefix(t, "sha-") ||
			strings.HasSuffix(t, "-amd64") || strings.HasSuffix(t, "-arm64") {
			continue
		}
		return true
	}
	return false
}

// GHCR lists a container package's versions via the GitHub packages REST API.
type GHCR struct {
	HTTP    *http.Client
	Token   string
	Owner   string // the org that owns the package, e.g. "aethons-tools"
	Package string // the container package name, e.g. "cove-base-image"
}

const ghAPI = "https://api.github.com"

// Digests implements Lister: it fetches every version page and returns the
// tagged manifest digests newest-first.
func (g GHCR) Digests(ctx context.Context) ([]string, error) {
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
	return digestsNewestFirst(all), nil
}
