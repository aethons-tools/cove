package meui

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

type fakeStore struct {
	rosters map[string]jam.Roster
	insts   []jam.Instance
	cursors map[string]map[string]int64
}

func (f *fakeStore) GetRoster(p string) (jam.Roster, bool) { r, ok := f.rosters[p]; return r, ok }
func (f *fakeStore) ListInstances() []jam.Instance         { return f.insts }
func (f *fakeStore) UnreadCursors(participant string) map[string]int64 {
	return f.cursors[participant]
}
func (f *fakeStore) CommitUnread(participant, channel string, seq int64) error {
	if f.cursors == nil {
		f.cursors = map[string]map[string]int64{}
	}
	if f.cursors[participant] == nil {
		f.cursors[participant] = map[string]int64{}
	}
	f.cursors[participant][channel] = seq
	return nil
}

type fakeLog struct{ sq []intercom.Squawk }

func (f fakeLog) ListSince(after int64, _ int) []intercom.Squawk {
	var out []intercom.Squawk
	for _, m := range f.sq {
		if m.Seq > after {
			out = append(out, m)
		}
	}
	return out
}

func fixture() (*fakeStore, fakeLog, jam.Participant) {
	roster := jam.Roster{
		Humans: []jam.Human{{
			Name: "alice", Handle: "alice",
			Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-alice"}},
		}},
		Channels: []jam.Channel{{Name: "eng"}},
	}
	store := &fakeStore{rosters: map[string]jam.Roster{"proj": roster}}
	log := fakeLog{sq: []intercom.Squawk{{
		Seq:  1,
		From: intercom.Target{Kind: "human", Ref: "alice"},
		To:   []intercom.Target{{Kind: "channel", Ref: "eng"}},
		Body: "hi from alice", At: time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC), Project: "proj",
	}}}
	p := jam.Participant{Issuer: "https://idp", Subject: "sub-alice", Projects: []string{"proj"}, Name: "alice"}
	return store, log, p
}

func TestInboxFullPageRendersRailAndConversation(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)

	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /me/ = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Jam", "New message", "eng", "hi from alice", "/me/static/htmx.min.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing %q", want)
		}
	}
	// The message from the viewer renders on the "human" (own) side.
	if !strings.Contains(body, `msg human`) {
		t.Error("viewer's own message should render on the human side")
	}
	// The composer must be OUTSIDE the polled region: the poll lives on #stream
	// (innerHTML), and the composer is a later sibling — so a 3s refresh never
	// wipes a typed reply.
	if strings.Index(body, `id="stream"`) > strings.Index(body, `class="composer"`) {
		t.Error("composer should render after the stream, as a sibling outside it")
	}
	if !strings.Contains(body, `hx-swap="innerHTML"`) {
		t.Error("stream should poll with innerHTML swap (not outerHTML on the whole pane)")
	}
}

func TestStreamFragmentIsMessagesOnly(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/stream?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /me/stream = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hi from alice") {
		t.Error("stream fragment should contain the messages")
	}
	// It is ONLY the messages — no composer, no topbar chrome to clobber.
	for _, absent := range []string{"class=\"composer\"", "New message", "<header"} {
		if strings.Contains(body, absent) {
			t.Errorf("stream fragment should not contain %q (it must not re-render chrome)", absent)
		}
	}
}

func TestInboxFailsClosedWithoutParticipant(t *testing.T) {
	store, log, _ := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/", nil) // no WithParticipant
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no-participant /me/ = %d, want 401", rec.Code)
	}
}

func TestMarkReadCommitsCursor(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)

	form := url.Values{"channel": {"named:eng"}, "seq": {"1"}}
	req := httptest.NewRequest("POST", "/me/read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("POST /me/read = %d, want 204", rec.Code)
	}
	// Committed under the participant's self ref in the channel's project.
	if got := store.cursors["human:alice"]["named:eng"]; got != 1 {
		t.Errorf("cursor = %d, want 1", got)
	}
}
