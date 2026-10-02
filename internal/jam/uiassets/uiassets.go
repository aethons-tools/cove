// Package uiassets holds the static assets the Jam UIs share — jam.css (the
// design tokens and typography base) and htmx — so the operator admin UI (/ui)
// and the participant intercom (/me) read as one product from one source. Each
// UI mounts Handler under its own static prefix, keeping its own auth gate.
package uiassets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

//go:embed static/jam.css static/htmx.min.js
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

// Handler serves the shared assets at prefix (e.g. "/ui/static/"); register it
// on the "GET <prefix>" pattern.
func Handler(prefix string) http.Handler {
	return http.StripPrefix(prefix, http.FileServer(http.FS(static)))
}

// StylesheetHref is jam.css's URL under prefix, versioned by content.
func StylesheetHref(prefix string) string {
	return prefix + "jam.css?v=" + version
}
