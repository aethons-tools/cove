package jam

import (
	"strings"
	"testing"
)

func TestRedactEnv(t *testing.T) {
	for _, c := range []struct{ k, v, want string }{
		{"AT_JAM_IDENTITY_TOKEN", "tok-live-123", MarkIdentityToken},
		{"AT_HARBOR_IDENTITY_TOKEN", "{token}", MarkIdentityToken},
		{"ANTHROPIC_API_KEY", "{token}", MarkIdentityToken},
		{"GH_TOKEN", "Bearer {token}", "Bearer " + MarkIdentityToken},
		{"X_AUTH", "token {token}", "token " + MarkIdentityToken},
		{"ANTHROPIC_BASE_URL", "{base}/anthropic", "{base}/anthropic"},
		{"GH_HOST", "{host}", "{host}"},
		{"PATH", "/usr/local/bin:/usr/bin", "/usr/local/bin:/usr/bin"},
		{"GOPROXY", "https://proxy.golang.org,direct", "https://proxy.golang.org,direct"},
		{"GITHUB_TOKEN", "ghp_live0123456789", MarkMasked},
		{"db_password", "hunter2", MarkMasked},
		{"MY_SECRET_URL", "{base}/x", MarkMasked}, // literal text beside a placeholder
		{"Api-Key", "{base}sk-live", MarkMasked},
		{"KEY_WITH_UNKNOWN", "{sk-live}", MarkMasked}, // only known placeholders count
		{"credential_helper", "store", MarkMasked},
		{"PASSWD", "", MarkMasked},
		// ruling: userinfo is stripped from any value containing "://"
		{"REPO", "https://u:p@github.com/x/y", "https://github.com/x/y"},
		{"REPO", "https://github.com/x/y?token=abc#frag", "https://github.com/x/y?«masked»"},
		{"NOTE", "clone https://u:p@h/x now", "clone https://h/x now"},
		{"REPO_KEY", "https://u:p@h/x", MarkMasked},
		// fix round 1
		{"GOPROXY", "https://a,https://u:pw@corp/x", "https://a,https://corp/x"},
		{"GOPROXY", "https://a;https://u:pw@corp/x,direct", "https://a;https://corp/x,direct"},
		{"GOPROXY", "https://u:pw@a/x,https://v:qq@b/y", "https://a/x,https://b/y"},
		{"NOTE", "see https://h/x?token=abc#f and more", "see https://h/x?«masked» and more"},
		{"NOTE", "https://h/x#f", "https://h/x?«masked»"},
		{"REPO", "https://u:p@ss@h/x", "https://h/x"},
		{"REPO", "https://u:p@ss", "https://ss"},
		{"ANTHROPIC_CUSTOM_HEADERS", "Authorization: Bearer sk-live", MarkMasked},
		{"HDR", "authorization : x", MarkMasked},
		{"AUTHORIZATION", "Bearer x", MarkMasked},
		{"HDR", "Bearer sk-live", MarkMasked},
		{"HDR", "Basic dXNlcjpwdw==", MarkMasked},
		{"HDR", "Bearer {token}", "Bearer " + MarkIdentityToken},
		{"npm_config__auth", "dXNlcjpwdw==", MarkMasked},
		{"_auth", "dXNlcjpwdw==", MarkMasked},
		{"auth", "x", MarkMasked},
		{"Authorization", "x", MarkMasked},
	} {
		if got := RedactEnv(c.k, c.v); got != c.want {
			t.Errorf("RedactEnv(%q, %q) = %q, want %q", c.k, c.v, got, c.want)
		}
	}
}

func TestRedactJSON(t *testing.T) {
	got, ok := RedactJSON([]byte(`{"env":{"GH_TOKEN":"ghp_SETTINGS","EDITOR":"vi"},"apiKeyHelper":"echo sk-1","model":"opus","hooks":[{"command":"<x>"}],"secretNum":12345,"tokenFlag":true,"nested":{"password":{"a":"zzz-pw"}}}`))
	if !ok {
		t.Fatal("valid JSON refused")
	}
	for _, bad := range []string{"ghp_SETTINGS", "sk-1", "12345", "true", "zzz-pw"} {
		if strings.Contains(got, bad) {
			t.Errorf("secret %q leaked: %s", bad, got)
		}
	}
	for _, want := range []string{`"EDITOR": "vi"`, `"model": "opus"`, `"<x>"`, MarkMasked} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	hd, _ := RedactJSON([]byte(`{"mcpServers":{"x":{"headers":{"Authorization":"Bearer sk-hdr","Accept":"json"}}}}`))
	if strings.Contains(hd, "sk-hdr") || !strings.Contains(hd, `"Accept": "json"`) {
		t.Errorf("headers: %s", hd)
	}
	num, _ := RedactJSON([]byte(`{"big":12345678901234567890,"f":1.50}`))
	if !strings.Contains(num, "12345678901234567890") || !strings.Contains(num, "1.50") {
		t.Errorf("numbers not exact: %s", num)
	}
	if _, ok := RedactJSON([]byte(`{"a":1} {"b":2}`)); ok {
		t.Fatal("trailing data accepted")
	}
	if _, ok := RedactJSON([]byte("{nope")); ok {
		t.Fatal("invalid JSON accepted")
	}
}

func TestSafeText(t *testing.T) {
	for in, want := range map[string]string{
		"ok": "ok", "a\nb": MarkInvalid, "a\u202eb": MarkInvalid, "a\u200bb": MarkInvalid, "\xff": MarkInvalid, strings.Repeat("a", 257): MarkInvalid, strings.Repeat("a", 256): strings.Repeat("a", 256),
	} {
		if got := SafeText(in); got != want {
			t.Errorf("SafeText(%q) = %q", in, got)
		}
	}
}

func TestRedactLabel(t *testing.T) {
	for _, c := range []struct{ k, v, wantK, wantV string }{
		{"org.opencontainers.image.source", "https://u:p@github.com/x/y", "org.opencontainers.image.source", "https://github.com/x/y"},
		{"org.opencontainers.image.source", "https://github.com/x/y", "org.opencontainers.image.source", "https://github.com/x/y"},
		{"cove.kit", "my-app@v1", "cove.kit", "my-app@v1"},
		{"evil", "a\nb", "evil", MarkInvalid},
		{"bad\x00key", "v", MarkInvalid, MarkInvalid},
		{"build.token", "abc", "build.token", MarkMasked},
		{"url", "http://[::1", "url", MarkInvalid},
		{"urls", "https://a,https://u:pw@corp/x", "urls", "https://a,https://corp/x"},
		{"desc", "clone https://u:p@h/x?t=1 please", "desc", "clone https://h/x?«masked» please"},
		{"u", "https://u:p@h/x?t=1", "u", "https://h/x?«masked»"},
	} {
		k, v := RedactLabel(c.k, c.v)
		if k != c.wantK || v != c.wantV {
			t.Errorf("RedactLabel(%q, %q) = %q, %q; want %q, %q", c.k, c.v, k, v, c.wantK, c.wantV)
		}
	}
}

func TestStripUserinfo(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@h/x?q=1#f": "https://h/x?«masked»",
		"https://h/x":           "https://h/x",
	} {
		if got, ok := stripUserinfo(in); !ok || got != want {
			t.Errorf("stripUserinfo(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := stripUserinfo("http://[::1"); ok {
		t.Error("unparsable accepted")
	}
}
