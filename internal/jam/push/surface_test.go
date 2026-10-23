package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/relay"
)

// fakeSubs is an in-memory jam.PushSubscriptionStore.
type fakeSubs struct {
	mu      sync.Mutex
	rows    map[string]jam.PushSubscription
	touched map[string]time.Time
}

func newFakeSubs(rows ...jam.PushSubscription) *fakeSubs {
	f := &fakeSubs{rows: map[string]jam.PushSubscription{}, touched: map[string]time.Time{}}
	for _, r := range rows {
		f.rows[r.Endpoint] = r
	}
	return f
}

func (f *fakeSubs) PutPushSubscription(s jam.PushSubscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[s.Endpoint] = s
	return nil
}

func (f *fakeSubs) DeletePushSubscription(endpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, endpoint)
	return nil
}

func (f *fakeSubs) DeleteUserPushSubscription(user ident.ID, endpoint string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[endpoint]; ok && r.UserID == user {
		delete(f.rows, endpoint)
		return true, nil
	}
	return false, nil
}

func (f *fakeSubs) PushSubscriptions(user ident.ID) []jam.PushSubscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []jam.PushSubscription
	for _, r := range f.rows {
		if r.UserID == user {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b jam.PushSubscription) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	return out
}

func (f *fakeSubs) TouchPushSubscription(endpoint string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[endpoint]; ok {
		f.touched[endpoint] = at
	}
	return nil
}

func (f *fakeSubs) has(endpoint string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[endpoint]
	return ok
}

// fakeSender answers each Send with a scripted status or error.
type fakeSender struct {
	status int
	err    error
	calls  []sendCall
}

type sendCall struct {
	payload []byte
	sub     jam.PushSubscription
	opts    SendOptions
}

func (f *fakeSender) Send(_ context.Context, payload []byte, sub jam.PushSubscription, opts SendOptions) (int, error) {
	f.calls = append(f.calls, sendCall{payload: payload, sub: sub, opts: opts})
	return f.status, f.err
}

const testEndpoint = "https://fcm.googleapis.com/fcm/send/device-1"

type surfaceKit struct {
	subs   *fakeSubs
	sender *fakeSender
	conds  *condition.Tracker
	logs   *bytes.Buffer
	now    time.Time
	s      *Surface
}

func newSurfaceKit(t *testing.T, rows ...jam.PushSubscription) *surfaceKit {
	t.Helper()
	if rows == nil {
		rows = []jam.PushSubscription{{Endpoint: testEndpoint, UserID: u1, P256dh: "p", Auth: "a"}}
	}
	k := &surfaceKit{subs: newFakeSubs(rows...), sender: &fakeSender{status: 201}, logs: &bytes.Buffer{},
		now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))}
	k.conds = condition.New(condition.Options{Now: func() time.Time { return k.now }})
	k.s = &Surface{
		Store:  k.subs,
		Sender: k.sender,
		Payload: func(m intercom.Squawk, user ident.ID) (Payload, bool) {
			return Payload{Title: "t", Body: m.Body, Channel: string(m.Channel), ID: m.ID, Unread: 3}, true
		},
		Opts:  SendOptions{Subscriber: "mailto:ops@example.com", VAPIDPrivate: "priv", VAPIDPublic: "pub"},
		Now:   func() time.Time { return k.now },
		Conds: k.conds,
		Log:   slog.New(slog.NewTextHandler(k.logs, nil)),
	}
	return k
}

func (k *surfaceKit) msg() intercom.Squawk {
	return intercom.Squawk{ID: "sq-1", Channel: chat, From: u2, Body: "hello", At: k.now.Add(-time.Minute)}
}

func (k *surfaceKit) deliver(t *testing.T, m intercom.Squawk) error {
	t.Helper()
	_, err := k.s.Deliver(context.Background(), relay.Delivery{Service: "push", Address: AddressFor(testEndpoint), BodyPrefix: string(u1)}, m)
	return err
}

func TestAddressFor(t *testing.T) {
	sum := sha256.Sum256([]byte(testEndpoint))
	if got, want := AddressFor(testEndpoint), "push:"+hex.EncodeToString(sum[:])[:16]; got != want {
		t.Fatalf("AddressFor = %q, want %q", got, want)
	}
	if AddressFor(testEndpoint) == AddressFor(testEndpoint+"x") {
		t.Fatal("distinct endpoints share an address")
	}
}

