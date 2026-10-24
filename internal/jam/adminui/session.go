package adminui

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub) {
	mux.HandleFunc("GET /app/admin/agents/{id}/session", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": r.PathValue("id") + " session", "ActorID": r.PathValue("id"), "Enabled": store != nil}
		if store != nil {
			streams, _ := store.Streams(r.PathValue("id"))
			selected := r.URL.Query().Get("stream")
			if !sessionevents.ValidStreamID(selected) && len(streams) > 0 {
				selected = streams[0].StreamID
			}
			data["Streams"], data["Stream"] = streams, selected
		}
		render(w, r, "session", data)
	})
	events := func(w http.ResponseWriter, r *http.Request) {
		if store == nil || hub == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sessionview.Serve(w, r, sessionview.Source{Store: store, Hub: hub, ActorID: r.PathValue("id"),
			Event:  func(v sessionview.EventView) string { return fragment("session-event", v) },
			Totals: func(t sessionevents.Totals) string { return fragment("session-totals", t) }})
	}
	mux.HandleFunc("GET /app/admin/agents/{id}/session/events", events)
	// The pre-agents path, for timelines already open in a browser.
	mux.HandleFunc("GET /app/admin/coves/{id}/session/events", events)
}

// sessionFragments is a clone of the session page's set for rendering event
// fragments (the master set is only ever cloned, never executed).
var sessionFragments = sync.OnceValue(func() *template.Template {
	return template.Must(pages["session"].t.Clone())
})

func fragment(name string, data any) string {
	var b bytes.Buffer
	if err := sessionFragments().ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	// html/template leaves CR raw; keep it visible to the operator as an entity
	// (SSE framing is separately CR-safe in sessionview.WriteSSE).
	return strings.ReplaceAll(b.String(), "\r", "&#13;")
}
