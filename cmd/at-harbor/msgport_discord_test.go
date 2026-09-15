package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
	"github.com/aethons-tools/cove/internal/switchboard"
)

// fakeDiscordClient is a fake discordClient: records PostID calls, returns a
// scripted post id / poll result, so discordSurface is testable without a
// live Discord bot.
type fakeDiscordClient struct {
	posts      []struct{ channel, content string }
	postID     string
	postErr    error
	pollMsgs   []switchboard.Message
	pollNext   map[string]string
	pollErr    error
	gotCursors map[string]string
}

func (f *fakeDiscordClient) PostID(_ context.Context, ch, c string) (string, error) {
	if f.postErr != nil {
		return "", f.postErr
	}
	f.posts = append(f.posts, struct{ channel, content string }{ch, c})
	return f.postID, nil
}

func (f *fakeDiscordClient) Poll(_ context.Context, cur map[string]string) ([]switchboard.Message, map[string]string, error) {
	f.gotCursors = cur
	return f.pollMsgs, f.pollNext, f.pollErr
}

func mustReceipts(t *testing.T) *fileReceipts {
	t.Helper()
	r, err := newFileReceipts(filepath.Join(t.TempDir(), "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDiscordDeliverRecordsReceipt(t *testing.T) {
	fc := &fakeDiscordClient{postID: "D1"}
	rec := mustReceipts(t)
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec}
	id, err := s.Deliver(context.Background(), msgport.Delivery{Address: "inbox-A", BodyPrefix: "cove-1: "}, msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, Body: "hi"})
	if err != nil || id != "D1" {
		t.Fatalf("Deliver = %q,%v", id, err)
	}
	if len(fc.posts) != 1 || fc.posts[0].channel != "inbox-A" || fc.posts[0].content != "cove-1: hi" {
		t.Fatalf("post = %+v", fc.posts)
	}
	if a, ok := rec.Lookup("D1"); !ok || a != "cove-1" {
		t.Fatalf("receipt = %q,%v", a, ok)
	}
}

func TestDiscordDeliverPropagatesPostError(t *testing.T) {
	fc := &fakeDiscordClient{postErr: errBoom}
	rec := mustReceipts(t)
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{Body: "x"}); err == nil {
		t.Fatal("expected post error")
	}
}

func TestDiscordDeliverSwallowsEmptyID(t *testing.T) {
	// PostID returning "" (a known empty-response-body behavior) must never
	// be recorded as a receipt — an empty key would be ambiguous.
	fc := &fakeDiscordClient{postID: ""}
	rec := mustReceipts(t)
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec}
	id, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, Body: "hi"})
	if err != nil || id != "" {
		t.Fatalf("Deliver = %q,%v", id, err)
	}
	if _, ok := rec.Lookup(""); ok {
		t.Fatal("must not record an empty-key receipt")
	}
}

func TestDiscordDeliverSwallowsReceiptError(t *testing.T) {
	// receipts pointed at an unwritable path: Record fails, but the post
	// already happened, so Deliver must still return a nil error — an error
	// here would make the engine retry and double-post. The swallowed error
	// must still surface as a warn log (channel + error only — no secret
	// body/token), so it isn't silently lost.
	fc := &fakeDiscordClient{postID: "D1"}
	// newFileReceipts tolerates a missing file at open time (starts empty);
	// the parent dir not existing only bites on the later Record→WriteFile.
	rec, err := newFileReceipts(filepath.Join(t.TempDir(), "nope", "sub", "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, nil))
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec, log: log}
	const secretBody = "top-secret cove message body"
	id, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, Body: secretBody})
	if err != nil {
		t.Fatalf("Deliver must swallow the receipt error, got %v", err)
	}
	if id != "D1" {
		t.Fatalf("Deliver id = %q, want D1", id)
	}
	logs := logbuf.String()
	if !strings.Contains(logs, "receipt record failed") || !strings.Contains(logs, "channel=c") {
		t.Fatalf("expected a warn log naming the channel, got %q", logs)
	}
	if strings.Contains(logs, secretBody) {
		t.Fatalf("log must not contain the message body, got %q", logs)
	}
}

