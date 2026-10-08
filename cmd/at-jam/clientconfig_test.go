package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSettingsAndTokenCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// settings.yml is app-keyed; saveSettings upserts one app, preserving others.
	if err := saveSettings("default", clientSettings{AdminURL: "http://127.0.0.1:8081", BaseURL: "https://h.local"}); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings("dev", clientSettings{AdminURL: "http://localhost:9091"}); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings("default"); s.AdminURL != "http://127.0.0.1:8081" || s.BaseURL != "https://h.local" {
		t.Fatalf("default settings = %+v", s)
	}
	if s := loadSettings("dev"); s.AdminURL != "http://localhost:9091" {
		t.Fatalf("dev settings = %+v", s)
	}
	// upserting dev must not clobber default's block
	if s := loadSettings("default"); s.AdminURL == "" {
		t.Fatal("saveSettings clobbered another app's block")
	}

	// per-app token files, mode 0600, named {app}-admin-token.json
	tok := cachedToken{AccessToken: "AT", Sub: "auth0|op", Expiry: time.Now().Add(time.Hour), AdminURL: "http://127.0.0.1:8081"}
	if err := saveToken("default", tok); err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(tokenPath("default")); base != "default-admin-token.json" {
		t.Fatalf("token filename = %s", base)
	}
	info, err := os.Stat(tokenPath("default"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, err=%v (want 0600)", info.Mode().Perm(), err)
	}
	if got, ok := loadToken("default"); !ok || got.AccessToken != "AT" || got.Sub != "auth0|op" {
		t.Fatalf("loadToken(default) = %+v ok=%v", got, ok)
	}
	// a different app has no token
	if _, ok := loadToken("dev"); ok {
		t.Fatal("dev app must not see default's token")
	}

	// expired cache loads as absent
	if err := saveToken("default", cachedToken{AccessToken: "OLD", Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadToken("default"); ok {
		t.Fatal("expired token must load as absent")
	}

	// clear
	if err := clearToken("default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath("default")); !os.IsNotExist(err) {
		t.Fatal("token file should be gone after clearToken")
	}
}

func TestValidateApp(t *testing.T) {
	for _, ok := range []string{"default", "dev", "dev-app", "prod.1", "a_b"} {
		if err := validateApp(ok); err != nil {
			t.Errorf("validateApp(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "../etc", "a b", "a\\b"} {
		if err := validateApp(bad); err == nil {
			t.Errorf("validateApp(%q) = nil, want error", bad)
		}
	}
}

func TestParseJWTClaims(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	exp := time.Now().Add(time.Hour).Unix()
	token := b64(`{"alg":"RS256"}`) + "." + b64(`{"sub":"auth0|abc","exp":`+strconv.FormatInt(exp, 10)+`}`) + ".sig"
	sub, gotExp, err := parseJWTClaims(token)
	if err != nil || sub != "auth0|abc" || gotExp.Unix() != exp {
		t.Fatalf("parseJWTClaims: sub=%q exp=%v err=%v", sub, gotExp, err)
	}
}
