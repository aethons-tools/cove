package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/install"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
)

// A destinations domain adds exactly ".<domain>" (its subdomains) to the infra
// list; without one the list is the Jam host alone, as before.
func TestStudioEgressDestinationsDomain(t *testing.T) {
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"pkg.go.dev"}}
	for _, tc := range []struct {
		name, jamHost, domain string
		want                  []string
	}{
		{"no domain", "jam.example.com", "", []string{"jam.example.com"}},
		{"separate domain", "jam.example.com", "dest.example.com", []string{"jam.example.com", ".dest.example.com"}},
		// ".<domain>" covers its apex, so the exact Jam host would only
		// duplicate it in one squid ACL.
		{"domain is the jam host", "jam.example.com", "jam.example.com", []string{".jam.example.com"}},
		{"jam host under the domain", "jam.example.com", "example.com", []string{".example.com"}},
		{"look-alike is not under", "xjam.example.com", "jam.example.com", []string{"xjam.example.com", ".jam.example.com"}},
	} {
		eg, _ := studioEgress(sk, tc.jamHost, tc.domain)
		if !slices.Equal(eg.Infra, tc.want) {
			t.Errorf("%s: infra = %v, want %v", tc.name, eg.Infra, tc.want)
		}
		if !slices.Equal(eg.Policy, []string{"pkg.go.dev"}) {
			t.Errorf("%s: policy = %v; the domain must never reach the kit list or ceiling", tc.name, eg.Policy)
		}
	}
}

// The real assembler bakes ".<domain>" into allowed_domains.infra.txt.
func TestDefaultAssembleBakesDestinationsDomain(t *testing.T) {
	for _, tc := range []struct{ domain, want string }{
		{"", "jam.example.com"},
		{"dest.example.com", ".dest.example.com,jam.example.com"},
	} {
		l := New(Config{JamHost: "jam.example.com", DestinationsDomain: tc.domain, PublicKey: []byte("k\n")})
		buildDir := filepath.Join(t.TempDir(), "b")
		if err := l.defaultAssemble(studioKitDef("web", 1, studio.StudioKit{Kind: studio.Kind}), buildDir); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.infra.txt"))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, ln := range strings.Split(string(b), "\n") {
			if ln != "" && !strings.HasPrefix(ln, "#") {
				got = append(got, ln)
			}
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("domain %q: infra.txt = %v, want %s", tc.domain, got, tc.want)
		}
	}
}

// legacyAsmDigest is asmDigest as it was before the destinations domain: the
// four length-prefixed fields. A launcher without a domain must match it, so
// every existing Jam keeps its image tags (no rebuild).
func legacyAsmDigest(identity, defaultRef, jamHost string, pub []byte) string {
	h := sha256.New()
	for _, f := range [][]byte{[]byte(identity), []byte(defaultRef), []byte(jamHost), pub} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestAsmDigestUnchangedWithoutDestinationsDomain(t *testing.T) {
	if got, want := asmDigest("id", "ref", "jam.a", []byte("key"), ""), legacyAsmDigest("id", "ref", "jam.a", []byte("key")); got != want {
		t.Fatalf("asmDigest without a domain = %s, want the legacy %s", got, want)
	}
	id, _ := install.AssemblyIdentity()
	l := New(Config{JamHost: "jam.a", PublicKey: []byte("k")})
	if want := legacyAsmDigest(id, basedigest.DefaultRef(), "jam.a", []byte("k")); l.asm != want {
		t.Fatalf("launcher asm = %s, want the legacy %s", l.asm, want)
	}
	d := New(Config{JamHost: "jam.a", PublicKey: []byte("k"), DestinationsDomain: "dest.example"})
	if d.asm == l.asm {
		t.Fatal("a destinations domain must change the assembly fingerprint (it is baked into the image)")
	}
	if asmDigest("id", "ref", "jam.a", []byte("key"), "a.example") == asmDigest("id", "ref", "jam.a", []byte("key"), "b.example") {
		t.Fatal("the domain must key the fingerprint")
	}
}

func newDestLauncher(ops *fakeOps, domain string, logw *bytes.Buffer) *Launcher {
	return New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh", DestinationsDomain: domain,
		Inventory: readyInv(),
		Log:       slog.New(slog.NewTextHandler(logw, nil)),
		sleep:     func(time.Duration) {},
	})
}

// The raise maps the spec's destination hosts to the gateway beside the Jam
// host — only names exactly one valid label under this launcher's domain.
// Anything else is dropped with a warning and never reaches docker argv.
func TestRaiseAddsDestinationHosts(t *testing.T) {
	ops := &fakeOps{}
	var logs bytes.Buffer
	l := newDestLauncher(ops, "dest.example.com", &logs)
	hosts := []string{
		"gh.dest.example.com", "git.dest.example.com", "gh.dest.example.com", // dup
		"dest.example.com",            // the apex
		"a.b.dest.example.com",        // two labels deep
		"evil.example.com",            // another domain
		"xdest.example.com",           // look-alike suffix
		"GH.dest.example.com",         // not lowercase
		"-x.dest.example.com",         // bad label
		"xn--a.dest.example.com",      // "--"
		"gh.dest.example.com:1.2.3.4", // smuggled mapping
		"gh.dest.example.com --privileged",
		"",
	}
	spec := jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, DestinationHosts: hosts}
	if _, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"jam.example.com", "gh.dest.example.com", "git.dest.example.com"}
	if !slices.Equal(ops.runAddHost, want) {
		t.Fatalf("addHosts = %v, want %v", ops.runAddHost, want)
	}
	if !strings.Contains(logs.String(), "destination host refused") {
		t.Fatalf("refused hosts must be logged:\n%s", logs.String())
	}
}

// A launcher without a destinations domain maps none of them: its studios'
// egress does not admit them either.
func TestRaiseWithoutDomainAddsNoDestinationHosts(t *testing.T) {
	ops := &fakeOps{}
	var logs bytes.Buffer
	l := newDestLauncher(ops, "", &logs)
	spec := jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, DestinationHosts: []string{"gh.dest.example.com"}}
	if _, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ops.runAddHost, []string{"jam.example.com"}) {
		t.Fatalf("addHosts = %v, want the Jam host only", ops.runAddHost)
	}
	if !strings.Contains(logs.String(), "destination host refused") {
		t.Fatalf("refused hosts must be logged:\n%s", logs.String())
	}
}
