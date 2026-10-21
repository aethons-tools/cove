package adminclient

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

func TestKitSourceRoundTrip(t *testing.T) {
	const kit = "kind: studio\nname: web-dev\nprompt: one\n"
	repo, c1, update := gittest.Serve(t, map[string]string{"kit.yml": kit}, gittest.WithAuth("x-access-token", "good-token"))
	store := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &gitsource.Fetcher{Creds: gitsource.Creds{{Prefix: repo, Name: "gh", Value: "x-access-token:good-token"}}, Timeout: 30 * time.Second}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		jam.WithKitFetcher(store, f, log)))
	defer ts.Close()
	c := New(ts.URL, "")
	src := repo + "@main"

	res, err := c.ImportKit("", src)
	if err != nil || res.Name != "web-dev" || res.Version != 1 || res.Commit != c1 {
		t.Fatalf("ImportKit = %+v, %v", res, err)
	}
	if res, err = c.TrackKit("web-dev", src); err != nil || !res.Unchanged {
		t.Fatalf("TrackKit = %+v, %v", res, err)
	}
	c2 := update(map[string]string{"kit.yml": strings.Replace(kit, "one", "two", 1)})
	if res, err = c.SyncKit("web-dev"); err != nil || res.Unchanged || res.Version != 2 || res.Commit != c2 {
		t.Fatalf("SyncKit = %+v, %v", res, err)
	}
	h, err := c.KitHistory("web-dev")
	if err != nil || len(h) != 2 || h[0].Current || !h[1].Current || h[1].Source == nil || h[1].Source.Commit != c2 {
		t.Fatalf("KitHistory = %+v, %v", h, err)
	}
	if err := c.UntrackKit("web-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncKit("web-dev"); !errors.Is(err, ErrConflict) {
		t.Fatalf("SyncKit untracked = %v, want ErrConflict", err)
	}
	if _, err := c.SyncKit("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SyncKit unknown = %v, want ErrNotFound", err)
	}

	// A wrong credential is a 502 that names the cred, never its value.
	bad := &gitsource.Fetcher{Creds: gitsource.Creds{{Prefix: repo, Name: "gh", Value: "x-access-token:bad-token-xyz"}}, Timeout: 30 * time.Second}
	ts2 := httptest.NewServer(jam.NewAdminHandler(jam.NewMemStore(), nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		jam.WithKitFetcher(jam.NewMemStore(), bad, log)))
	defer ts2.Close()
	_, err = New(ts2.URL, "").ImportKit("", src)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 502 || strings.Contains(err.Error(), "bad-token-xyz") || !strings.Contains(se.Body, "cred gh") {
		t.Fatalf("bad cred = %v", err)
	}
}
