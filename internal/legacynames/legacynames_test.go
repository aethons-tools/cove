package legacynames

import "testing"

func TestRewriteKitText(t *testing.T) {
	in := "ARG COVE_BASE_IMAGE\nARG AT_JAM_STUDIO_TARGET_ARCH\nFROM ${COVE_BASE_IMAGE}\n" +
		"ENV COVE_SSHENV=\"GOPATH\"\nENV MY_COVE_BASE_IMAGE=keep\n# " + BaseImageRepo + "@sha256:ab\n"
	want := "ARG JAM_STUDIO_BASE_IMAGE\nARG JAM_STUDIO_TARGET_ARCH\nFROM ${JAM_STUDIO_BASE_IMAGE}\n" +
		"ENV JAM_SSHENV=\"GOPATH\"\nENV MY_COVE_BASE_IMAGE=keep\n# ghcr.io/aethons-tools/jam-studio-base@sha256:ab\n"
	got, changed := RewriteKitText(in)
	if !changed || got != want {
		t.Fatalf("RewriteKitText =\n%s\nwant\n%s", got, want)
	}
	if again, changed := RewriteKitText(got); changed || again != got {
		t.Fatalf("a second rewrite changed %q", again)
	}
}

func TestFindKitNameAndScannedFiles(t *testing.T) {
	if old, ok := FindKitName("FROM ${AT_JAM_STUDIO_BASE_IMAGE}"); !ok || old != "AT_JAM_STUDIO_BASE_IMAGE" {
		t.Fatalf("FindKitName = %q, %v", old, ok)
	}
	if _, ok := FindKitName("FROM ${JAM_STUDIO_BASE_IMAGE}"); ok {
		t.Fatal("a new name is not a pre-rename one")
	}
	for p, want := range map[string]bool{"kit.yml": true, "context/Dockerfile": true, "a/b.Dockerfile": true,
		"Dockerfile.dev": true, "Containerfile": true, "install.sh": false, "README.md": false} {
		if KitFileScanned(p) != want {
			t.Errorf("KitFileScanned(%q) != %v", p, want)
		}
	}
}

func TestContainer(t *testing.T) {
	if got := Container("ses_1/x"); got != "atcove-cove-ses_1-x" {
		t.Fatalf("Container = %q", got)
	}
}
