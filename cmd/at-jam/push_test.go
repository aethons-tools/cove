package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

func pushCfg(p *pushConfig) serveConfig {
	c := serveConfig{Credentials: map[string]credSpec{"jam-vapid": {}}}
	c.Runtime.Push = p
	return c
}

func TestValidatePush(t *testing.T) {
	if err := (serveConfig{}).validatePush(); err != nil {
		t.Fatalf("no push block: %v", err)
	}
	for _, s := range []string{"mailto:ops@example.com", "https://example.com/contact"} {
		if err := pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: s}).validatePush(); err != nil {
			t.Errorf("subject %q: %v", s, err)
		}
	}
	bad := []struct {
		name string
		cfg  serveConfig
		want string
	}{
		{"missing cred", pushCfg(&pushConfig{Subject: "mailto:a@b.c"}), "vapid-private-key-cred"},
		{"undeclared cred", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "nope", Subject: "mailto:a@b.c"}), "vapid-private-key-cred"},
		{"empty subject", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid"}), "subject"},
		{"empty mailto", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "mailto:"}), "subject"},
		{"mailto without @", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "mailto:ops"}), "subject"},
		{"https without host", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "https://"}), "subject"},
		{"https path only", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "https:///x"}), "subject"},
		{"ftp subject", pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "ftp://x"}), "subject"},
	}
	for _, tc := range bad {
		err := tc.cfg.validatePush()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want mention of %q", tc.name, err, tc.want)
		}
	}
}

func TestPushPreviewDefault(t *testing.T) {
	f, tr := false, true
	if !(&pushConfig{}).preview() || !(&pushConfig{Preview: &tr}).preview() {
		t.Error("nil/true preview should be true")
	}
	if (&pushConfig{Preview: &f}).preview() {
		t.Error("explicit false should be false")
	}
}

func TestPushCredentialIsDemanded(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  jam-vapid: { value: k }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "mailto:a@b.c"})
	cfg.CredentialsFile = credFile
	if got := cfg.demandedCredentials(); len(got) != 1 || got[0] != "jam-vapid" {
		t.Fatalf("demanded = %v", got)
	}
	specs, err := planCredentials(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := specs["jam-vapid"]; !ok {
		t.Fatalf("planCredentials missing jam-vapid: %v", specs)
	}
}

func TestPushKeygen(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdPush([]string{"keygen"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "private: ") || !strings.HasPrefix(lines[1], "public: ") {
		t.Fatalf("keygen printed %d lines, want private: and public:", len(lines))
	}
	priv, pub := strings.TrimPrefix(lines[0], "private: "), strings.TrimPrefix(lines[1], "public: ")
	got, err := vapidPublicKey(priv)
	if err != nil || got != pub {
		t.Fatalf("vapidPublicKey = %q, %v; want %q", got, err, pub)
	}
	raw, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil || len(raw) != 65 || raw[0] != 0x04 {
		t.Fatalf("public key shape: len=%d err=%v", len(raw), err)
	}
	if _, err := vapidPublicKey("!!notb64"); err == nil {
		t.Error("bad key should error")
	}
	if _, err := vapidPublicKey(base64.RawURLEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("short key should error")
	}
}

func TestPushUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}} {
		var out, errb bytes.Buffer
		if code := cmdPush(args, &out, &errb); code == 0 || !strings.Contains(errb.String(), "keygen") {
			t.Errorf("args %v: code %d stderr %q", args, code, errb.String())
		}
	}
}

// resolvePush resolves the VAPID key through the credential specs and derives
// the public key; no block is off; an unresolvable or invalid key refuses to
// start without echoing the key.
func TestResolvePush(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdPush([]string{"keygen"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	priv, pub := strings.TrimPrefix(lines[0], "private: "), strings.TrimPrefix(lines[1], "public: ")
	cfg := pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "mailto:ops@example.com"})

	k, err := resolvePush(&runner.Fake{}, cfg, map[string]secret.Spec{"jam-vapid": {Name: "jam-vapid", Literal: true, Value: priv}})
	if err != nil || k == nil || k.public != pub || k.private != priv {
		var gotPub string
		if k != nil {
			gotPub = k.public
		}
		t.Fatalf("resolvePush: public = %q, %v; want public %q", gotPub, err, pub) // never print the private key
	}
	// A command-sourced key resolves through the runner.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: priv + "\n"}}}
	if k, err := resolvePush(f, cfg, map[string]secret.Spec{"jam-vapid": {Name: "jam-vapid", Command: []string{"pass", "jam-vapid"}}}); err != nil || k.public != pub {
		t.Fatalf("command-sourced key: err = %v", err)
	}
	if k, err := resolvePush(&runner.Fake{}, serveConfig{}, nil); k != nil || err != nil {
		t.Fatalf("no push block: set = %v, err = %v", k != nil, err)
	}
	bad := "c2VjcmV0LW5vdC1hLWtleQ"
	_, err = resolvePush(&runner.Fake{}, cfg, map[string]secret.Spec{"jam-vapid": {Name: "jam-vapid", Literal: true, Value: bad}})
	if err == nil || strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "jam-vapid") {
		t.Fatalf("invalid key err = %v", err)
	}
	f = &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	_, err = resolvePush(f, cfg, map[string]secret.Spec{"jam-vapid": {Name: "jam-vapid", Command: []string{"pass", "jam-vapid"}}})
	if err == nil {
		t.Fatal("an unresolvable credential must refuse")
	}
}

func TestResolvePushNormalizesKey(t *testing.T) {
	var out, errb bytes.Buffer
	cmdPush([]string{"keygen"}, &out, &errb)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	priv := strings.TrimPrefix(lines[0], "private: ")
	cfg := pushCfg(&pushConfig{VAPIDPrivateKeyCred: "jam-vapid", Subject: "mailto:ops@example.com"})
	dirty := priv + "==\r\n"
	k, err := resolvePush(&runner.Fake{}, cfg, map[string]secret.Spec{"jam-vapid": {Name: "jam-vapid", Literal: true, Value: dirty}})
	if err != nil || k == nil || k.private != priv {
		t.Fatalf("normalized private mismatch, err = %v", err)
	}
}

func TestPushKeysRedacted(t *testing.T) {
	k := pushKeys{public: "PUBKEY", private: "SECRETKEY"}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "keys", k)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "keys", &k)
	got := fmt.Sprintf("%v|%+v|%s|%#v|%v", k, k, k, k, &k) + buf.String()
	if strings.Contains(got, "SECRETKEY") {
		t.Fatalf("private key leaked: %s", got)
	}
	if !strings.Contains(got, "PUBKEY") {
		t.Fatalf("public key missing: %s", got)
	}
}