func TestDiscordDeliverSwallowsReceiptErrorNilLogger(t *testing.T) {
	// A nil logger (e.g. a discordSurface constructed without one) must not
	// panic on the swallowed-error path.
	fc := &fakeDiscordClient{postID: "D1"}
	rec, err := newFileReceipts(filepath.Join(t.TempDir(), "nope", "sub", "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, Body: "hi"}); err != nil {
		t.Fatalf("Deliver must swallow the receipt error, got %v", err)
	}
}

func TestDiscordPollMapsReplies(t *testing.T) {
	fc := &fakeDiscordClient{
		pollMsgs: []switchboard.Message{{ID: "m2", Channel: "inbox-A", Author: "alice", Content: "re", ReferencedID: "D1"}},
		pollNext: map[string]string{"inbox-A": "m2"},
	}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, channelsFor: func(string) []string { return []string{"inbox-A"} }}
	ev, next, err := s.Poll(context.Background(), "acme", "")
	if err != nil || len(ev) != 1 || ev[0].ReplyToForeign != "D1" || ev[0].ForeignID != "m2" || ev[0].Author != "alice" {
		t.Fatalf("poll = %+v,%v", ev, err)
	}
	if fc.gotCursors == nil || len(fc.gotCursors) != 0 {
		t.Fatalf("expected empty decoded cursors, got %+v", fc.gotCursors)
	}
	// next round-trips: decode(next) == pollNext
	if got := decodeCursors(next); got["inbox-A"] != "m2" {
		t.Fatalf("next = %q", next)
	}
}

func TestDiscordPollPropagatesError(t *testing.T) {
	fc := &fakeDiscordClient{pollErr: errBoom}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, channelsFor: func(string) []string { return []string{"inbox-A"} }}
	ev, next, err := s.Poll(context.Background(), "acme", "cur")
	if err == nil || len(ev) != 0 || next != "cur" {
		t.Fatalf("poll = %+v,%q,%v", ev, next, err)
	}
}

func TestDiscordPollEmptyChannels(t *testing.T) {
	s := &discordSurface{channelsFor: func(string) []string { return nil }}
	ev, next, err := s.Poll(context.Background(), "acme", "cur")
	if err != nil || len(ev) != 0 || next != "cur" {
		t.Fatalf("empty = %+v,%q,%v", ev, next, err)
	}
}

func TestCursorCodecRoundTrip(t *testing.T) {
	if got := decodeCursors(""); len(got) != 0 {
		t.Fatal("empty → empty map")
	}
	m := map[string]string{"a": "1", "b": "2"}
	if got := decodeCursors(encodeCursors(m)); got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("round-trip = %+v", got)
	}
	if got := decodeCursors("not json"); len(got) != 0 {
		t.Fatal("torn → empty")
	}
	if got := encodeCursors(nil); got != "" {
		t.Fatalf("nil map should encode to %q, got %q", "", got)
	}
	if got := encodeCursors(map[string]string{}); got != "" {
		t.Fatalf("empty map should encode to %q, got %q", "", got)
	}
}

func TestFileReceiptsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	r, err := newFileReceipts(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("D1"); ok {
		t.Fatal("empty lookup should miss")
	}
	if err := r.Record("D1", "cove-1"); err != nil {
		t.Fatal(err)
	}
	if a, ok := r.Lookup("D1"); !ok || a != "cove-1" {
		t.Fatalf("lookup = %q,%v", a, ok)
	}
	r2, err := newFileReceipts(p) // reload
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := r2.Lookup("D1"); !ok || a != "cove-1" {
		t.Fatalf("reload = %q,%v", a, ok)
	}
}

func TestFileReceiptsMissingFile(t *testing.T) {
	r, err := newFileReceipts(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("x"); ok {
		t.Fatal("missing file → empty")
	}
}

func TestDiscordInboxChannels(t *testing.T) {
	store := &fakeStore{
		roster: map[string]harbor.Roster{
			"acme": {
				Humans: []harbor.Human{
					{Name: "alice", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "chan-A"}}},
					{Name: "bob", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "chan-B"}}},
					{Name: "carol", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "chan-A"}}}, // duplicate address, deduped
					{Name: "dave"}, // no discord profile
				},
			},
		},
	}
	got := discordInboxChannels(store, "acme")
	if len(got) != 2 {
		t.Fatalf("channels = %v, want 2 distinct", got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		seen[c] = true
	}
	if !seen["chan-A"] || !seen["chan-B"] {
		t.Fatalf("channels = %v, want chan-A and chan-B", got)
	}
}

func TestDiscordInboxChannelsNoRoster(t *testing.T) {
	store := &fakeStore{roster: map[string]harbor.Roster{}}
	if got := discordInboxChannels(store, "nope"); got != nil {
		t.Fatalf("channels = %v, want nil", got)
	}
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }
