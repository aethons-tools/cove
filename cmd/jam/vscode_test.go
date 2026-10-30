package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testVSIX(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("extension/package.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`{"name":"jam","version":"` + version + `"}`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// vscodeJam is a Jam serving /me/vscode to the app token "apptok"; info and
// vsix are its answers (info "" = 404, an older Jam).
func vscodeJam(t *testing.T, info string, vsix []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer apptok" {
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/me/vscode" && info != "":
			_, _ = io.WriteString(w, info)
		case r.URL.Path == "/me/vscode/jam.vsix" && vsix != nil:
			_, _ = w.Write(vsix)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubCode makes code (and only the names in found) resolvable and records
// what ran.
func stubCode(t *testing.T, goos string, found ...string) *[][]string {
	t.Helper()
	ran := &[][]string{}
	oldLook, oldRun, oldGOOS := vscodeLookPath, vscodeRun, vscodeGOOS
	t.Cleanup(func() { vscodeLookPath, vscodeRun, vscodeGOOS = oldLook, oldRun, oldGOOS })
	vscodeGOOS = goos
	vscodeLookPath = func(name string) (string, error) {
		for _, f := range found {
			if f == name {
				return "/bin/" + filepath.Base(name), nil
			}
		}
		return "", errors.New("not found")
	}
	vscodeRun = func(name string, args []string, _, _ io.Writer) error {
		*ran = append(*ran, append([]string{name}, args...))
		return nil
	}
	return ran
}

func runVSCode(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"vscode"}, args...), func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestVSCodeInstall(t *testing.T) {
	sshTestHome(t)
	srv := vscodeJam(t, `{"available":true,"version":"0.123.0"}`, testVSIX(t, "0.123.0"))
	writeAppTokenFile(t, appTokenJSON(t, "apptok", srv.URL))
	ran := stubCode(t, "linux", "code")

	code, out, errOut := runVSCode(t, "install", "--admin-url", srv.URL)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	file := filepath.Join(vsixDir(), "jam-vscode_0.123.0.vsix")
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, testVSIX(t, "0.123.0")) {
		t.Fatalf("saved .vsix: %v", err)
	}
	want := []string{"/bin/code", "--install-extension", file, "--force"}
	if len(*ran) != 1 || strings.Join((*ran)[0], " ") != strings.Join(want, " ") {
		t.Fatalf("ran %q, want %q", *ran, want)
	}
	if !strings.Contains(out, "Installed the Jam extension 0.123.0") {
		t.Errorf("stdout: %q", out)
	}
}

func TestVSCodeInstallCodeFlagAndMacFallback(t *testing.T) {
	sshTestHome(t)
	srv := vscodeJam(t, `{"available":true,"version":"0.1.0"}`, testVSIX(t, "0.1.0"))
	writeAppTokenFile(t, appTokenJSON(t, "apptok", srv.URL))

	ran := stubCode(t, "linux", "code", "code-insiders")
	if code, _, errOut := runVSCode(t, "install", "--admin-url", srv.URL, "--code", "code-insiders"); code != 0 || (*ran)[0][0] != "/bin/code-insiders" {
		t.Fatalf("--code: exit %d, ran %q: %s", code, *ran, errOut)
	}

	ran = stubCode(t, "darwin", macCodeCLI)
	if code, _, errOut := runVSCode(t, "install", "--admin-url", srv.URL); code != 0 || len(*ran) != 1 {
		t.Fatalf("mac fallback: exit %d, ran %q: %s", code, *ran, errOut)
	}
	ran = stubCode(t, "linux", macCodeCLI) // the bundle path is only tried on macOS
	if code, _, _ := runVSCode(t, "install", "--admin-url", srv.URL); code != 1 || len(*ran) != 0 {
		t.Fatalf("linux, no code: exit %d, ran %q", code, *ran)
	}
}

func TestVSCodeInstallNoCodeKeepsTheFile(t *testing.T) {
	sshTestHome(t)
	srv := vscodeJam(t, `{"available":true,"version":"0.2.0"}`, testVSIX(t, "0.2.0"))
	writeAppTokenFile(t, appTokenJSON(t, "apptok", srv.URL))
	stubCode(t, "linux")
	code, _, errOut := runVSCode(t, "install", "--admin-url", srv.URL)
	file := filepath.Join(vsixDir(), "jam-vscode_0.2.0.vsix")
	if code != 1 || !strings.Contains(errOut, file) || !strings.Contains(errOut, "Install from VSIX") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the .vsix wasn't kept: %v", err)
	}
}

func TestVSCodeInstallRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		info  string
		vsix  []byte
		token string
		want  string
	}{
		"not built in":     {`{"available":false}`, nil, "apptok", "built without the VS Code extension"},
		"an older Jam":     {"", nil, "apptok", "predates it"},
		"a revoked token":  {`{"available":true,"version":"0.1.0"}`, nil, "old", "run Jam: Sign In in VS Code"},
		"version mismatch": {`{"available":true,"version":"0.2.0"}`, []byte("zip?"), "apptok", "unreadable .vsix"},
	} {
		t.Run(name, func(t *testing.T) {
			sshTestHome(t)
			srv := vscodeJam(t, tc.info, tc.vsix)
			writeAppTokenFile(t, appTokenJSON(t, tc.token, srv.URL))
			ran := stubCode(t, "linux", "code")
			code, _, errOut := runVSCode(t, "install", "--admin-url", srv.URL)
			if code != 1 || !strings.Contains(errOut, tc.want) || len(*ran) != 0 {
				t.Errorf("exit %d, ran %q: %s", code, *ran, errOut)
			}
		})
	}

	t.Run("a different version than advertised", func(t *testing.T) {
		sshTestHome(t)
		srv := vscodeJam(t, `{"available":true,"version":"0.2.0"}`, testVSIX(t, "0.3.0"))
		writeAppTokenFile(t, appTokenJSON(t, "apptok", srv.URL))
		stubCode(t, "linux", "code")
		if code, _, errOut := runVSCode(t, "install", "--admin-url", srv.URL); code != 1 || !strings.Contains(errOut, "not the 0.2.0 it advertised") {
			t.Errorf("exit %d: %s", code, errOut)
		}
	})
}

func TestVSCodeUsage(t *testing.T) {
	sshTestHome(t)
	for _, args := range [][]string{{}, {"upgrade"}, {"install", "extra"}, {"install", "--participant", "--operator"}} {
		if code, _, _ := runVSCode(t, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
}
