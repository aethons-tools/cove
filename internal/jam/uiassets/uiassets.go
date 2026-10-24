// Package uiassets holds the static assets the Jam UIs share — jam.css (the
// design tokens and typography base) and htmx — so the admin UI (/app/admin)
// and the user Inbox (/app/user) read as one product from one source. The
// browser app serves them, ungated, under /app/static/.
package uiassets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static/jam.css static/htmx.min.js static/push.js static/sounds.js static/brand/*.svg static/icons/*.png
var files embed.FS

// whiteboardFiles is the whiteboard editor bundle, built into whiteboard/ by
// `just whiteboard` (scripts/whiteboard.sh); only a placeholder is committed,
// so a plain `go build` has none.
//
//go:embed all:whiteboard
var whiteboardFiles embed.FS

var static = func() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// version is a content hash of jam.css, so a changed stylesheet is a new URL
// and browsers never serve a stale one.
var version = func() string {
	b, err := fs.ReadFile(static, "jam.css")
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}()

// iconVersions maps each embedded icon name to a content hash of its file.
var iconVersions = func() map[string]string {
	entries, err := fs.ReadDir(static, "icons")
	if err != nil {
		panic(err)
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		b, err := fs.ReadFile(static, "icons/"+e.Name())
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(b)
		m[e.Name()] = hex.EncodeToString(sum[:6])
	}
	return m
}()

// scriptVersions maps each versioned script (served under its own name) to
// a content hash of its file.
var scriptVersions = func() map[string]string {
	m := map[string]string{}
	for _, name := range []string{"push.js", "sounds.js"} {
		b, err := fs.ReadFile(static, name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(b)
		m[name] = hex.EncodeToString(sum[:6])
	}
	return m
}()

// Handler serves the shared assets at prefix (e.g. "/app/static/"); register it
// on the "GET <prefix>" pattern.
func Handler(prefix string) http.Handler {
	shared := http.StripPrefix(prefix, http.FileServer(http.FS(static)))
	board := http.StripPrefix(prefix+"whiteboard/", http.FileServer(http.FS(whiteboard)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, prefix+"whiteboard/"); ok {
			// Files only: no dotfiles, no directory listings.
			if !WhiteboardBuilt() || rest == "" || strings.HasSuffix(rest, "/") || strings.HasPrefix(path.Base(rest), ".") {
				http.NotFound(w, r)
				return
			}
			// Chunks and assets are content-hashed and the entry is
			// versioned (?v=): cache them for good; fonts keep their names
			// across builds, so for a day.
			if strings.HasPrefix(rest, "fonts/") {
				w.Header().Set("Cache-Control", "public, max-age=86400")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			board.ServeHTTP(w, r)
			return
		}
		shared.ServeHTTP(w, r)
	})
}

var whiteboard = func() fs.FS {
	sub, err := fs.Sub(whiteboardFiles, "whiteboard")
	if err != nil {
		panic(err)
	}
	return sub
}()

// whiteboardVersion hashes the bundle's entry script ("" when not built).
var whiteboardVersion = func() string {
	b, err := fs.ReadFile(whiteboard, "whiteboard.js")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}()

// WhiteboardBuilt reports whether this binary carries the whiteboard editor.
func WhiteboardBuilt() bool { return whiteboardVersion != "" }

// WhiteboardScript is the editor's entry module URL under prefix, versioned
// (callers check WhiteboardBuilt first); WhiteboardStyles its stylesheet; WhiteboardBase the
// directory Excalidraw loads its fonts and chunks from.
func WhiteboardScript(prefix string) string {
	return prefix + "whiteboard/whiteboard.js?v=" + whiteboardVersion
}

func WhiteboardStyles(prefix string) string {
	return prefix + "whiteboard/whiteboard.css?v=" + whiteboardVersion
}

func WhiteboardBase(prefix string) string { return prefix + "whiteboard/" }

// StylesheetHref is jam.css's URL under prefix, versioned by content.
func StylesheetHref(prefix string) string {
	return prefix + "jam.css?v=" + version
}

// IconHref is an icon's URL under prefix, versioned by its content hash. It
// panics on an unknown name: that is a programmer error, not a runtime one.
func IconHref(prefix, name string) string {
	v, ok := iconVersions[name]
	if !ok {
		panic("uiassets: unknown icon " + name)
	}
	return prefix + "icons/" + name + "?v=" + v
}

// ScriptHref is a script's URL under prefix, versioned by its content hash.
// It panics on an unknown name: that is a programmer error, not a runtime one.
func ScriptHref(prefix, name string) string {
	v, ok := scriptVersions[name]
	if !ok {
		panic("uiassets: unknown script " + name)
	}
	return prefix + name + "?v=" + v
}
