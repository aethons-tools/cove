package userui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithPush enables the Web Push subscription API (GET /app/user/push/key,
// POST and DELETE /app/user/push/subscriptions) with publicKey, the VAPID
// public key in base64url. "" leaves it off: those routes answer 404.
func WithPush(publicKey string) Option {
	return func(h *handler) { h.pushKey = publicKey }
}

const (
	pushBodyMax     = 8 << 10 // request body cap for the push routes
	pushEndpointMax = 2048    // longest accepted endpoint URL, bytes
	pushUAMax       = 256     // stored User-Agent is truncated to this many bytes
	p256dhLen       = 65      // an uncompressed P-256 point
	authLen         = 16      // the browser's auth secret
)

// pushSubscriptionJSON is the browser's PushSubscription.toJSON().
type pushSubscriptionJSON struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (h *handler) pushRoutes(mux *http.ServeMux) {
	if h.pushKey == "" {
		return
	}
	mux.HandleFunc("GET /app/user/push/key", h.pushKeyRoute)
	mux.HandleFunc("POST /app/user/push/subscriptions", h.pushSubscribe)
	mux.HandleFunc("DELETE /app/user/push/subscriptions", h.pushUnsubscribe)
}

func (h *handler) pushKeyRoute(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"key": h.pushKey})
}

// pushSubscribe stores this device's subscription for the caller (upsert by
// endpoint, so a device that switched users moves to the new one).
func (h *handler) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	var in pushSubscriptionJSON
	if !decodePushBody(w, r, &in) {
		return
	}
	if !validPushEndpoint(in.Endpoint) || !keyLen(in.Keys.P256dh, p256dhLen) || !keyLen(in.Keys.Auth, authLen) {
		http.Error(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	s := jam.PushSubscription{
		Endpoint:  in.Endpoint,
		UserID:    p.UserID,
		P256dh:    in.Keys.P256dh,
		Auth:      in.Keys.Auth,
		UserAgent: truncateUTF8(r.UserAgent(), pushUAMax),
		CreatedAt: time.Now().UTC(),
	}
	if err := h.d.Store.PutPushSubscription(s); err != nil {
		h.lg.Error("userui push subscribe failed", "user", p.UserID, "error", err.Error())
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushUnsubscribe drops the endpoint only if the caller owns it; either way
// it answers 204, so it never says whose an endpoint is.
func (h *handler) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodePushBody(w, r, &in) {
		return
	}
	if in.Endpoint == "" {
		http.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	if _, err := h.d.Store.DeleteUserPushSubscription(p.UserID, in.Endpoint); err != nil {
		h.lg.Error("userui push unsubscribe failed", "user", p.UserID, "error", err.Error())
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodePushBody reads a capped JSON body into v, answering 400 itself on
// failure.
func decodePushBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, pushBodyMax)).Decode(v); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func validPushEndpoint(s string) bool {
	if s == "" || len(s) > pushEndpointMax {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// keyLen reports whether s is base64url (padding optional) of exactly n bytes.
func keyLen(s string, n int) bool {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return err == nil && len(b) == n
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
