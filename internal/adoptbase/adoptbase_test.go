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

func TestRewriteWatermarkIdempotent(t *testing.T) {
	once, err := RewriteWatermark(sampleWatermark, "527-0808", baseDigest, "Docker+systemd, drop podman — COV-116")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	twice, err := RewriteWatermark(once, "527-0808", baseDigest, "Docker+systemd, drop podman — COV-116")
	if err != nil || twice != once {
		t.Fatalf("not idempotent: err=%v", err)
	}
}

func TestRewriteImageBaseIgnoresSiblingRepo(t *testing.T) {
	cfg := "image:\n  base: ghcr.io/aethons-tools/cove-image-arm64:1.0\n"
	if _, err := RewriteImageBase(cfg, goodDigest); err == nil {
		t.Fatal("expected error: cove-image-arm64 must not match the cove-image base line")
	}
}
