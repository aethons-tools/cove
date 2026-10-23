package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/relay"
)

// pushServer is a TLS push service stand-in: it records every request and
// answers with status (and Location, for a redirect).
type pushServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []recorded
	status   int
	location string
}

type recorded struct {
	header http.Header
	path   string
	body   []byte
}

func newPushServer(t *testing.T) *pushServer {
	t.Helper()
	ps := &pushServer{status: http.StatusCreated}
	ps.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.reqs = append(ps.reqs, recorded{header: r.Header.Clone(), path: r.URL.Path, body: body})
		status, loc := ps.status, ps.location
		ps.mu.Unlock()
		if loc != "" {
			w.Header().Set("Location", loc)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

// transport reaches the stand-in for any host (the endpoint names a real
// push service, as the allowlist requires), trusting its certificate.
func (ps *pushServer) transport() http.RoundTripper {
	tr := ps.srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = "example.com" // in httptest's certificate
	addr := ps.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return tr
}

func (ps *pushServer) requests() []recorded {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]recorded(nil), ps.reqs...)
}

// decrypt opens an aes128gcm Web Push body (RFC 8188 + RFC 8291) with the
// browser's keys.
func decrypt(t *testing.T, bk browserKeys, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt, idlen := body[:16], int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatalf("keyid: %v", err)
	}
	shared, err := bk.priv.ECDH(as)
	if err != nil {
		t.Fatal(err)
	}
	info := "WebPush: info\x00" + string(bk.priv.PublicKey().Bytes()) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, shared, bk.auth, info, 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("no last-record delimiter in %q", plain)
	}
	return plain[:len(plain)-1]
}

