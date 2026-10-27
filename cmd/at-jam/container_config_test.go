package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loadContainerExample runs the example through the same load + validate path
// cmdServe uses (no listeners, no store). credentials-file is repointed at the
// example's sibling, since /etc/jam/credentials.yml only exists in the image.
func loadContainerExample(t *testing.T, mutate func(string) string) (serveConfig, error) {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("../../dev/jam.container.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "jam.yml")
	if err := os.WriteFile(p, []byte(mutate(string(raw))), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _, err := loadServeConfigs([]string{p}, os.ReadFile)
	if err != nil {
		return serveConfig{}, err
	}
	cfg, err := parseServeConfig(data)
	if err != nil {
		return cfg, err
	}
	if unknown := unknownServeKeys(data); len(unknown) > 0 {
		return cfg, fmt.Errorf("unknown serve config keys: %v", unknown)
	}
	for _, v := range []func() error{
		cfg.validateAdminExposure, cfg.validateLauncher, cfg.validateRequisitioner,
		cfg.validateStorage, cfg.validateStorePostgres, cfg.validateDiscord,
		cfg.validatePush, cfg.validateDisplayName, cfg.validateWake, cfg.validateMetrics,
		cfg.validateSSH, cfg.validateKits, cfg.validateBlobs, cfg.validatePreviews,
		cfg.validatePool, cfg.validateCredentials, cfg.validateSessionEvents,
	} {
		if err := v(); err != nil {
			return cfg, err
		}
	}
	cfg.CredentialsFile, _ = filepath.Abs("../../dev/credentials.container.yml.example")
	if _, err := planCredentials(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func TestContainerConfigExampleValidates(t *testing.T) {
	cfg, err := loadContainerExample(t, func(s string) string { return s })
	if err != nil {
		t.Fatalf("example config should validate: %v", err)
	}
	if !cfg.Runtime.StudioManagers {
		t.Error("runtime.studio-managers should be true")
	}
	if cfg.Runtime.Launcher != nil {
		t.Error("runtime.launcher should be unset")
	}
	if cfg.DevIdentity != nil {
		t.Error("dev-identity should be unset")
	}
	if cfg.Listen != ":8443" || cfg.AdminListen != ":8081" {
		t.Errorf("listen/admin-listen = %q/%q", cfg.Listen, cfg.AdminListen)
	}
	if pg := cfg.StorePostgres; pg == nil || pg.Host != "postgres" || pg.Port != 5432 {
		t.Errorf("store-postgres = %+v", pg)
	}
	if cfg.Blobs == nil || cfg.Blobs.Endpoint != "http://garage:3900" {
		t.Errorf("blobs = %+v", cfg.Blobs)
	}
	if cfg.CredentialsFile == "" {
		t.Error("credentials-file unset")
	}
}

func TestContainerConfigExampleNeedsOperatorAuth(t *testing.T) {
	re := regexp.MustCompile(`(?s)\noperator-auth:.*?\n\n`)
	_, err := loadContainerExample(t, func(s string) string {
		out := re.ReplaceAllString(s, "\n")
		if out == s {
			t.Fatal("operator-auth block not found in example")
		}
		return out
	})
	if err == nil || !strings.Contains(err.Error(), "operator-auth.oidc") {
		t.Fatalf("want admin-exposure error naming operator-auth.oidc, got %v", err)
	}
}
