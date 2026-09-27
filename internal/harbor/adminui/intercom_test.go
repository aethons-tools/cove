package adminui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor/adminui"
	"github.com/aethons-tools/cove/internal/intercom"
)

// newIntercomLog opens a hermetic Log in a temp dir and appends the given messages.
func newIntercomLog(t *testing.T, squawks ...intercom.Squawk) *intercom.Log {
	t.Helper()
	l, err := intercom.Open(t.TempDir()+"/squawks.jsonl", testLogger())
	if err != nil {
		t.Fatalf("intercom.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	for _, m := range squawks {
		if _, err := l.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return l
}

func squawkHandler(t *testing.T, l adminui.SquawkReader) http.Handler {
	t.Helper()
	return adminui.Handler(newStore(t), testLogger(), nil, anyCred, l)
}

func actor(ref string) intercom.Target   { return intercom.Target{Kind: "actor", Ref: ref} }
func human(ref string) intercom.Target   { return intercom.Target{Kind: "human", Ref: ref} }
func channel(ref string) intercom.Target { return intercom.Target{Kind: "channel", Ref: ref} }

func TestSquawksRendersNewestFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	l := newIntercomLog(t,
		intercom.Squawk{From: actor("cove-1"), To: []intercom.Target{human("alice")}, Body: "older ping", At: t0, Project: "acme"},
		intercom.Squawk{From: human("alice"), To: []intercom.Target{actor("cove-1")}, Body: "newer reply", At: t0.Add(time.Hour), Project: "acme"},
	)
	body := get(t, squawkHandler(t, l), "/ui/intercom").Body.String()
	for _, want := range []string{"<nav", "Intercom", "older ping", "newer reply", "actor:cove-1", "human:alice", "acme"} {
		if !strings.Contains(body, want) {
			t.Errorf("messages page missing %q; got:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, "newer reply"), strings.Index(body, "older ping"); i > j {
		t.Errorf("newest message must render first: newer@%d older@%d", i, j)
	}
}

func TestSquawksReachBadges(t *testing.T) {
	l := newIntercomLog(t,
		intercom.Squawk{From: actor("cove-1"), To: []intercom.Target{actor("cove-2"), human("alice")}, Body: "hi"},
	)
	body := get(t, squawkHandler(t, l), "/ui/intercom").Body.String()
	// actor:cove-2 → internal, human:alice → external (intercom.Classify).
	if !strings.Contains(body, "internal") || !strings.Contains(body, "external") {
		t.Errorf("expected internal+external reach badges; got:\n%s", body)
	}
}

func TestSquawksEmptyLog(t *testing.T) {
	body := get(t, squawkHandler(t, newIntercomLog(t)), "/ui/intercom").Body.String()
	if !strings.Contains(body, "No squawks logged yet") {
		t.Errorf("empty log should say so; got:\n%s", body)
	}
}

func TestSquawksNotConfigured(t *testing.T) {
	rec := get(t, squawkHandler(t, nil), "/ui/intercom")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/intercom (nil reader) = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("nil reader should render a not-configured notice; got:\n%s", rec.Body.String())
	}
}

func TestSquawksNavLinkPresentOnOtherPages(t *testing.T) {
	body := get(t, squawkHandler(t, newIntercomLog(t)), "/ui/coves").Body.String()
	if !strings.Contains(body, `href="/ui/intercom"`) {
		t.Errorf("nav should link to /ui/intercom; got:\n%s", body)
	}
}

func fixtureLog(t *testing.T) *intercom.Log {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return newIntercomLog(t,
		intercom.Squawk{From: actor("cove-1"), To: []intercom.Target{channel("eng")}, Body: "deploy started", At: t0, Project: "acme"},
		intercom.Squawk{From: human("alice"), To: []intercom.Target{actor("cove-1")}, Body: "please HOLD", At: t0.Add(24 * time.Hour), Project: "acme"},
		intercom.Squawk{From: actor("cove-9"), To: []intercom.Target{human("bob")}, Body: "beta status", At: t0.Add(48 * time.Hour), Project: "beta"},
	)
}

func TestSquawksFilterProject(t *testing.T) {
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?project=beta").Body.String()
	if !strings.Contains(body, "beta status") || strings.Contains(body, "deploy started") {
		t.Errorf("project=beta should show only beta rows; got:\n%s", body)
	}
}

func TestSquawksFilterParticipant(t *testing.T) {
	// channel:eng appears only in the first message's To.
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?participant=channel:eng").Body.String()
	if !strings.Contains(body, "deploy started") || strings.Contains(body, "beta status") || strings.Contains(body, "please HOLD") {
		t.Errorf("participant=channel:eng should match only the eng-channel message; got:\n%s", body)
	}
	// actor:cove-1 is the sender of msg1 and a recipient of msg2 → both match.
	body = get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?participant=actor:cove-1").Body.String()
	if !strings.Contains(body, "deploy started") || !strings.Contains(body, "please HOLD") || strings.Contains(body, "beta status") {
		t.Errorf("participant=actor:cove-1 should match its sent + received messages; got:\n%s", body)
	}
}

func TestSquawksFilterBodySubstringCaseInsensitive(t *testing.T) {
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?q=hold").Body.String()
	if !strings.Contains(body, "please HOLD") || strings.Contains(body, "deploy started") {
		t.Errorf("q=hold should case-insensitively match 'please HOLD' only; got:\n%s", body)
	}
}

func TestSquawksFilterTimeWindow(t *testing.T) {
	// [2026-09-11, 2026-09-11] inclusive → only the 2026-09-11 message (msg2).
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?since=2026-09-11&until=2026-09-11").Body.String()
	if !strings.Contains(body, "please HOLD") || strings.Contains(body, "deploy started") || strings.Contains(body, "beta status") {
		t.Errorf("since=until=2026-09-11 should show only that day; got:\n%s", body)
	}
}

func TestSquawksFiltersIntersect(t *testing.T) {
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?project=acme&q=deploy").Body.String()
	if !strings.Contains(body, "deploy started") || strings.Contains(body, "please HOLD") {
		t.Errorf("project=acme&q=deploy should intersect to one row; got:\n%s", body)
	}
}

func TestSquawksFilterNoMatch(t *testing.T) {
	body := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?q=nothingmatchesthis").Body.String()
	if !strings.Contains(body, "No squawks match") {
		t.Errorf("a no-match filter should say 'No squawks match'; got:\n%s", body)
	}
}

func TestSquawksMalformedDateNotice(t *testing.T) {
	// A non-empty but unparseable date must be surfaced, not silently dropped —
	// and the page still renders (unbounded on that side), not errors.
	rec := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?since=not-a-date")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with a bad date = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ignored an unparseable date") {
		t.Errorf("a malformed date should be surfaced; got:\n%s", body)
	}
	// The rows still render (bad bound treated as unbounded).
	if !strings.Contains(body, "deploy started") {
		t.Errorf("a malformed date should leave that bound unbounded, still showing rows; got:\n%s", body)
	}
	// A well-formed date must NOT trip the notice.
	good := get(t, squawkHandler(t, fixtureLog(t)), "/ui/intercom?since=2026-09-11").Body.String()
	if strings.Contains(good, "Ignored an unparseable date") {
		t.Errorf("a valid date should not trip the malformed-date notice; got:\n%s", good)
	}
}