func TestSurfaceBasics(t *testing.T) {
	k := newSurfaceKit(t)
	if k.s.Service() != "push" {
		t.Fatalf("Service = %q", k.s.Service())
	}
	if ev, next, err := k.s.Poll(context.Background(), "p", "c"); ev != nil || next != "" || err != nil {
		t.Fatalf("Poll = %v %q %v", ev, next, err)
	}
	if err := k.s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverSuccess(t *testing.T) {
	k := newSurfaceKit(t)
	m := k.msg()
	if err := k.deliver(t, m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(k.sender.calls) != 1 {
		t.Fatalf("sends = %d", len(k.sender.calls))
	}
	c := k.sender.calls[0]
	if c.sub.Endpoint != testEndpoint {
		t.Fatalf("sent to %q", c.sub.Endpoint)
	}
	sum := sha256.Sum256([]byte(chat))
	want := SendOptions{TTL: 86400, Topic: hex.EncodeToString(sum[:])[:32], Subscriber: "mailto:ops@example.com", VAPIDPrivate: "priv", VAPIDPublic: "pub"}
	if c.opts != want {
		t.Fatalf("opts = %+v, want %+v", c.opts, want)
	}
	var p Payload
	if err := json.Unmarshal(c.payload, &p); err != nil || p.Body != "hello" || p.ID != "sq-1" || p.Unread != 3 {
		t.Fatalf("payload %s (%v)", c.payload, err)
	}
	at, ok := k.subs.touched[testEndpoint]
	if !ok || !at.Equal(k.now) || at.Location() != time.UTC {
		t.Fatalf("touched = %v (%v), want %v in UTC", at, ok, k.now)
	}
}

func TestDeliverGoneDeletes(t *testing.T) {
	for _, status := range []int{404, 410} {
		k := newSurfaceKit(t)
		k.sender.status = status
		if err := k.deliver(t, k.msg()); err != nil {
			t.Fatalf("%d: Deliver = %v, want nil", status, err)
		}
		if k.subs.has(testEndpoint) {
			t.Fatalf("%d: subscription kept", status)
		}
	}
}

func TestDeliverOtherClientErrorIsLoggedSuccess(t *testing.T) {
	for _, status := range []int{400, 403, 413, 307} {
		k := newSurfaceKit(t)
		k.sender.status = status
		if err := k.deliver(t, k.msg()); err != nil {
			t.Fatalf("%d: Deliver = %v, want nil", status, err)
		}
		if !k.subs.has(testEndpoint) {
			t.Fatalf("%d: subscription deleted", status)
		}
		if !strings.Contains(k.logs.String(), "push: send refused") {
			t.Fatalf("%d: not logged: %q", status, k.logs.String())
		}
		if strings.Contains(k.logs.String(), testEndpoint) {
			t.Fatalf("log carries the endpoint: %q", k.logs.String())
		}
	}
}

func TestDeliverTransientErrors(t *testing.T) {
	for _, tt := range []struct {
		status int
		err    error
	}{{429, nil}, {500, nil}, {503, nil}, {0, errors.New("connection reset")}} {
		k := newSurfaceKit(t)
		k.sender.status, k.sender.err = tt.status, tt.err
		if err := k.deliver(t, k.msg()); err == nil {
			t.Fatalf("%d/%v: Deliver = nil, want an error (retried)", tt.status, tt.err)
		}
		if !k.subs.has(testEndpoint) {
			t.Fatalf("%d: subscription deleted", tt.status)
		}
	}
}

func TestDeliverStaleSkips(t *testing.T) {
	k := newSurfaceKit(t)
	m := k.msg()
	m.At = k.now.Add(-11 * time.Minute)
	if err := k.deliver(t, m); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	if len(k.sender.calls) != 0 {
		t.Fatal("a stale squawk was sent")
	}
	m.At = k.now.Add(-9 * time.Minute)
	if err := k.deliver(t, m); err != nil || len(k.sender.calls) != 1 {
		t.Fatalf("a 9-minute-old squawk: err %v, sends %d", err, len(k.sender.calls))
	}
}

func TestDeliverHealthCondition(t *testing.T) {
	k := newSurfaceKit(t)
	k.sender.status = 503
	for i := range 4 {
		_ = k.deliver(t, k.msg())
		if k.conds.IsOpen(ConditionKey) {
			t.Fatalf("raised after %d failures", i+1)
		}
	}
	_ = k.deliver(t, k.msg())
	c, ok := k.conds.Get(ConditionKey)
	if !ok {
		t.Fatal("push:relay not raised after 5 transient failures")
	}
	if c.Severity != condition.Warning || c.Summary == "" || len(c.Summary) > condition.MaxSummary || len(c.Fix) > condition.MaxFix {
		t.Fatalf("condition = %+v", c)
	}
	if got := len(k.sender.calls); got != 1 {
		t.Fatalf("sends = %d, want 1: the host's breaker holds the rest (each still counts)", got)
	}
	k.sender.status = 201
	k.now = k.now.Add(DefaultBreakFor)
	if err := k.deliver(t, k.msg()); err != nil {
		t.Fatal(err)
	}
	if k.conds.IsOpen(ConditionKey) {
		t.Fatal("push:relay not cleared by a success")
	}
	// A nil tracker is fine.
	k.s.Conds = nil
	k.sender.status = 503
	_ = k.deliver(t, k.msg())
}

func TestDeliverVanishedSubscription(t *testing.T) {
	k := newSurfaceKit(t)
	if err := k.subs.DeletePushSubscription(testEndpoint); err != nil {
		t.Fatal(err)
	}
	if err := k.deliver(t, k.msg()); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	if len(k.sender.calls) != 0 {
		t.Fatal("sent with no subscription")
	}
	// Moved to another user since Surfaces: not u1's any more.
	if err := k.subs.PutPushSubscription(jam.PushSubscription{Endpoint: testEndpoint, UserID: u2}); err != nil {
		t.Fatal(err)
	}
	if err := k.deliver(t, k.msg()); err != nil || len(k.sender.calls) != 0 {
		t.Fatalf("sent to a device now another user's: %v", err)
	}
}

func TestDeliverInvalidEndpointDeletes(t *testing.T) {
	bad := "https://127.0.0.1/push"
	k := newSurfaceKit(t, jam.PushSubscription{Endpoint: bad, UserID: u1})
	_, err := k.s.Deliver(context.Background(), relay.Delivery{Service: "push", Address: AddressFor(bad), BodyPrefix: string(u1)}, k.msg())
	if err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	if len(k.sender.calls) != 0 || k.subs.has(bad) {
		t.Fatalf("invalid endpoint: sends %d, kept %v", len(k.sender.calls), k.subs.has(bad))
	}
}

func TestDeliverBadSubscriptionKeysDeletes(t *testing.T) {
	k := newSurfaceKit(t)
	k.sender.err = ErrBadSubscription
	if err := k.deliver(t, k.msg()); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	if k.subs.has(testEndpoint) {
		t.Fatal("a subscription with unusable keys was kept")
	}
}

func TestDeliverNoPayload(t *testing.T) {
	k := newSurfaceKit(t)
	k.s.Payload = func(intercom.Squawk, ident.ID) (Payload, bool) { return Payload{}, false }
	if err := k.deliver(t, k.msg()); err != nil || len(k.sender.calls) != 0 {
		t.Fatalf("no longer a recipient: err %v, sends %d", err, len(k.sender.calls))
	}
}

// browserKeys is a test browser's subscription key pair and auth secret.
type browserKeys struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowserKeys(t *testing.T) browserKeys {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return browserKeys{priv: priv, auth: auth}
}

// hostSender answers per endpoint host: a host in down fails as a timeout
// would — after advancing the clock by the request timeout — and every
// other host answers 201.
type hostSender struct {
	k     *surfaceKit
	down  map[string]bool
	calls map[string]int
}

func (h *hostSender) Send(_ context.Context, _ []byte, sub jam.PushSubscription, _ SendOptions) (int, error) {
	host := endpointHost(sub.Endpoint)
	h.calls[host]++
	if h.down[host] {
		h.k.now = h.k.now.Add(SendTimeout)
		return 0, context.DeadlineExceeded
	}
	return 201, nil
}

const (
	hostA = "fcm.googleapis.com"
	hostB = "updates.push.services.mozilla.com"
	epA1  = "https://" + hostA + "/fcm/send/a1"
	epA2  = "https://" + hostA + "/fcm/send/a2"
	epB1  = "https://" + hostB + "/wpush/v2/b1"
)

func newBreakerKit(t *testing.T) (*surfaceKit, *hostSender) {
	t.Helper()
	k := newSurfaceKit(t,
		jam.PushSubscription{Endpoint: epA1, UserID: u1},
		jam.PushSubscription{Endpoint: epA2, UserID: u2},
		jam.PushSubscription{Endpoint: epB1, UserID: u1})
	hs := &hostSender{k: k, down: map[string]bool{}, calls: map[string]int{}}
	k.s.Sender = hs
	return k, hs
}

func (k *surfaceKit) deliverTo(user ident.ID, endpoint string, m intercom.Squawk) error {
	_, err := k.s.Deliver(context.Background(), relay.Delivery{Service: "push", Address: AddressFor(endpoint), BodyPrefix: string(user)}, m)
	return err
}

// A black-holed push service costs one timed-out request per breaker
// window: its other devices fail fast (retried later), and devices on
// another push service are sent without waiting behind it.
func TestDeliverBreakerIsolatesASlowHost(t *testing.T) {
	k, hs := newBreakerKit(t)
	hs.down[hostA] = true
	m := k.msg()
	start := k.now
	for tick := range 4 { // ticks a couple of seconds apart, all inside the window
		for _, d := range []struct {
			user ident.ID
			ep   string
		}{{u1, epA1}, {u2, epA2}} {
			if err := k.deliverTo(d.user, d.ep, m); err == nil {
				t.Fatalf("tick %d: %s delivered while its host is down", tick, d.ep)
			}
		}
		if err := k.deliverTo(u1, epB1, m); err != nil {
			t.Fatalf("tick %d: host B: %v", tick, err)
		}
		k.now = k.now.Add(2 * time.Second)
	}
	if hs.calls[hostA] != 1 || hs.calls[hostB] != 4 {
		t.Fatalf("sends A=%d B=%d, want A attempted once, B every tick", hs.calls[hostA], hs.calls[hostB])
	}
	if spent := k.now.Sub(start); spent > SendTimeout+4*2*time.Second {
		t.Fatalf("ticks took %v: more than one timeout on host A", spent)
	}

	// After the window host A is tried again, and still failing re-opens it.
	k.now = k.now.Add(DefaultBreakFor)
	_ = k.deliverTo(u1, epA1, m)
	_ = k.deliverTo(u2, epA2, m)
	if hs.calls[hostA] != 2 {
		t.Fatalf("after the window: host A sends = %d, want 2", hs.calls[hostA])
	}

	// It recovers: the next try after the window succeeds and closes the
	// breaker, so its other device goes straight out.
	hs.down[hostA] = false
	k.now = k.now.Add(DefaultBreakFor)
	if err := k.deliverTo(u1, epA1, m); err != nil {
		t.Fatalf("host A recovered: %v", err)
	}
	if err := k.deliverTo(u2, epA2, m); err != nil || hs.calls[hostA] != 4 {
		t.Fatalf("after a success: err %v, host A sends %d, want 4", err, hs.calls[hostA])
	}
}

// 429 and 5xx trip the breaker too; its window is configurable; a final
// answer (404, a refusal) does not trip it.
func TestDeliverBreakerStatusesAndWindow(t *testing.T) {
	k := newSurfaceKit(t)
	k.s.BreakFor = 5 * time.Second
	m := k.msg()
	for _, status := range []int{429, 503} {
		k.sender.status = status
		_ = k.deliver(t, m)
		n := len(k.sender.calls)
		if err := k.deliver(t, m); err == nil || len(k.sender.calls) != n {
			t.Fatalf("%d: second deliver err %v, sends %d→%d: want held", status, err, n, len(k.sender.calls))
		}
		k.now = k.now.Add(5 * time.Second)
		k.sender.status = 403
		if err := k.deliver(t, m); err != nil || len(k.sender.calls) != n+1 {
			t.Fatalf("%d: after the 5 s window: err %v, sends %d", status, err, len(k.sender.calls))
		}
		if err := k.deliver(t, m); err != nil || len(k.sender.calls) != n+2 {
			t.Fatalf("%d: a refusal tripped the breaker: err %v, sends %d", status, err, len(k.sender.calls))
		}
	}
}
