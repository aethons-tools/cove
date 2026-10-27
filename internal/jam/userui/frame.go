package userui

import (
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

// frameOutcome mints a frame URL for one of inst's framed views: the IDE
// (preview.IDEName) under the ssh right, else an exposed preview under the
// preview right. It answers 200 with the URL, or a refusal's status, API
// code and text.
func (h *handler) frameOutcome(r *http.Request, p jam.Participant, inst jam.Instance, name string) (FrameURL, int, string, string) {
	right := collab.SSH
	if name != preview.IDEName {
		if !slices.ContainsFunc(inst.Exposures, func(e jam.Exposure) bool { return e.Name == name }) {
			return FrameURL{}, http.StatusNotFound, "not_found", "no such session"
		}
		right = collab.Preview
	}
	cp := collabPrincipal(r, p)
	if _, ok := collab.Decide(h.d.Store, time.Now(), cp, inst, right); !ok {
		return FrameURL{}, http.StatusForbidden, "forbidden", "you may not open this here"
	}
	if !h.mints.allow(p.UserID) {
		return FrameURL{}, http.StatusTooManyRequests, "too_many", "too many frames opened; wait a minute"
	}
	u, err := h.d.Frames.FrameURL(cp, p.Name, inst.ActorID, name, right)
	if err != nil {
		h.lg.Error("frame mint failed", "session", inst.ActorID, "name", name, "error", err.Error())
		return FrameURL{}, http.StatusInternalServerError, "internal", "couldn't open the frame"
	}
	out := FrameURL{URL: u}
	if h.d.PreviewURL != nil {
		out.Open, _ = h.d.PreviewURL(inst.ActorID, name)
	}
	return out, http.StatusOK, "", ""
}
