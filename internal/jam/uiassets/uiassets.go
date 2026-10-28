// Package uiassets holds Jam's static browser assets — jam.css (the server-
// rendered refusal page's styles), the brand marks and the installable-app
// icons — which the browser app serves, ungated, under /app/static/. It also
// embeds the built Jam app (web/app: both the user and admin areas).
package uiassets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

//go:embed static/jam.css static/brand/*.svg static/icons/*.png
var files embed.FS

// appFiles is the Jam app bundle (web/app), built into app/ by `just app`
// (scripts/app.sh); only a placeholder is committed, so a plain `go build`
// has none and webapp serves its not-built page.
//
//go:embed all:app
var appFiles embed.FS

var app = func() fs.FS {
	sub, err := fs.Sub(appFiles, "app")
	if err != nil {
		panic(err)
	}
	return sub
}()

// AppFS is the Jam app bundle's root (index.html, assets/).
func AppFS() fs.FS { return app }

// AppBuilt reports whether this binary carries the Jam app bundle.
func AppBuilt() bool {
	_, err := fs.Stat(app, "index.html")
	return err == nil
}

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
