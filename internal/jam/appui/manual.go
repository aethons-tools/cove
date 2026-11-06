package appui

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// manualHandler serves the customer manual (a static Antora site) under
// /app/manual/. The site carries no script, so the CSP forbids it. A directory
// path answers its index.html; a file is served as asked (no index.html
// redirect, so the page's own URL works).
func manualHandler(m fs.FS) http.Handler {
	if _, err := fs.Stat(m, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "The manual isn't built into this Jam.", http.StatusNotFound)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/app/manual/")), "/")
		if name == "" {
			name = "."
		}
		if fi, err := fs.Stat(m, name); err == nil && fi.IsDir() {
			if !strings.HasSuffix(r.URL.Path, "/") { // relative links need the slash
				http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
				return
			}
			name = path.Join(name, "index.html")
		}
		f, err := m.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; object-src 'none'")
		if rs, ok := f.(io.ReadSeeker); ok {
			http.ServeContent(w, r, name, time.Time{}, rs)
			return
		}
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(b)))
	})
}
