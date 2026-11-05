package blessgen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Registry reads blessed images' manifests and config blobs from an OCI
// distribution registry (ghcr.io in production, an httptest server in tests), so
// the build can embed each blessed digest's per-arch layer diff_ids and the
// provenance gate never inspects or pulls a blessed image.
//
// Auth follows the registry's bearer challenge (WWW-Authenticate → token realm).
// The token request is anonymous (jam-studio-base is a public package) or, with
// Token set, carries it as basic auth — only to a realm on the registry's own
// host, retrying anonymously if the realm refuses it. Requests go through the client's transport, so http(s)_proxy applies.
type Registry struct {
	HTTP  *http.Client
	Base  string // registry origin, e.g. "https://ghcr.io"
	Repo  string // repository path, e.g. "aethons-tools/jam-studio-base"
	Token string // optional GitHub token for the token realm

	bearer string // cached pull token
}

// Arches are the image architectures recorded: the Studio Manager hosts'
// (darwin/linux amd64/arm64), whose VM — and so studio image — arch matches.
var Arches = []string{"amd64", "arm64"}

const (
	mtOCIIndex     = "application/vnd.oci.image.index.v1+json"
	mtDockerList   = "application/vnd.docker.distribution.manifest.list.v2+json"
	mtOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	mtDockerManif  = "application/vnd.docker.distribution.manifest.v2+json"
	maxRegistryDoc = 4 << 20 // manifests and image configs are small
)

type manifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

type imageConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	RootFS       struct {
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// DiffIDs resolves digest — a multi-arch index or a single image manifest — to
// arch → rootfs diff_ids for its linux images among Arches. Every manifest and
// config body is verified against the digest it was fetched by. It errors when
// no wanted arch is present or an image has no diff_ids.
func (r *Registry) DiffIDs(ctx context.Context, digest string) (map[string][]string, error) {
	m, err := r.manifest(ctx, digest)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	if len(m.Manifests) == 0 { // a single image manifest
		cfg, err := r.config(ctx, digest, m)
		if err != nil {
			return nil, err
		}
		if cfg.OS == "linux" && wantArch(cfg.Architecture) {
			out[cfg.Architecture] = cfg.RootFS.DiffIDs
		}
	}
	for _, c := range m.Manifests {
		arch := c.Platform.Architecture
		if c.Platform.OS != "linux" || !wantArch(arch) || out[arch] != nil {
			continue // attestations (unknown/unknown), other platforms, variants after the first
		}
		cm, err := r.manifest(ctx, c.Digest)
		if err != nil {
			return nil, err
		}
		cfg, err := r.config(ctx, c.Digest, cm)
		if err != nil {
			return nil, err
		}
		if cfg.Architecture != arch {
			return nil, fmt.Errorf("blessgen: %s: index says linux/%s but image %s is %s", digest, arch, c.Digest, cfg.Architecture)
		}
		out[arch] = cfg.RootFS.DiffIDs
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("blessgen: %s has no linux image for %v", digest, Arches)
	}
	return out, nil
}

func wantArch(a string) bool { return slices.Contains(Arches, a) }

func (r *Registry) manifest(ctx context.Context, digest string) (manifest, error) {
	var m manifest
	b, err := r.get(ctx, "manifests", digest, strings.Join([]string{mtOCIIndex, mtDockerList, mtOCIManifest, mtDockerManif}, ", "))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("blessgen: manifest %s: %w", digest, err)
	}
	return m, nil
}

func (r *Registry) config(ctx context.Context, digest string, m manifest) (imageConfig, error) {
	var cfg imageConfig
	if m.Config.Digest == "" {
		return cfg, fmt.Errorf("blessgen: manifest %s names no config blob", digest)
	}
	b, err := r.get(ctx, "blobs", m.Config.Digest, "")
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("blessgen: image config %s: %w", m.Config.Digest, err)
	}
	if len(cfg.RootFS.DiffIDs) == 0 {
		return cfg, fmt.Errorf("blessgen: image %s (config %s) has no rootfs diff_ids", digest, m.Config.Digest)
	}
	return cfg, nil
}

