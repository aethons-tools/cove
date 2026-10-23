package jam

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fp(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}

func TestRedactHeaderValue(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@x.com:SECRET-TOKEN"))
	for _, c := range []struct {
		name, value, want string
	}{
		{"Authorization", basic, `Basic user="me@x.com" pass=<redacted len=12 sha256:` + fp("SECRET-TOKEN") + `>`},
		{"Authorization", "Bearer TOK123", "Bearer <redacted len=6 sha256:" + fp("TOK123") + ">"},
		{"Authorization", "token TOK123", "token <redacted len=6 sha256:" + fp("TOK123") + ">"},
		{"Authorization", "Basic !!!notbase64", "<redacted len=18 sha256:" + fp("Basic !!!notbase64") + ">"},
		{"X-Api-Key", "K", "<redacted len=1 sha256:" + fp("K") + ">"},
	} {
		if got := redactHeaderValue(c.value); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

func TestSensitiveHeader(t *testing.T) {
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "Private-Token",
		"X-Goog-Api-Key", "X-Auth-Token", "X-Client-Secret", "X-Custom-Ident"} {
		if !sensitiveHeader(h, "X-Custom-Ident", "") {
			t.Errorf("%s not treated as sensitive", h)
		}
	}
	for _, h := range []string{"Content-Type", "User-Agent", "Accept", "Git-Protocol", "Anthropic-Beta"} {
		if sensitiveHeader(h, "Authorization", "Authorization") {
			t.Errorf("%s treated as sensitive", h)
		}
	}
}

func TestValidateHeaderLogging(t *testing.T) {
	for _, d := range []Destination{{}, {LogHeadersIn: HeaderLogNone, LogHeadersOut: HeaderLogNone},
		{LogHeadersIn: HeaderLogExamined, LogHeadersOut: HeaderLogChanged}, {LogHeadersIn: HeaderLogAll, LogHeadersOut: HeaderLogAll}} {
		if err := d.ValidateHeaderLogging(); err != nil {
			t.Errorf("%+v refused: %v", d, err)
		}
	}
	for _, d := range []Destination{{LogHeadersIn: "changed"}, {LogHeadersOut: "examined"}, {LogHeadersIn: "raw"}, {LogHeadersOut: "changed-raw"}} {
		if err := d.ValidateHeaderLogging(); err == nil {
			t.Errorf("%+v accepted", d)
		}
	}
}

// brokerWithLogging is newTestBroker with the git destination's header logging set.
func brokerWithLogging(t *testing.T, upstream string, in, out HeaderLog) (*Broker, *strings.Builder, string) {
	t.Helper()
	b, _, tok := newTestBroker(t, "http://unused", upstream)
	d, _ := findDestination(b.store, "git")
	d.LogHeadersIn, d.LogHeadersOut = in, out
	if err := b.store.AddDestination(d); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	b.log = testLoggerTo(&logs)
	return b, &logs, tok
}

// examined + changed: the identity the broker read and what it set upstream,
// structurally — never the token or the credential.
func TestBrokerLogsHeadersRedacted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	b, logs, tok := brokerWithLogging(t, up.URL, HeaderLogExamined, HeaderLogChanged)
	req := httptest.NewRequest("GET", "/git/acme/api/info/refs", nil)
	req.SetBasicAuth("x-access-token", tok)
	req.Header.Set("User-Agent", "git/2.45")
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	out := logs.String()
	for _, want := range []string{
		"broker headers in", "broker headers out", "destination=git", "path=/git/acme/api/info/refs",
		`user=\"x-access-token\"`, "sha256:" + fp(tok), "sha256:" + fp("REAL-PAT"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, tok) || strings.Contains(out, "REAL-PAT") {
		t.Fatalf("secret in log:\n%s", out)
	}
	if strings.Contains(out, "git/2.45") {
		t.Errorf("examined/changed logged an unexamined, unchanged header:\n%s", out)
	}
}

// all: every header, sensitive ones redacted; a missing identity is logged too.
func TestBrokerLogsAllHeadersAndMissingIdentity(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	b, logs, tok := brokerWithLogging(t, up.URL, HeaderLogAll, HeaderLogAll)
	req := httptest.NewRequest("GET", "/git/acme/api/info/refs", nil)
	req.SetBasicAuth("x-access-token", tok)
	req.Header.Set("User-Agent", "git/2.45")
	req.Header.Set("Cookie", "session=COOKIE-SECRET")
	b.ServeHTTP(httptest.NewRecorder(), req)
	out := logs.String()
	if !strings.Contains(out, "git/2.45") || strings.Contains(out, "COOKIE-SECRET") || strings.Contains(out, tok) || strings.Contains(out, "REAL-PAT") {
		t.Fatalf("all-headers log wrong:\n%s", out)
	}

	logs.Reset()
	b.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/git/acme/api/info/refs", nil))
	if out := logs.String(); !strings.Contains(out, "broker headers in") || !strings.Contains(out, "missing") {
		t.Fatalf("missing identity not logged:\n%s", out)
	}
}

// Off by default: nothing logged.
func TestBrokerHeaderLoggingOffByDefault(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	b, logs, tok := brokerWithLogging(t, up.URL, "", "")
	req := httptest.NewRequest("GET", "/git/acme/api/info/refs", nil)
	req.SetBasicAuth("x-access-token", tok)
	b.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(logs.String(), "broker headers") {
		t.Fatalf("logged with logging off:\n%s", logs.String())
	}
}

// At most headerLogBurst lines per destination and direction per window.
func TestHeaderLogRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newHeaderLogLimiter(func() time.Time { return now })
	n := 0
	for range headerLogBurst + 5 {
		if l.allow("git", "in") {
			n++
		}
	}
	if n != headerLogBurst {
		t.Fatalf("allowed %d, want %d", n, headerLogBurst)
	}
	if !l.allow("git", "out") || !l.allow("other", "in") {
		t.Fatal("limit not per destination and direction")
	}
	now = now.Add(headerLogWindow)
	if !l.allow("git", "in") {
		t.Fatal("window did not reset")
	}
}
