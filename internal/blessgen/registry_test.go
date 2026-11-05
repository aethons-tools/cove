package blessgen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRegistry is a minimal OCI distribution registry for repo: content-addressed
// manifests and blobs behind a GHCR-style bearer challenge (WWW-Authenticate →
// /token → Bearer). It serves exactly what the test put in.
type fakeRegistry struct {
	t       *testing.T
	repo    string
	content map[string][]byte // digest → body (manifests and blobs alike)
	types   map[string]string // digest → manifest media type
	srv     *httptest.Server

	wantBasic  string       // when set, /token requires this basic-auth password
	tokenCalls atomic.Int32 // /token hits
	tampered   string       // digest whose served body is altered
}

const fakeBearer = "fake-bearer"

func newFakeRegistry(t *testing.T, repo string) *fakeRegistry {
	r := &fakeRegistry{t: t, repo: repo, content: map[string][]byte{}, types: map[string]string{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		r.tokenCalls.Add(1)
		if req.URL.Query().Get("scope") != "repository:"+r.repo+":pull" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		if r.wantBasic != "" {
			if _, pw, ok := req.BasicAuth(); !ok || pw != r.wantBasic {
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
		}
		fmt.Fprintf(w, `{"token":%q}`, fakeBearer)
		return
	}
	if req.Header.Get("Authorization") != "Bearer "+fakeBearer {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, r.srv.URL, r.repo))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	prefix := "/v2/" + r.repo + "/"
	rest, ok := strings.CutPrefix(req.URL.Path, prefix)
	if !ok {
		http.NotFound(w, req)
		return
	}
	kind, digest, _ := strings.Cut(rest, "/")
	body, ok := r.content[digest]
	if !ok || (kind != "manifests" && kind != "blobs") {
		http.NotFound(w, req)
		return
	}
	if kind == "manifests" {
		w.Header().Set("Content-Type", r.types[digest])
	}
	if digest == r.tampered {
		body = append([]byte(" "), body...)
	}
	_, _ = w.Write(body)
}

func (r *fakeRegistry) put(body []byte, mediaType string) string {
	d := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	r.content[d] = body
	if mediaType != "" {
		r.types[d] = mediaType
	}
	return d
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// image publishes one per-arch image (config blob + manifest) and returns the
// manifest digest.
func (r *fakeRegistry) image(arch string, diffIDs []string) string {
	cfg := r.put(mustJSON(r.t, map[string]any{
		"architecture": arch, "os": "linux",
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	}), "")
	return r.put(mustJSON(r.t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfg},
	}), "application/vnd.oci.image.manifest.v1+json")
}

type platformRef struct{ digest, os, arch string }

func (r *fakeRegistry) index(mediaType string, children ...platformRef) string {
	var ms []map[string]any
	for _, c := range children {
		ms = append(ms, map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    c.digest,
			"platform":  map[string]any{"os": c.os, "architecture": c.arch},
		})
	}
	return r.put(mustJSON(r.t, map[string]any{"schemaVersion": 2, "mediaType": mediaType, "manifests": ms}), mediaType)
}

func (r *fakeRegistry) client() *Registry {
	return &Registry{HTTP: r.srv.Client(), Base: r.srv.URL, Repo: r.repo}
}

const testRepo = "aethons-tools/jam-studio-base"

// A multi-arch index yields each linux/amd64 and linux/arm64 image's diff_ids,
// read from its config blob; attestation entries (unknown/unknown) are skipped.
func TestRegistryDiffIDsIndex(t *testing.T) {
	for _, mt := range []string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	} {
		reg := newFakeRegistry(t, testRepo)
		amd := reg.image("amd64", []string{"sha256:a1", "sha256:a2"})
		arm := reg.image("arm64", []string{"sha256:r1"})
		att := reg.put([]byte(`{"schemaVersion":2,"config":{"digest":"sha256:none"}}`), "application/vnd.oci.image.manifest.v1+json")
		idx := reg.index(mt,
			platformRef{amd, "linux", "amd64"},
			platformRef{arm, "linux", "arm64"},
			platformRef{att, "unknown", "unknown"},
		)
		got, err := reg.client().DiffIDs(context.Background(), idx)
		if err != nil {
			t.Fatalf("%s: %v", mt, err)
		}
		if strings.Join(got["amd64"], ",") != "sha256:a1,sha256:a2" || strings.Join(got["arm64"], ",") != "sha256:r1" || len(got) != 2 {
			t.Fatalf("%s: DiffIDs = %v", mt, got)
		}
	}
}

// A single-arch manifest digest yields its own arch, from its config.
func TestRegistryDiffIDsSingleManifest(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	m := reg.image("arm64", []string{"sha256:r1", "sha256:r2"})
	got, err := reg.client().DiffIDs(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || strings.Join(got["arm64"], ",") != "sha256:r1,sha256:r2" {
		t.Fatalf("DiffIDs = %v", got)
	}
}

// Every body is checked against the digest it was fetched by: a registry (or a
// proxy) serving altered content fails the build rather than embedding it.
func TestRegistryDiffIDsVerifiesDigests(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	amd := reg.image("amd64", []string{"sha256:a1"})
	idx := reg.index("application/vnd.oci.image.index.v1+json", platformRef{amd, "linux", "amd64"})
	reg.tampered = amd
	if _, err := reg.client().DiffIDs(context.Background(), idx); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v, want a digest mismatch", err)
	}
}

