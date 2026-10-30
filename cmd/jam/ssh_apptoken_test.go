package main

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

const extTestJam = "https://jam.example.com"

func writeAppTokenFile(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appTokenPath(defaultApp), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appTokenJSON(t *testing.T, token, jamURL string) string {
	t.Helper()
	b, err := json.Marshal(appTokenFile{Token: token, JamURL: jamURL})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cacheParticipantIDToken(t *testing.T) {
	t.Helper()
	if err := saveTokenAt(participantTokenPath(defaultApp), cachedToken{IDToken: "idtok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func resolveWith(t *testing.T, args ...string) sshTarget {
	t.Helper()
	fs := flag.NewFlagSet("ssh-proxy", flag.ContinueOnError)
	f := addSSHFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	tg, _, err := f.resolve("ses_a", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestResolvePrefersAppTokenFile(t *testing.T) {
	sshTestHome(t)
	cacheParticipantIDToken(t)
	writeAppTokenFile(t, appTokenJSON(t, "apptok", extTestJam+"/"))
	got := resolveWith(t, "--admin-url", extTestJam, "--participant")
	if got.token != "apptok" || !got.participant || !got.appToken {
		t.Fatalf("got token %q participant=%v appToken=%v, want the app token", got.token, got.participant, got.appToken)
	}
	// Without --participant too: the app token is a participant credential.
	if got := resolveWith(t, "--admin-url", extTestJam); got.token != "apptok" || !got.participant || !got.appToken {
		t.Fatalf("unflagged: token %q participant=%v appToken=%v", got.token, got.participant, got.appToken)
	}
}

// A token file naming another Jam is never sent to this one (Review Focus 2).
func TestResolveAppTokenOtherJamIgnored(t *testing.T) {
	sshTestHome(t)
	cacheParticipantIDToken(t)
	writeAppTokenFile(t, appTokenJSON(t, "apptok", "https://other.example.com"))
	got := resolveWith(t, "--admin-url", extTestJam, "--participant")
	if got.token != "idtok" || got.appToken {
		t.Fatalf("token %q appToken=%v, want the participant ID token", got.token, got.appToken)
	}
}

func TestResolveAppTokenOperatorFlagIgnores(t *testing.T) {
	sshTestHome(t)
	t.Setenv("JAM_ADMIN_TOKEN", "optok")
	writeAppTokenFile(t, appTokenJSON(t, "apptok", extTestJam))
	got := resolveWith(t, "--admin-url", extTestJam, "--operator")
	if got.token != "optok" || got.participant || got.appToken {
		t.Fatalf("token %q participant=%v appToken=%v, want the operator token", got.token, got.participant, got.appToken)
	}
}

func TestResolveAppTokenMalformedIgnored(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"bad json", "{not json"},
		{"empty token", `{"token":"","jam_url":"` + extTestJam + `"}`},
		{"missing file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sshTestHome(t)
			cacheParticipantIDToken(t)
			if tc.body != "" {
				writeAppTokenFile(t, tc.body)
			}
			got := resolveWith(t, "--admin-url", extTestJam, "--participant")
			if got.token != "idtok" || got.appToken {
				t.Fatalf("token %q appToken=%v, want the participant ID token", got.token, got.appToken)
			}
		})
	}
}

func TestSSHErrMessageAppToken401(t *testing.T) {
	got := sshErrMessage(&adminclient.StatusError{Code: http.StatusUnauthorized}, sshTarget{app: defaultApp, studio: "ses_a", participant: true, appToken: true})
	if want := "your VS Code sign-in to Jam is missing or was revoked; run Jam: Sign In in VS Code"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

const extStudio = "ses_0abcdefghjkmnpqrstvwxyz012"

func extFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "vscode", "test", "fixtures", "ssh-block.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The VS Code extension writes jam ssh-config's block plus one marker line;
// the fixture is shared with the extension's own test, pinning both.
func TestExtensionSSHBlockGolden(t *testing.T) {
	stubSSHSelf(t, "/usr/local/bin/jam", nil)
	t.Setenv("XDG_CONFIG_HOME", "/home/u/.config")
	block := sshTarget{app: defaultApp, studio: extStudio, adminURLFlag: extTestJam, participant: true}.sshConfigBlock()
	first, rest, _ := strings.Cut(block, "\n")
	got := first + "\n# managed by the Jam VS Code extension (aethons.jam)\n" + rest
	if want := extFixture(t); got != want {
		t.Fatalf("block differs from the fixture:\n--- got\n%s--- want\n%s", got, want)
	}
}

// jam ssh-config --prune and --write treat an extension block as their own.
func TestExtensionSSHBlockPrunable(t *testing.T) {
	sshTestHome(t)
	fx := extFixture(t)
	es := sshConfigEntries(fx)
	if len(es) != 1 || es[0].studio != extStudio || es[0].proxy != "/usr/local/bin/jam ssh-proxy --admin-url '"+extTestJam+"' --participant "+extStudio {
		t.Fatalf("entries = %+v", es)
	}
	cacheParticipantIDToken(t)
	tg, err := pruneTarget(es[0], io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if tg.adminURLFlag != extTestJam || !tg.participant {
		t.Fatalf("pruneTarget = %+v", tg)
	}
	if got := removeSSHConfigBlock(fx, extStudio); got != "" {
		t.Fatalf("remove left %q", got)
	}
	repl := "# >>> jam " + extStudio + " >>>\nHost x\n# <<< jam " + extStudio + " <<<\n"
	got, err := spliceSSHConfigBlock(fx, extStudio, repl)
	if err != nil || got != repl {
		t.Fatalf("splice = %q, %v", got, err)
	}
}
