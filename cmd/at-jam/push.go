package main

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// pushConfig enables Web Push notifications for the Jam app. The VAPID private
// key is resolved through the credential resolver (never on disk, in argv or
// in logs); the public key is derived from it.
type pushConfig struct {
	// VAPIDPrivateKeyCred names a demanded credential holding the base64url
	// P-256 private key.
	VAPIDPrivateKeyCred string `yaml:"vapid-private-key-cred"`
	// Subject is the VAPID "sub": a mailto: or https: contact.
	Subject string `yaml:"subject"`
	// Preview shows message text in notifications; nil means true.
	Preview *bool `yaml:"preview"`
}

// preview reports whether notification bodies carry message text.
func (p *pushConfig) preview() bool { return p.Preview == nil || *p.Preview }

// validatePush checks runtime.push when present; a no-op when unset (push off).
func (c serveConfig) validatePush() error {
	p := c.Runtime.Push
	if p == nil {
		return nil
	}
	if p.VAPIDPrivateKeyCred == "" {
		return fmt.Errorf("runtime.push.vapid-private-key-cred is required")
	}
	if _, ok := c.Credentials[p.VAPIDPrivateKeyCred]; !ok {
		return fmt.Errorf("runtime.push.vapid-private-key-cred %q is not a demanded credential", p.VAPIDPrivateKeyCred)
	}
	if p.Subject == "" {
		return fmt.Errorf("runtime.push.subject is required (mailto: or https:)")
	}
	if !strings.HasPrefix(p.Subject, "mailto:") && !strings.HasPrefix(p.Subject, "https://") {
		return fmt.Errorf("runtime.push.subject %q must start with mailto: or https://", p.Subject)
	}
	return nil
}

// vapidPublicKey derives the base64url uncompressed P-256 public point (65
// bytes) from a base64url private scalar (32 bytes).
func vapidPublicKey(privateB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(privateB64), "="))
	if err != nil {
		return "", fmt.Errorf("vapid private key is not base64url")
	}
	k, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("vapid private key is not a valid P-256 scalar")
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

const pushUsage = "expected keygen"

// cmdPush implements `at-jam push`; keygen prints a fresh VAPID key pair.
func cmdPush(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam push:", pushUsage)
		return 2
	}
	switch args[0] {
	case "keygen":
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam push keygen:", err)
			return 1
		}
		fmt.Fprintf(stdout, "private: %s\npublic: %s\n", priv, pub)
		return 0
	default:
		fmt.Fprintf(stderr, "at-jam push: unknown subcommand %q (%s)\n", args[0], pushUsage)
		return 2
	}
}

// pushKeys is the resolved VAPID pair. private stays in this process's memory
// (it signs the push services' JWTs); it is never logged.
type pushKeys struct {
	public  string // base64url, served to browsers
	private string // base64url, never logged
}

// resolvePush resolves runtime.push's VAPID private key through the
// credential specs (in memory only) and derives the public key. Absent block
// ⇒ nil, nil (push off). Errors name the credential, never its value.
func resolvePush(cfg serveConfig, specs map[string]secret.Spec) (*pushKeys, error) {
	p := cfg.Runtime.Push
	if p == nil {
		return nil, nil
	}
	name := p.VAPIDPrivateKeyCred
	v, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[name]})
	if err != nil {
		return nil, fmt.Errorf("runtime.push.vapid-private-key-cred: %w", err)
	}
	pub, err := vapidPublicKey(v[name])
	if err != nil {
		return nil, fmt.Errorf("runtime.push.vapid-private-key-cred %q: %w", name, err)
	}
	return &pushKeys{public: pub, private: v[name]}, nil
}
