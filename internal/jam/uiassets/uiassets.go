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
)

//go:embed static/jam.css static/htmx.min.js static/brand/*.svg static/icons/*.png
var files embed.FS

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

// Handler serves the shared assets at prefix (e.g. "/app/static/"); register it
// on the "GET <prefix>" pattern.
func Handler(prefix string) http.Handler {
	return http.StripPrefix(prefix, http.FileServer(http.FS(static)))
}

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