// An index with no linux/amd64 or linux/arm64 image, an empty diff_ids list,
// and a missing digest are all errors.
func TestRegistryDiffIDsErrors(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	s390 := reg.image("s390x", []string{"sha256:s"})
	noArch := reg.index("application/vnd.oci.image.index.v1+json", platformRef{s390, "linux", "s390x"})
	empty := reg.image("amd64", nil)
	mismatch := reg.index("application/vnd.oci.image.index.v1+json", platformRef{reg.image("arm64", []string{"sha256:r"}), "linux", "amd64"})
	for name, d := range map[string]string{
		"no linux amd64/arm64": noArch,
		"no diff_ids":          empty,
		"arch mismatch":        mismatch,
		"absent":               "sha256:" + strings.Repeat("0", 64),
	} {
		if got, err := reg.client().DiffIDs(context.Background(), d); err == nil {
			t.Fatalf("%s: DiffIDs = %v, want an error", name, got)
		}
	}
}

// With a GitHub token the /token request carries it as basic auth; without one
// it is anonymous (a public package). The bearer is fetched once and reused.
func TestRegistryAuth(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	reg.wantBasic = "ghp_secret"
	m := reg.image("amd64", []string{"sha256:a1"})
	if _, err := reg.client().DiffIDs(context.Background(), m); err == nil {
		t.Fatal("anonymous must be refused when the registry demands credentials")
	}
	c := reg.client()
	c.Token = "ghp_secret"
	reg.tokenCalls.Store(0)
	if _, err := c.DiffIDs(context.Background(), m); err != nil {
		t.Fatalf("with the token: %v", err)
	}
	if n := reg.tokenCalls.Load(); n != 1 {
		t.Fatalf("/token called %d times, want once (bearer reused)", n)
	}
}

// The GitHub token is never sent to a token realm on another host than the
// registry: a hostile challenge must not exfiltrate it.
func TestRegistryNeverSendsTokenToForeignRealm(t *testing.T) {
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, pw, ok := req.BasicAuth(); ok && pw != "" {
			leaked.Store(true)
		}
		fmt.Fprint(w, `{"token":"x"}`)
	}))
	t.Cleanup(foreign.Close)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+foreign.URL+`/token",service="x",scope="repository:`+testRepo+`:pull"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(reg.Close)
	c := &Registry{HTTP: reg.Client(), Base: reg.URL, Repo: testRepo, Token: "ghp_secret"}
	_, _ = c.DiffIDs(context.Background(), "sha256:"+strings.Repeat("0", 64))
	if leaked.Load() {
		t.Fatal("the GitHub token was sent to a foreign token realm")
	}
}

// CollectLayers maps every blessed digest to its per-arch diff_ids, failing on
// the first digest the registry cannot resolve.
func TestCollectLayers(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	a := reg.index("application/vnd.oci.image.index.v1+json",
		platformRef{reg.image("amd64", []string{"sha256:a1"}), "linux", "amd64"},
		platformRef{reg.image("arm64", []string{"sha256:r1"}), "linux", "arm64"})
	b := reg.image("amd64", []string{"sha256:b1"})
	got, err := CollectLayers(context.Background(), reg.client(), []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a]["arm64"][0] != "sha256:r1" || got[b]["amd64"][0] != "sha256:b1" {
		t.Fatalf("CollectLayers = %v", got)
	}
	if _, err := CollectLayers(context.Background(), reg.client(), []string{a, "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Fatal("an unresolvable blessed digest must fail")
	}
}

// RenderLayers is deterministic JSON in basedigest's {digest: {arch: [diff_ids]}}
// shape.
func TestRenderLayers(t *testing.T) {
	m := map[string]map[string][]string{
		"sha256:b": {"arm64": {"sha256:2"}, "amd64": {"sha256:1"}},
		"sha256:a": {"amd64": {"sha256:0"}},
	}
	out, err := RenderLayers(m)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := RenderLayers(m)
	if string(out) != string(again) {
		t.Fatal("RenderLayers must be deterministic")
	}
	var back map[string]map[string][]string
	if err := json.Unmarshal(out, &back); err != nil || back["sha256:b"]["arm64"][0] != "sha256:2" {
		t.Fatalf("round-trip = %v, %v", back, err)
	}
	if strings.Index(string(out), "sha256:a") > strings.Index(string(out), "sha256:b") {
		t.Fatalf("keys must be sorted:\n%s", out)
	}
}

// A token the token realm refuses (wrong scope, expired) falls back to an
// anonymous pull token: jam-studio-base is public, so a credential quirk must
// not fail the release build.
func TestRegistryRefusedTokenFallsBackToAnonymous(t *testing.T) {
	reg := newFakeRegistry(t, testRepo)
	m := reg.image("amd64", []string{"sha256:a1"})
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, _, ok := req.BasicAuth(); ok && req.URL.Path == "/token" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		reg.serve(w, req)
	}))
	t.Cleanup(refusing.Close)
	reg.srv = refusing // challenges now name this server's realm
	c := &Registry{HTTP: refusing.Client(), Base: refusing.URL, Repo: testRepo, Token: "ghp_stale"}
	if got, err := c.DiffIDs(context.Background(), m); err != nil || got["amd64"][0] != "sha256:a1" {
		t.Fatalf("DiffIDs = %v, %v", got, err)
	}
}
