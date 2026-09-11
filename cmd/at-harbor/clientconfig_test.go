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
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// settings.yml
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir(), "settings.yml"),
		[]byte("admin-url: http://harbor.local:8081\nbase-url: https://harbor.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadSettings()
	if s.AdminURL != "http://harbor.local:8081" || s.BaseURL != "https://harbor.local" {
		t.Fatalf("settings = %+v", s)
	}

	// token round-trip + 0600
	tok := cachedToken{AccessToken: "AT", Sub: "auth0|op", Expiry: time.Now().Add(time.Hour)}
	if err := saveToken(tok); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tokenPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, err=%v (want 0600)", info.Mode().Perm(), err)
	}
	got, ok := loadToken()
	if !ok || got.AccessToken != "AT" || got.Sub != "auth0|op" {
		t.Fatalf("loadToken = %+v ok=%v", got, ok)
	}

	// expired cache loads as absent
	if err := saveToken(cachedToken{AccessToken: "OLD", Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadToken(); ok {
		t.Fatal("expired token must load as absent")
	}

	// clear
	if err := clearToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath()); !os.IsNotExist(err) {
		t.Fatal("token file should be gone after clearToken")
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
