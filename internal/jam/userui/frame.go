package userui

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

// The pane's framed views (docs/usage/jam/previews.md#framing): the Studio
// tab's IDE and a preview's View here. The page asks for a frame URL by the
// session and preview name only, never a host or URL.

const (
	// maxFrameMints is the most frame URLs one user may mint per
	// frameMintWindow; above it the route answers 429.
	maxFrameMints   = 30
	frameMintWindow = time.Minute
)

// mintLimiter is a per-user sliding-window counter of frame mints.
type mintLimiter struct {
	now func() time.Time
	mu  sync.Mutex
	at  map[ident.ID][]time.Time // each user's mints within the window, oldest first
}

func newMintLimiter(now func() time.Time) *mintLimiter {
	return &mintLimiter{now: now, at: map[ident.ID][]time.Time{}}
}

// allow records a mint for user and reports whether it is within the cap.
// A refused mint is not recorded.
func (l *mintLimiter) allow(user ident.ID) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := l.at[user]
	i := 0
	for i < len(ts) && !now.Before(ts[i].Add(frameMintWindow)) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= maxFrameMints {
		l.at[user] = ts
		return false
	}
	l.at[user] = append(ts, now)
	return true
}

// collabPrincipal is the viewer as collab sees them: an operator acts as one.
func collabPrincipal(r *http.Request, p jam.Participant) collab.Principal {
	cp := collab.Principal{UserID: p.UserID}
	if pr, _ := appui.PrincipalFrom(r); pr.Admin {
		cp.Operator = pr.Operator.ID
	}
	return cp
}

// frameRoute mints a frame URL for one of a session's framed views, form
// name: the IDE (preview.IDEName) under the ssh right, else an exposed
// preview under the preview right. 200 {"url", "open"} (open: the plain
// preview URL, for Open in new tab); 404 for an unseen session, framing off
// or a name the session doesn't expose; 403 without the right; 429 over the
// per-user mint cap.
func (h *handler) frameRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	inst, _, ok := h.sessionFor(p, r.PathValue("id"))
	if !ok || h.d.Frames == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PostFormValue("name")
	right := collab.SSH
	if name != preview.IDEName {
		if !slices.ContainsFunc(inst.Exposures, func(e jam.Exposure) bool { return e.Name == name }) {
			http.NotFound(w, r)
			return
		}
		right = collab.Preview
	}
	cp := collabPrincipal(r, p)
	if _, ok := collab.Decide(h.d.Store, time.Now(), cp, inst, right); !ok {
		http.Error(w, "you may not open this here", http.StatusForbidden)
		return
	}
	if !h.mints.allow(p.UserID) {
		http.Error(w, "too many frames opened; wait a minute", http.StatusTooManyRequests)
		return
	}
	u, err := h.d.Frames.FrameURL(cp, p.Name, inst.ActorID, name, right)
	if err != nil {
		h.lg.Error("frame mint failed", "session", inst.ActorID, "name", name, "error", err.Error())
		http.Error(w, "couldn't open the frame", http.StatusInternalServerError)
		return
	}
	var open string
	if h.d.PreviewURL != nil {
		open, _ = h.d.PreviewURL(inst.ActorID, name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // the URL carries a one-time ticket
	_ = json.NewEncoder(w).Encode(map[string]string{"url": u, "open": open})
}