// verifyJWT checks an ES256 JWT's signature with the base64url uncompressed
// public key and returns its claims.
func verifyJWT(t *testing.T, token, pubB64 string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), raw) //nolint:staticcheck // test-only point decode for ecdsa.Verify
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("jwt signature: %v (%d bytes)", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("jwt signature does not verify with the VAPID public key")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// The whole send path with real crypto: the surface, webpush-go and a push
// service stand-in that decrypts with the browser's keys.
func TestDeliverRealCrypto(t *testing.T) {
	ps := newPushServer(t)
	bk := newBrowserKeys(t)
	vapidPriv, vapidPub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://fcm.googleapis.com/fcm/send/device-1"
	sub := jam.PushSubscription{
		Endpoint: endpoint, UserID: u1,
		P256dh: base64.RawURLEncoding.EncodeToString(bk.priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(bk.auth),
	}
	subs := newFakeSubs(sub)
	now := time.Now()
	want := Payload{Title: "Bob · Alice", Body: "hello", Channel: string(chat), ID: "sq-9", Unread: 4}
	s := &Surface{
		Store:   subs,
		Sender:  NewWebPushSender(ps.transport()),
		Payload: func(intercom.Squawk, ident.ID) (Payload, bool) { return want, true },
		Opts:    SendOptions{Subscriber: "mailto:ops@example.com", VAPIDPrivate: vapidPriv, VAPIDPublic: vapidPub},
		Now:     func() time.Time { return now },
	}
	m := intercom.Squawk{ID: "sq-9", Channel: chat, From: u2, Body: "hello", At: now}
	if _, err := s.Deliver(context.Background(), relay.Delivery{Service: "push", Address: AddressFor(endpoint), BodyPrefix: string(u1)}, m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	reqs := ps.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if r.path != "/fcm/send/device-1" {
		t.Fatalf("path = %q", r.path)
	}
	topic := sha256.Sum256([]byte(chat))
	if r.header.Get("TTL") != "86400" || r.header.Get("Topic") != hex.EncodeToString(topic[:])[:32] || r.header.Get("Content-Encoding") != "aes128gcm" {
		t.Fatalf("headers = %v", r.header)
	}
	auth := r.header.Get("Authorization")
	tok, k, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !strings.HasPrefix(auth, "vapid t=") || !ok || k != vapidPub {
		t.Fatalf("Authorization = %q", auth)
	}
	claims := verifyJWT(t, tok, vapidPub)
	if claims["sub"] != "mailto:ops@example.com" || claims["aud"] != "https://fcm.googleapis.com" {
		t.Fatalf("jwt claims = %v", claims)
	}
	var got Payload
	if err := json.Unmarshal(decrypt(t, bk, r.body), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decrypted payload = %+v, want %+v", got, want)
	}
	if _, ok := subs.touched[endpoint]; !ok {
		t.Fatal("success not recorded")
	}

	// An https: subject passes through as is.
	s.Opts.Subscriber = "https://jam.example.com/contact"
	if _, err := s.Deliver(context.Background(), relay.Delivery{Service: "push", Address: AddressFor(endpoint), BodyPrefix: string(u1)}, m); err != nil {
		t.Fatal(err)
	}
	reqs = ps.requests()
	tok, _, _ = strings.Cut(strings.TrimPrefix(reqs[1].header.Get("Authorization"), "vapid t="), ", k=")
	if sub := verifyJWT(t, tok, vapidPub)["sub"]; sub != "https://jam.example.com/contact" {
		t.Fatalf("https subject = %v", sub)
	}
}

// The sender never follows a redirect: the push service's answer is final.
func TestWebPushSenderRefusesRedirects(t *testing.T) {
	ps := newPushServer(t)
	ps.status, ps.location = http.StatusTemporaryRedirect, "https://fcm.googleapis.com/elsewhere"
	bk := newBrowserKeys(t)
	vapidPriv, vapidPub, _ := webpush.GenerateVAPIDKeys()
	sub := jam.PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/d",
		P256dh:   base64.RawURLEncoding.EncodeToString(bk.priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(bk.auth),
	}
	status, err := NewWebPushSender(ps.transport()).Send(context.Background(), []byte(`{}`), sub,
		SendOptions{TTL: 60, Subscriber: "mailto:a@b.c", VAPIDPrivate: vapidPriv, VAPIDPublic: vapidPub})
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("Send = %d, %v", status, err)
	}
	if n := len(ps.requests()); n != 1 {
		t.Fatalf("requests = %d: the redirect was followed", n)
	}
	c := NewWebPushSender(nil).client
	if c.Timeout != SendTimeout || c.CheckRedirect == nil {
		t.Fatalf("default client: timeout %v, redirect check %v", c.Timeout, c.CheckRedirect != nil)
	}
}

func TestWebPushSenderBadKeys(t *testing.T) {
	vapidPriv, vapidPub, _ := webpush.GenerateVAPIDKeys()
	bk := newBrowserKeys(t)
	good := base64.RawURLEncoding.EncodeToString(bk.priv.PublicKey().Bytes())
	for _, sub := range []jam.PushSubscription{
		{Endpoint: testEndpoint, P256dh: "not base64!", Auth: base64.RawURLEncoding.EncodeToString(bk.auth)},
		{Endpoint: testEndpoint, P256dh: base64.RawURLEncoding.EncodeToString(make([]byte, 65)), Auth: base64.RawURLEncoding.EncodeToString(bk.auth)},
		{Endpoint: testEndpoint, P256dh: good, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 5))},
	} {
		_, err := NewWebPushSender(nil).Send(context.Background(), []byte(`{}`), sub,
			SendOptions{TTL: 60, Subscriber: "mailto:a@b.c", VAPIDPrivate: vapidPriv, VAPIDPublic: vapidPub})
		if !errors.Is(err, ErrBadSubscription) {
			t.Fatalf("%+v: err = %v, want ErrBadSubscription", sub, err)
		}
	}
}

// A transport failure is an error without the endpoint (a capability URL)
// in it: it reaches the logs and the health condition.
func TestWebPushSenderErrorHidesEndpoint(t *testing.T) {
	ps := newPushServer(t)
	rt := ps.transport()
	ps.srv.Close()
	vapidPriv, vapidPub, _ := webpush.GenerateVAPIDKeys()
	bk := newBrowserKeys(t)
	sub := jam.PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/secret-device",
		P256dh:   base64.RawURLEncoding.EncodeToString(bk.priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(bk.auth),
	}
	_, err := NewWebPushSender(rt).Send(context.Background(), []byte(`{}`), sub,
		SendOptions{TTL: 60, Subscriber: "mailto:a@b.c", VAPIDPrivate: vapidPriv, VAPIDPublic: vapidPub})
	if err == nil || strings.Contains(err.Error(), "secret-device") {
		t.Fatalf("err = %v", err)
	}
}
