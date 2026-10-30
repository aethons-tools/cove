// Package vscodeext holds the Jam VS Code extension (.vsix) this jam was
// released with, and serves it to signed-in users: `jam vscode install`
// downloads it, and the extension checks it for updates
// (docs/usage/jam/vscode.md).
package vscodeext

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// files holds vsix/jam.vsix, staged by scripts/stage-vscode.sh before a
// release build; only a placeholder is committed, so a plain `go build` has
// none and the routes answer available: false.
//
//go:embed all:vsix
var files embed.FS

// Bundle is a .vsix and the extension version it packages ("" data: none).
type Bundle struct {
	data    []byte
	version string
}

// Available reports whether the bundle holds a .vsix.
func (b Bundle) Available() bool { return len(b.data) > 0 }

// Version is the packaged extension's version, e.g. "0.123.0".
func (b Bundle) Version() string { return b.version }

// Parse reads a .vsix: its extension version comes from the package.json
// inside it, so it can't drift from what VS Code installs.
func Parse(data []byte) (Bundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Bundle{}, fmt.Errorf("vsix: %w", err)
	}
	f, err := zr.Open("extension/package.json")
	if err != nil {
		return Bundle{}, fmt.Errorf("vsix: %w", err)
	}
	defer f.Close()
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&pkg); err != nil {
		return Bundle{}, fmt.Errorf("vsix: package.json: %w", err)
	}
	if _, ok := parseSemver(pkg.Version); !ok {
		return Bundle{}, fmt.Errorf("vsix: package.json version %q is not x.y.z", pkg.Version)
	}
	return Bundle{data: data, version: pkg.Version}, nil
}

// Embedded is the bundle built into this binary (empty when none was
// staged). An unreadable staged .vsix panics: a release must not ship one.
var Embedded = sync.OnceValue(func() Bundle {
	data, err := fs.ReadFile(files, "vsix/jam.vsix")
	if errors.Is(err, fs.ErrNotExist) {
		return Bundle{}
	}
	if err != nil {
		panic(err)
	}
	b, err := Parse(data)
	if err != nil {
		panic(err)
	}
	return b
})

// parseSemver reads "x.y.z" (digits only).
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || strconv.Itoa(n) != p {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Info is GET <prefix>/vscode's body.
type Info struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

// Routes serves GET <prefix>/vscode (Info) and GET <prefix>/vscode/jam.vsix
// (the file; 404 when the bundle is empty). authorize, when set, gates both:
// a non-nil error is a 401. Mount it at both paths.
func Routes(prefix string, b Bundle, authorize func(*http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if authorize != nil {
			if err := authorize(r); err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="jam"`)
				http.Error(w, "sign in to Jam first (jam login --participant, or the VS Code extension's sign-in)", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case prefix + "/vscode":
			w.Header().Set("Content-Type", "application/json")
			info := Info{Available: b.Available()}
			if info.Available {
				info.Version = b.version
			}
			_ = json.NewEncoder(w).Encode(info)
		case prefix + "/vscode/jam.vsix":
			if !b.Available() {
				http.Error(w, "this Jam was built without the VS Code extension", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="jam-vscode_`+b.version+`.vsix"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(b.data)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b.data)
			}
		default:
			http.NotFound(w, r)
		}
	})
}
