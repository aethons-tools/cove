package kit

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/logging"
)

// The kit config.yml harbor: block is the deprecated name for jam: (Harbor →
// Jam rename). See docs/usage/jam/renamed-from-harbor.md.

func captureDeprecations(t *testing.T) *bytes.Buffer {
	t.Helper()
	logging.ResetDeprecations()
	var buf bytes.Buffer
	prev := deprecationOut
	deprecationOut = &buf
	t.Cleanup(func() { deprecationOut = prev; logging.ResetDeprecations() })
	return &buf
}

func TestJamKeyDoesNotWarn(t *testing.T) {
	warn := captureDeprecations(t)
	cfg, err := ParseConfig([]byte("name: k\njam:\n  host: h.example\n"))
	if err != nil || cfg.Jam == nil || cfg.Jam.Host != "h.example" {
		t.Fatalf("jam: must parse: cfg=%+v err=%v", cfg.Jam, err)
	}
	if warn.Len() != 0 {
		t.Fatalf("jam: must not warn; got %q", warn.String())
	}
}

func TestHarborKeyIsADeprecatedAliasForJam(t *testing.T) {
	warn := captureDeprecations(t)
	cfg, err := ParseConfig([]byte("name: k\nharbor:\n  host: h.example\n  identity: i\n  via-host-gateway: false\n"))
	if err != nil {
		t.Fatalf("harbor: must still parse: %v", err)
	}
	if cfg.Jam == nil || cfg.Jam.Host != "h.example" || cfg.Jam.Identity != "i" || cfg.Jam.HostGateway() {
		t.Fatalf("harbor: must populate Jam: %+v", cfg.Jam)
	}
	if cfg.DeprecatedHarbor != nil {
		t.Fatal("the alias field must be folded into Jam and cleared")
	}
	if !contains(RootDomains(cfg), "h.example") {
		t.Fatalf("an aliased host must still reach the allow-list: %v", RootDomains(cfg))
	}
	if !strings.Contains(warn.String(), "harbor:") || !strings.Contains(warn.String(), "jam:") || !strings.Contains(warn.String(), logging.RenameDoc) {
		t.Fatalf("want a deprecation warning naming harbor: → jam:; got %q", warn.String())
	}
}

func TestHarborKeyAliasIsValidatedLikeJam(t *testing.T) {
	captureDeprecations(t)
	for label, data := range map[string]string{
		"host w/ scheme":  "name: k\nharbor:\n  host: https://h.example\n",
		"empty host":      "name: k\nharbor:\n  identity: i\n",
		"harbor+provider": "name: k\nharbor:\n  host: h.example\nmodel-provider:\n  vertex:\n    env: { ANTHROPIC_VERTEX_PROJECT_ID: p, CLOUD_ML_REGION: us }\n",
	} {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected validation error, got nil", label)
		}
	}
}

func TestJamAndHarborBothPresentIsAnError(t *testing.T) {
	captureDeprecations(t)
	_, err := ParseConfig([]byte("name: k\njam:\n  host: a.example\nharbor:\n  host: b.example\n"))
	if err == nil || !strings.Contains(err.Error(), "jam") || !strings.Contains(err.Error(), "harbor") {
		t.Fatalf("both jam: and harbor: must be a validation error naming both; got %v", err)
	}
}
