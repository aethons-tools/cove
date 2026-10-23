package appui

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// HeadTemplate defines {{define "app-head"}} over a Head: the manifest link,
// theme colours, icons and the service-worker registration. The admin and
// user UIs parse it into their page templates, inside <head>.
//
//go:embed head.html
var HeadTemplate string

//go:embed sw.js
var swJS []byte

// Head is the data the "app-head" template renders.
type Head struct {
	Title      string // the shell brand ("{display name} Jam"), else "Jam"
	Favicon    string // versioned favicon-32 URL
	AppleTouch string // versioned apple-touch-icon URL
}

const staticPrefix = "/app/static/"

// HeadFor builds the app head for r (its shell's brand).
func HeadFor(r *http.Request) Head {
	sh, _ := r.Context().Value(shellKey{}).(Shell)
	return Head{
		Title:      brandOr(sh.Brand),
		Favicon:    uiassets.IconHref(staticPrefix, "favicon-32.png"),
		AppleTouch: uiassets.IconHref(staticPrefix, "apple-touch-icon.png"),
	}
}

func brandOr(b string) string {
	if b == "" {
		return "Jam"
	}
	return b
}

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

type manifest struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	Scope           string         `json:"scope"`
	StartURL        string         `json:"start_url"`
	Display         string         `json:"display"`
	ThemeColor      string         `json:"theme_color"`
	BackgroundColor string         `json:"background_color"`
	Icons           []manifestIcon `json:"icons"`
}

// manifestHandler serves the web app manifest for brand (the shell brand; ""
// = "Jam"). It is public: the browser fetches it without credentials.
func manifestHandler(brand string) http.Handler {
	icon := func(name, sizes, purpose string) manifestIcon {
		return manifestIcon{Src: uiassets.IconHref(staticPrefix, name), Sizes: sizes, Type: "image/png", Purpose: purpose}
	}
	body, err := json.Marshal(manifest{
		ID: "/app/", Name: brandOr(brand), ShortName: "Jam",
		Scope: "/app/", StartURL: "/app/", Display: "standalone",
		ThemeColor: "#2F67B2", BackgroundColor: "#F5F6F8",
		Icons: []manifestIcon{
			icon("icon-192.png", "192x192", "any"),
			icon("icon-512.png", "512x512", "any"),
			icon("icon-maskable-512.png", "512x512", "maskable"),
		},
	})
	if err != nil {
		panic(err) // a fixed struct of strings always marshals
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		_, _ = w.Write(body)
	})
}

// serviceWorker serves the network-only service worker; no-cache so a new
// build's worker is picked up on the next navigation.
func serviceWorker() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(swJS)
	})
}
