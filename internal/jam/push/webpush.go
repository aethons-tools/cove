package push

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/aethons-tools/cove/internal/jam"
)

// SendTimeout bounds one push request.
const SendTimeout = 10 * time.Second

// WebPushSender is the Sender over webpush-go: RFC 8291 encryption, a VAPID
// JWT, and an HTTP client that never follows a redirect.
type WebPushSender struct {
	client *http.Client
}

// NewWebPushSender is a sender over rt (nil: a clone of
// http.DefaultTransport, honouring the proxy environment), with a
// SendTimeout and no redirects.
func NewWebPushSender(rt http.RoundTripper) *WebPushSender {
	if rt == nil {
		rt = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &WebPushSender{client: &http.Client{
		Transport:     rt,
		Timeout:       SendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Send implements Sender. Keys that can't encrypt are ErrBadSubscription
// (no request is made); a transport error never carries the endpoint URL.
func (w *WebPushSender) Send(ctx context.Context, payload []byte, sub jam.PushSubscription, opts SendOptions) (int, error) {
	if err := checkKeys(sub); err != nil {
		return 0, err
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload,
		&webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}},
		&webpush.Options{
			HTTPClient: w.client,
			// webpush-go prefixes "mailto:" to any subject not starting
			// "https:"; hand it the bare address so the JWT's sub is the
			// configured subject.
			Subscriber:      strings.TrimPrefix(opts.Subscriber, "mailto:"),
			TTL:             opts.TTL,
			Topic:           opts.Topic,
			VAPIDPublicKey:  opts.VAPIDPublic,
			VAPIDPrivateKey: opts.VAPIDPrivate,
		})
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			return 0, fmt.Errorf("%s push service: %w", ue.Op, ue.Err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// checkKeys refuses a subscription whose p256dh is not a P-256 point or
// whose auth secret is not 16 bytes.
func checkKeys(sub jam.PushSubscription) error {
	dh, err := decodeKey(sub.P256dh)
	if err != nil {
		return fmt.Errorf("%w: p256dh not base64", ErrBadSubscription)
	}
	if _, err := ecdh.P256().NewPublicKey(dh); err != nil {
		return fmt.Errorf("%w: p256dh not a P-256 point", ErrBadSubscription)
	}
	if auth, err := decodeKey(sub.Auth); err != nil || len(auth) != 16 {
		return fmt.Errorf("%w: auth not 16 bytes", ErrBadSubscription)
	}
	return nil
}

// decodeKey decodes a subscription key in any base64 alphabet, padded or not.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