// get fetches /v2/<repo>/<kind>/<digest>, answering one bearer challenge, and
// verifies the body's sha256 against digest.
func (r *Registry) get(ctx context.Context, kind, digest, accept string) ([]byte, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("blessgen: %q is not a sha256 digest", digest)
	}
	u := fmt.Sprintf("%s/v2/%s/%s/%s", strings.TrimSuffix(r.Base, "/"), r.Repo, kind, digest)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if r.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+r.bearer)
		}
		resp, err := r.client().Do(req) // blob redirects drop Authorization off-host
		if err != nil {
			return nil, err
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxRegistryDoc))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if err := r.authenticate(ctx, resp.Header.Get("WWW-Authenticate")); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("blessgen: GET %s %s: http %d", kind, digest, resp.StatusCode)
		}
		if rerr != nil {
			return nil, rerr
		}
		if got := fmt.Sprintf("sha256:%x", sha256.Sum256(body)); got != digest {
			return nil, fmt.Errorf("blessgen: %s %s: served content has digest %s", kind, digest, got)
		}
		return body, nil
	}
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authenticate answers a Bearer challenge: GET realm?service=…&scope=… and keep
// the returned token. The GitHub token goes only to a realm on the registry's host.
func (r *Registry) authenticate(ctx context.Context, challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("blessgen: registry %s: unsupported auth challenge %q", r.Base, challenge)
	}
	p := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(params, -1) {
		p[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || realm.Host == "" {
		return fmt.Errorf("blessgen: registry %s: bad auth realm %q", r.Base, p["realm"])
	}
	q := realm.Query()
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	q.Set("scope", "repository:"+r.Repo+":pull")
	realm.RawQuery = q.Encode()
	base, err := url.Parse(r.Base)
	withToken := r.Token != "" && err == nil && base.Host == realm.Host
	body, status, err := r.fetchToken(ctx, realm.String(), withToken)
	if err == nil && status != http.StatusOK && withToken {
		// A refused token (stale, under-scoped) — the package is public, so
		// retry anonymously rather than fail the build on a credential quirk.
		body, status, err = r.fetchToken(ctx, realm.String(), false)
	}
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("blessgen: registry token for %s: http %d", r.Repo, status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("blessgen: registry token for %s: %w", r.Repo, err)
	}
	r.bearer = tok.Token
	if r.bearer == "" {
		r.bearer = tok.AccessToken
	}
	if r.bearer == "" {
		return fmt.Errorf("blessgen: registry token for %s: empty token", r.Repo)
	}
	return nil
}

func (r *Registry) fetchToken(ctx context.Context, realm string, withToken bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return nil, 0, err
	}
	if withToken {
		req.SetBasicAuth("token", r.Token)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryDoc))
	return body, resp.StatusCode, err
}

func (r *Registry) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}

// LayerReader resolves a blessed digest to arch → diff_ids; *Registry is one.
type LayerReader interface {
	DiffIDs(ctx context.Context, digest string) (map[string][]string, error)
}

// CollectLayers resolves every blessed digest to its per-arch diff_ids — the
// content of basedigest's blessed/layers.json. Any failure is an error: the
// build must not embed a partial view silently.
func CollectLayers(ctx context.Context, lr LayerReader, digests []string) (map[string]map[string][]string, error) {
	out := make(map[string]map[string][]string, len(digests))
	for _, d := range digests {
		l, err := lr.DiffIDs(ctx, d)
		if err != nil {
			return nil, err
		}
		out[d] = l
	}
	return out, nil
}

// RenderLayers produces the layers.json body: {digest: {arch: [diff_ids]}},
// indented, keys sorted (deterministic for reproducible builds).
func RenderLayers(m map[string]map[string][]string) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
