package adminui

//go:generate sh -c "cd ../../.. && go tool tygo generate"

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/jam/webapp"
)

// appMount is where the admin area of the Jam app (web/app) is served while
// it is built beside the old pages; the cutover moves it to /app/admin/
// (docs/superpowers/specs/2026-10-28-admin-spa-design.md).
const appMount = "/app/admin/next/"

// apiPrefix is the admin app's JSON API.
const apiPrefix = "/app/admin/api/"

// registerApp mounts the admin app's pages and its JSON API on mux. The API
// refuses cross-origin writes before any of its handlers runs.
func registerApp(mux *http.ServeMux, src frameSource, msgs SquawkReader, ra roleAPI, su specUI, trustedOrigins []string, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger) {
	api := http.NewServeMux()
	api.HandleFunc("GET "+apiPrefix+"bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, adminBootstrap(r, src.items()))
	})
	api.HandleFunc("GET "+apiPrefix+"tree", treeHandler(src))
	apiUsers(api, src, log)
	apiTracking(api, src.store, f, env, log)
	apiProjects(api, src, msgs, log)
	apiRoles(api, src, ra, log)
	apiSpecs(api, src, su, ra.credExists, log)
	apiKits(api, src, f, log)
	api.HandleFunc(apiPrefix, func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such API route")
	})
	mux.Handle(apiPrefix, sameOriginAPI(trustedOrigins, api))
	mux.Handle("GET "+appMount, webapp.Handler(webapp.Config{
		Prefix: appMount, FS: uiassets.AppFS(), Name: "the Jam app", Head: appui.HeadMeta,
		Bootstrap: func(r *http.Request) (any, error) {
			if _, ok := appui.PrincipalFrom(r); !ok {
				return nil, errors.New("adminui: no principal on the request (mount the handler behind the admin gate)")
			}
			return adminBootstrap(r, src.items()), nil
		},
	}))
}

// adminBootstrap is the admin app's bootstrap for r's principal; items are
// every attention item (the tabs' badges).
func adminBootstrap(r *http.Request, items []attnItem) Bootstrap {
	tb := appui.TitlebarFor(r, "")
	p, _ := appui.PrincipalFrom(r)
	b := Bootstrap{Brand: tb.Brand, Name: tb.Name, Initial: tb.Initial, Roles: tb.Roles, Member: p.User != nil, SignOut: tb.SignOut, Tabs: []NavTab{}}
	if b.Member {
		b.Tabs = append(b.Tabs, NavTab{Key: "inbox", Label: "Inbox", Href: "/app/user/"})
	}
	badges := appTabBadges(items)
	for _, t := range []struct{ key, label, path string }{
		{"dashboard", "Dashboard", ""}, {"config", "Config", "config/tracking"}, {"agents", "Agents", "agents"}, {"health", "Health", "health"},
	} {
		bd := badges[t.key]
		b.Tabs = append(b.Tabs, NavTab{Key: t.key, Label: t.label, Href: appMount + t.path, Badge: bd.N, BadgeRed: bd.Red, BadgeTitle: bd.Title})
	}
	return b
}

// sameOriginAPI is browserauth.RequireSameOrigin answering in the API's JSON
// envelope, so the app shows the refusal (a non-JSON 403 reads as an expired
// sign-in).
func sameOriginAPI(trustedOrigins []string, next http.Handler) http.Handler {
	trusted := map[string]bool{}
	for _, o := range trustedOrigins {
		trusted[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !browserauth.SameOrigin(r, trusted) {
				writeAPIError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// decodeJSON reads a write's JSON body into v; false (answered) when the body
// isn't JSON.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "bad_request", "send JSON (Content-Type: application/json)")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// apiCode is the error code for an API status.
func apiCode(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType:
		return "bad_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "stale"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	}
	return "internal"
}

// apiFail answers a write's error: its status from a jam.WriteError or a
// registry error, else fallback.
func apiFail(w http.ResponseWriter, err error, fallback int) {
	status := jam.WriteStatus(registryErr(err), fallback)
	writeAPIError(w, status, apiCode(status), err.Error())
}

// writeJSON writes v as an API response (never cached, never sniffed).
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError writes the API's error envelope.
func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIError{Error: msg, Code: code})
}
